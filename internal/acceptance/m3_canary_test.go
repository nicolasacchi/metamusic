package acceptance_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// wiredFake is the production wiring (app.Build, the runner, the reconciler, the ingest bridge) over the fake
// backend with its simulated worker, on the real clock, behind a loopback server: what `metamusic dev` runs.
type wiredFake struct {
	svc   *app.Services
	st    *store.Store
	url   string
	token string
}

func wireFake(t *testing.T, fo fake.Options) *wiredFake {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy: renders audio and runs the wired runner")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "secret_key"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 1000, DataDir: dir, SecretsDir: secrets, Addr: ts.Listener.Addr().String(),
		Backend: config.BackendFake, Runner: true, JobMaxRuntime: 15 * time.Minute, JobTTL: 2 * time.Hour, ColdStartS: 180, KillSlackS: 15,
		PriceMarginPPM: 100000, BillingLag: 6 * time.Hour, ResidualHold: 48 * time.Hour, FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac"}
	sec, err := config.LoadSecrets(secrets, "local")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.Build(ctx, cfg, sec, st, root, app.Options{M3: app.M3Options{Live: true, Shutdown: ctx, Fake: &fo}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := httpserver.New(svc.Server)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = h
	ts.Start()
	var bg sync.WaitGroup
	bg.Add(3)
	go func() { defer bg.Done(); _ = svc.Runner.Run(ctx) }()
	go func() { defer bg.Done(); _ = svc.Reconciler.Run(ctx) }()
	// the reconciler sweeps every 10 s; a fake take lives for seconds, so the canary (which now insists on
	// seeing a live worker in get_provider_status) needs the snapshot refreshed far more often
	go func() {
		defer bg.Done()
		for ctx.Err() == nil {
			_ = svc.Reconciler.Pass(ctx)
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		bg.Wait()
		ts.Close()
		cctx, c2 := context.WithTimeout(context.Background(), time.Minute)
		defer c2()
		_ = svc.Close(cctx)
		_ = root.Close()
		_ = st.Close()
	})
	deadline := time.Now().Add(20 * time.Second)
	for !svc.Runner.Holding() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !svc.Runner.Holding() {
		t.Fatal("the runner never took the lease")
	}
	if err := svc.Reconciler.Pass(ctx); err != nil {
		t.Fatalf("reconciler pass: %v", err)
	}
	tok, _, err := svc.Tokens.Create(ctx, "canary", []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate})
	if err != nil {
		t.Fatal(err)
	}
	return &wiredFake{svc: svc, st: st, url: ts.URL, token: tok}
}

func (w *wiredFake) run(t *testing.T, c runpod.CanaryCase, mod func(*runpod.CanaryOptions)) *runpod.CanaryReport {
	t.Helper()
	o := runpod.CanaryOptions{BaseURL: w.url, Token: w.token, Case: c, SweepEvery: 200 * time.Millisecond, WaitS: 5, AfterCancelWait: time.Second}
	if mod != nil {
		mod(&o)
	}
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil {
		t.Fatalf("canary %s: %v", c, err)
	}
	return rep
}

func (w *wiredFake) batches(t *testing.T) int {
	t.Helper()
	var n int
	if err := w.st.R.QueryRow(`SELECT COUNT(*) FROM batches`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCanary_ScriptAgainstFake: the canary script (REST with a read+write+generate token) against the wired
// server over the fake backend: every case ends in its expected outcome, and the refusals create nothing.
func TestCanary_ScriptAgainstFake(t *testing.T) {
	t.Parallel()
	quick := fake.Options{IdleS: 1, ColdStartS: 1, Speed: fake.Speed{Slow: 6 * time.Second}} // long enough for the canary to see the worker
	t.Run("pass cases", func(t *testing.T) {
		t.Parallel()
		for _, c := range []runpod.CanaryCase{runpod.CaseMain, runpod.CaseCotFull, runpod.CaseTerminate} {
			t.Run(string(c), func(t *testing.T) {
				t.Parallel()
				w := wireFake(t, quick)
				rep := w.run(t, c, nil)
				if rep.Outcome != "pass" || rep.Exit != runpod.ExitPass {
					t.Fatalf("report %+v", rep)
				}
				for _, ck := range rep.Checks {
					if !ck.OK {
						t.Errorf("check %q failed: %s", ck.Name, ck.Note)
					}
				}
				switch c {
				case runpod.CaseTerminate:
					if rep.FinalState != "cancelled" {
						t.Errorf("terminate ended %s", rep.FinalState)
					}
				case runpod.CaseMain, runpod.CaseCotFull:
					if rep.FinalState != "completed" {
						t.Errorf("%s ended %s", c, rep.FinalState)
					}
				}
			})
		}
	})
	t.Run("kill-switch passes only when the endpoint is deactivated during the run", func(t *testing.T) {
		t.Parallel()
		for _, flip := range []bool{true, false} {
			w := wireFake(t, fake.Options{IdleS: 1, ColdStartS: 1, Speed: fake.Speed{Slow: 3 * time.Second}})
			if flip { // the operator's `provision --deactivate`, once the job is running (the fake skips "generating")
				go func() {
					for i := 0; i < 600; i++ {
						var n int
						if err := w.st.R.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state IN ('starting', 'generating', 'transferring')`).Scan(&n); err == nil && n > 0 {
							time.Sleep(2 * time.Second) // past the 1 s cold start: the worker holds the job, so only idle workers stop
							_ = w.svc.Fake.ScaleToZero(context.Background())
							return
						}
						time.Sleep(50 * time.Millisecond)
					}
				}()
			}
			rep := w.run(t, runpod.CaseKillSwitch, func(o *runpod.CanaryOptions) { o.KillSwitchConfirm = true })
			switch {
			case flip && (rep.Exit != runpod.ExitPass || rep.Outcome != "pass"):
				t.Errorf("flipped: report %+v", rep)
			case !flip && (rep.Exit != runpod.ExitFail || !strings.Contains(rep.Reason, "kill_switch_flipped")):
				t.Errorf("not flipped: report %+v", rep)
			}
		}
	})
	t.Run("timeout is refused on a server not configured for it", func(t *testing.T) {
		t.Parallel()
		w := wireFake(t, quick)
		rep := w.run(t, runpod.CaseTimeout, nil)
		if rep.Exit != runpod.ExitRefused || w.batches(t) != 0 {
			t.Fatalf("report %+v, batches %d", rep, w.batches(t))
		}
	})
	t.Run("max-usd below the ceiling creates nothing", func(t *testing.T) {
		t.Parallel()
		w := wireFake(t, quick)
		rep := w.run(t, runpod.CaseMain, func(o *runpod.CanaryOptions) { o.MaxUSD = money.MicroUSD(1000) }) // $0.001
		if rep.Exit != runpod.ExitRefused || w.batches(t) != 0 {
			t.Fatalf("report %+v, batches %d", rep, w.batches(t))
		}
	})
	t.Run("a disarmed provider is refused", func(t *testing.T) {
		t.Parallel()
		w := wireFake(t, quick)
		if _, err := w.st.W.Exec(`UPDATE provider_state SET armed = 0, changed_by = 'system:test' WHERE backend = 'fake'`); err != nil {
			t.Fatal(err)
		}
		rep := w.run(t, runpod.CaseMain, nil)
		if rep.Exit != runpod.ExitRefused || w.batches(t) != 0 {
			t.Fatalf("report %+v, batches %d", rep, w.batches(t))
		}
	})
	t.Run("a cold start above the start window aborts and confirms the workers", func(t *testing.T) {
		t.Parallel()
		w := wireFake(t, fake.Options{IdleS: 1, ColdStartS: 400})
		var log strings.Builder
		rep := w.run(t, runpod.CaseMain, func(o *runpod.CanaryOptions) { o.AbortAfter = 2 * time.Second; o.Log = &log })
		if rep.Exit != runpod.ExitAborted || rep.Outcome != "aborted" {
			t.Fatalf("report %+v", rep)
		}
		// confirmed zero workers, or else the exact manual command
		if !strings.Contains(rep.Reason, "workers are 0") && !strings.Contains(rep.Reason, "metamusic runpod provision --deactivate") {
			t.Errorf("neither a zero-workers confirmation nor the manual command in %q", rep.Reason)
		}
		// the abort asked for the cancel; the runner carries it out
		var asked int
		if err := w.st.R.QueryRow(`SELECT COUNT(*) FROM jobs WHERE cancel_requested_at IS NOT NULL`).Scan(&asked); err != nil || asked != 1 {
			t.Errorf("%d jobs with a cancel intent (%v), want the aborted one", asked, err)
		}
	})
}
