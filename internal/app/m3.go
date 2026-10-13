package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// M3Options tunes the generation services of Build; every field is optional.
type M3Options struct {
	// Clock drives the runner, the reconciler, Wait and upload expiry; nil => real timers over Options.Now.
	Clock clock.Clock
	// Backend replaces the backend MM_BACKEND selects (tests: providertest, an adversarial fake). It is used
	// only when MM_BACKEND is "fake" or "runpod" and counts as keys present.
	Backend provider.Backend
	// Fake overrides the fake backend's options (speed, permissions, adversarial switches).
	Fake *fake.Options
	// RunPod points the serverless backend at other hosts (runpodtest); nil => the production hosts.
	RunPod *runpod.BackendOptions
	// Ingest replaces the production ingest bridge (tests: a stub ingester).
	Ingest jobs.Ingester
	// Shutdown is the context whose end starts server shutdown (jobs.Wait returns on it); nil => never.
	Shutdown context.Context
	// Log is the logger of the runner and the reconciler; nil => slog.Default().
	Log *slog.Logger
}

// Backend identifies the active provider backend of a process.
type backendInfo struct {
	b          provider.Backend
	keysLoaded bool
	fake       *fake.Backend
}

// wakeRef lets generation, upload and the ops wake a runner constructed after them (nil-safe).
type wakeRef struct{ f atomic.Pointer[func()] }

func (w *wakeRef) set(f func()) { w.f.Store(&f) }

func (w *wakeRef) call() {
	if f := w.f.Load(); f != nil {
		(*f)()
	}
}

// clockFromNow adapts a now func to clock.Clock (real timers).
type clockFromNow struct{ now func() time.Time }

func (c clockFromNow) Now() time.Time                       { return c.now() }
func (c clockFromNow) NewTimer(d time.Duration) clock.Timer { return clock.Real.NewTimer(d) }

// selectBackend implements the MM_BACKEND choice (C68): none => no backend; fake => the in-process fake;
// runpod => the serverless backend when runpod.env is present, no backend (keys_absent) when it is not.
func selectBackend(cfg config.Config, o M3Options, clk clock.Clock) (backendInfo, error) {
	switch cfg.Backend {
	case config.BackendFake:
		if o.Backend != nil {
			return backendInfo{b: o.Backend, keysLoaded: true}, nil
		}
		fo := fake.Options{}
		if o.Fake != nil {
			fo = *o.Fake
		}
		fo.Clock = clk
		if cfg.FakeBackend != "" && cfg.FakeBackend != "fast" && fo.Speed.Slow == 0 {
			if d, ok := cutPrefix(cfg.FakeBackend, "slow:"); ok {
				if dur, err := time.ParseDuration(d); err == nil {
					fo.Speed.Slow = dur
				}
			}
		}
		fb := fake.New(fo)
		return backendInfo{b: fb, keysLoaded: true, fake: fb}, nil
	case config.BackendRunPod:
		keys, ok, err := config.LoadRunPodKeys(cfg.SecretsDir)
		if err != nil {
			return backendInfo{}, err
		}
		if o.Backend != nil {
			return backendInfo{b: o.Backend, keysLoaded: true}, nil
		}
		if !ok {
			return backendInfo{keysLoaded: false}, nil
		}
		bo := runpod.BackendOptions{}
		if o.RunPod != nil {
			bo = *o.RunPod
		}
		bo.Keys, bo.Clock = keys, clk
		bo.MaxRuntime, bo.ColdStartS, bo.RecordDir = cfg.JobMaxRuntime, cfg.ColdStartS, cfg.RunPodRecordDir
		return backendInfo{b: runpod.NewBackend(bo), keysLoaded: true}, nil
	}
	return backendInfo{}, nil
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

// ensureProviderState writes the provider_state row of the active backend once: the fake is armed by
// system:default, the serverless backend is disarmed (D-M3-8). An existing row is never rewritten.
func ensureProviderState(ctx context.Context, st *store.Store, name string, now time.Time) error {
	if name == "" {
		return nil
	}
	armed := 0
	if name == provider.BackendFake {
		armed = 1
	}
	if _, err := st.W.ExecContext(ctx, `INSERT OR IGNORE INTO provider_state(backend, armed, changed_by, changed_at) VALUES (?,?,?,?)`,
		name, armed, "system:default", store.FormatTime(now)); err != nil {
		return fmt.Errorf("app: provider state: %w", err)
	}
	return nil
}

// instanceID is a random runner instance id (never a hostname).
func instanceID(r io.Reader) string {
	if r == nil {
		r = rand.Reader
	}
	var b [8]byte
	_, _ = io.ReadFull(r, b[:])
	return hex.EncodeToString(b[:])
}

// m3Services is what buildM3 adds to Services.
type m3Services struct {
	Lineage    *lineage.Service
	Spend      *spend.Service
	Generation *generation.Service
	Jobs       *jobs.Service
	Provider   *jobs.ProviderAdmin
	Uploads    *upload.Service
	Backend    provider.Backend
	Fake       *fake.Backend
	Gate       *provider.Gate
	Runner     *jobs.Runner
	Reconciler *jobs.Reconciler
	Hub        *jobs.Hub
}

// buildM3 constructs the generation services (C39 amended): backend selection, the provider_state
// default row, the leaf services and, when a backend exists and the runner is enabled, an unstarted
// runner and reconciler. Nothing here calls a backend method.
func buildM3(ctx context.Context, cfg config.Config, sec config.Secrets, st *store.Store, data *os.Root, lib *library.Service, notifier notify.Notifier,
	now func() time.Time, newID func() string, rnd io.Reader, o M3Options, wake *wakeRef) (*m3Services, error) {
	clk := o.Clock
	if clk == nil {
		clk = clockFromNow{now}
	}
	bi, err := selectBackend(cfg, o, clk)
	if err != nil {
		return nil, err
	}
	m := &m3Services{Backend: bi.b, Fake: bi.fake, Hub: jobs.NewHub()}
	name := ""
	if bi.b != nil {
		name = bi.b.Name()
	} else if cfg.Backend == config.BackendRunPod {
		name = provider.BackendRunPod // keys absent: the row is still created (disarmed)
	}
	if err := ensureProviderState(ctx, st, name, now()); err != nil {
		return nil, err
	}
	m.Gate = provider.NewGate(st.R, provider.GateConfig{Mode: cfg.Backend, KeysLoaded: bi.keysLoaded, ColdStartS: cfg.ColdStartS, MaxRuntime: cfg.JobMaxRuntime}, now)

	m.Lineage = lineage.New(lineage.Options{Store: st, Now: now})
	m.Spend = spend.New(spend.Options{Store: st, Now: now, NewID: newID, Rand: rnd, ConfirmKey: sec.ConfirmKey()})
	engines := registry.Default()
	m.Generation = generation.New(generation.Options{
		Store: st, Library: lib, Lineage: m.Lineage, Spend: m.Spend, Engines: engines, Gate: m.Gate, Notify: notifier,
		PublicURL: cfg.PublicURL, Now: now, NewID: newID, Rand: rnd, Wake: wake.call,
		ColdStartS: cfg.ColdStartS, KillSlackS: cfg.KillSlackS, MaxRuntime: cfg.JobMaxRuntime, PriceMarginPPM: cfg.PriceMarginPPM,
	})
	m.Uploads = upload.NewService(upload.ServiceOptions{Data: data, Signer: upload.NewSigner(sec.Key), Store: st, Clock: clk, NewID: newID, PublicURL: cfg.PublicURL, Wake: wake.call})
	m.Jobs = jobs.New(jobs.Options{Store: st, Clock: clk, Hub: m.Hub, Gate: m.Gate})
	m.Provider = jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: m.Gate, Clock: clk})

	if bi.b != nil && cfg.Runner {
		ing := o.Ingest
		if ing == nil {
			ing = jobs.NewIngestBridge(jobs.BridgeOptions{Data: data, Library: lib, Lineage: m.Lineage, Store: st, Clock: clk, NewID: newID})
		}
		log := o.Log
		if log == nil {
			log = slog.Default()
		}
		owner := instanceID(rnd)
		m.Runner = jobs.NewRunner(jobs.RunnerOptions{
			Store: st, Backend: bi.b, Engines: engines, Spend: m.Spend, Uploads: m.Uploads, Ingest: ing, Notify: notifier, Hub: m.Hub,
			Clock: clk, NewID: newID, Owner: owner, PublicURL: cfg.PublicURL, TTL: cfg.JobTTL, MaxRuntime: cfg.JobMaxRuntime,
			ColdStartS: cfg.ColdStartS, KillSlackS: cfg.KillSlackS, Expect: provider.WorkerExpect{Weights: yue2.Weights}, Log: log,
		})
		m.Reconciler = jobs.NewReconciler(jobs.ReconcilerOptions{
			Store: st, Backend: bi.b, Spend: m.Spend, Uploads: m.Uploads, Notify: notifier, Runner: m.Runner, Clock: clk, NewID: newID,
			Owner: owner, Log: log, MeterKey: sec.MeterKey(), PublicURL: cfg.PublicURL, BillingLag: cfg.BillingLag, ResidualHold: cfg.ResidualHold,
			ColdStartS: cfg.ColdStartS, KillSlackS: cfg.KillSlackS, MaxRuntime: cfg.JobMaxRuntime, PriceMarginPPM: cfg.PriceMarginPPM,
			GateCfg: m.Gate.Config(),
		})
		wake.set(m.Runner.Wake)
	}
	return m, nil
}
