package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
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
	// Live marks a process that serves real traffic (serve, dev): the runner gets the claim preflight (D-M3-33)
	// and the fake backend gets a simulated worker that talks to the upload handlers in process. Rigs leave it
	// false and drive the runner and the fake worker themselves.
	Live bool
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
func selectBackend(cfg config.Config, o M3Options, clk clock.Clock, ups *upload.Service) (backendInfo, error) {
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
		if o.Live && fo.Worker == nil && ups != nil {
			fo.Worker = fake.NewSimWorker(fake.SimWorkerOptions{
				Uploader: fake.HandlerUploader{UploadHandler: ups.Handler(), StartHandler: ups.StartHandler()},
				Clock:    clk,
				Tools:    mediatool.Tools{FFmpeg: cfg.FFmpeg, FLAC: cfg.FLAC, Metaflac: cfg.Metaflac},
				Weights:  yue2.Weights,
			})
		}
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
		if bo.ProbeJSON == nil {
			// the committed key probe: without it Capabilities() stays conservative (billing "none") and the
			// reconciler never reads RunPod's hourly billing. A test may set its own.
			bo.ProbeJSON = runpod.EmbeddedCapabilities
		}
		bo.MaxRuntime, bo.ColdStartS, bo.RecordDir = cfg.JobMaxRuntime, cfg.ColdStartS, cfg.RunPodRecordDir
		if bo.Recorder == nil && cfg.RunPodRecordDir != "" {
			// MM_RUNPOD_RECORD_DIR: the RunPod calls of the runner and the reconciler are made by this process, so
			// this is where the fixtures are recorded (redacted: keys, signed URLs, endpoint and worker ids).
			rec, err := runpod.NewFileRecorder(cfg.RunPodRecordDir, keys.EndpointID, keys.Jobs, keys.Read, keys.Write)
			if err != nil {
				return backendInfo{}, err
			}
			bo.Recorder = rec
		}
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
	now func() time.Time, newID func() string, rnd io.Reader, o M3Options, wake *wakeRef, pipe *ingest.Pipeline, ears *ears.Service) (*m3Services, error) {
	clk := o.Clock
	if clk == nil {
		clk = clockFromNow{now}
	}
	m := &m3Services{Hub: jobs.NewHub()}
	m.Uploads = upload.NewService(upload.ServiceOptions{Data: data, Signer: upload.NewSigner(sec.Key), Store: st, Clock: clk, NewID: newID, PublicURL: cfg.PublicURL, Wake: wake.call})
	bi, err := selectBackend(cfg, o, clk, m.Uploads)
	if err != nil {
		return nil, err
	}
	m.Backend, m.Fake = bi.b, bi.fake
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
		Songwriting: songwriting.New(st), // the minimum-render control (X4) reads songwriting.min_duration_s
	})
	m.Jobs = jobs.New(jobs.Options{Store: st, Clock: clk, Hub: m.Hub, Gate: m.Gate, Spend: m.Spend, Wake: wake.call})
	m.Provider = jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: m.Gate, Clock: clk})
	if bi.b != nil {
		m.Provider.UseCapabilities(bi.b.Capabilities)
	}

	if bi.b != nil && cfg.Runner {
		log := o.Log
		if log == nil {
			log = slog.Default()
		}
		ing := o.Ingest
		if ing == nil {
			if pipe == nil {
				log.Warn("generation ingest unavailable: ffmpeg tools missing; finished takes will fail INGEST_FAILED")
			}
			ing = jobs.NewIngestBridge(jobs.BridgeOptions{Data: data, Pipeline: pipe, Library: lib, Ears: ears, Lineage: m.Lineage, Spend: m.Spend,
				Uploads: m.Uploads, Store: st, Clock: clk, NewID: newID, Log: log})
		}
		var pre func(context.Context, int64) error
		if o.Live {
			pre = livePreflight(cfg, bi, m.Uploads)
		}
		owner := instanceID(rnd)
		m.Runner = jobs.NewRunner(jobs.RunnerOptions{
			Store: st, Backend: bi.b, Engines: engines, Spend: m.Spend, Uploads: m.Uploads, Ingest: ing, Notify: notifier, Hub: m.Hub,
			Clock: clk, NewID: newID, Owner: owner, PublicURL: cfg.PublicURL, TTL: cfg.JobTTL, MaxRuntime: cfg.JobMaxRuntime,
			ColdStartS: cfg.ColdStartS, KillSlackS: cfg.KillSlackS, Expect: provider.WorkerExpect{Weights: yue2.Weights}, Log: log,
			Preflight: pre,
		})
		m.Reconciler = jobs.NewReconciler(jobs.ReconcilerOptions{
			Store: st, Backend: bi.b, Spend: m.Spend, Uploads: m.Uploads, Notify: notifier, Runner: m.Runner, Clock: clk, NewID: newID,
			Owner: owner, Log: log, MeterKey: sec.MeterKey(), PublicURL: cfg.PublicURL, BillingLag: cfg.BillingLag, ResidualHold: cfg.ResidualHold,
			ColdStartS: cfg.ColdStartS, KillSlackS: cfg.KillSlackS, MaxRuntime: cfg.JobMaxRuntime, PriceMarginPPM: cfg.PriceMarginPPM,
			GateCfg: m.Gate.Config(),
		})
		// get_provider_status shows what the reconciler's last sweep saw (workers, live endpoint, drift); without
		// this the status always had workers: null (the canary's max_workers_seen was 0 in every run).
		m.Provider.UseSweep(m.Reconciler.Snapshot)
		wake.set(m.Runner.Wake)
	}
	return m, nil
}

// livePreflight is the runner's claim preflight (D-M3-33). Against the serverless backend the upload route
// is probed through the public URL, because the worker's PUT arrives that way. The fake backend's simulated
// worker never leaves the process, so the same probe goes straight into the upload handler: the route check
// stays meaningful and a dev address that is not reachable from itself does not stall every claim.
func livePreflight(cfg config.Config, bi backendInfo, ups *upload.Service) func(context.Context, int64) error {
	po := jobs.PreflightOptions{DataDir: cfg.DataDir, PublicURL: cfg.PublicURL}
	if bi.fake != nil && ups != nil {
		po.HTTP = &http.Client{Transport: handlerTransport{ups.Handler()}, Timeout: 5 * time.Second}
	}
	return jobs.NewPreflight(po)
}

// handlerTransport serves a request with an http.Handler in process.
type handlerTransport struct{ h http.Handler }

type memResponse struct {
	hdr  http.Header
	code int
	body bytes.Buffer
}

func (m *memResponse) Header() http.Header { return m.hdr }
func (m *memResponse) WriteHeader(c int) {
	if m.code == 0 {
		m.code = c
	}
}
func (m *memResponse) Write(b []byte) (int, error) {
	m.WriteHeader(http.StatusOK)
	return m.body.Write(b)
}

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.RequestURI = r.URL.RequestURI()
	rec := &memResponse{hdr: http.Header{}}
	t.h.ServeHTTP(rec, r)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return &http.Response{StatusCode: rec.code, Status: http.StatusText(rec.code), Header: rec.hdr, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Body: io.NopCloser(&rec.body), Request: r}, nil
}
