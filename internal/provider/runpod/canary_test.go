package runpod_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

func TestCanary_ArgsAndRecipes(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	rows := []struct {
		c    runpod.CanaryCase
		bars int
		cot  string
		secs int
	}{
		{runpod.CaseMain, 8, "melody", 30},
		{runpod.CaseCotFull, 8, "full", 30},
		{runpod.CaseTerminate, 32, "melody", 64},
		{runpod.CaseTimeout, 64, "melody", 128},
		{runpod.CaseKillSwitch, 64, "melody", 128},
	}
	if len(runpod.CanaryCases) != len(rows) {
		t.Fatalf("cases = %v", runpod.CanaryCases)
	}
	for _, r := range rows {
		t.Run(string(r.c), func(t *testing.T) {
			t.Parallel()
			s, err := runpod.NewCanarySpec(r.c, day, 3)
			if err != nil {
				t.Fatal(err)
			}
			if want := "mm-canary-20261014-" + string(r.c) + "-3"; s.RequestID != want {
				t.Errorf("id = %q want %q", s.RequestID, want)
			}
			p, err := yue2.ParseRequest(s.Recipe, yue2.ParseOptions{})
			if err != nil {
				t.Fatalf("the recipe does not parse: %v", err)
			}
			rq := p.Request
			if rq.ID != s.RequestID || rq.Seed != 1 || rq.Cot != r.cot || rq.Style != "instrumental, ambient, 80 BPM, no vocals" ||
				rq.Lyrics != "[Intro]\n\n[Outro]\n" || rq.ABC == nil {
				t.Errorf("request %+v", rq)
			}
			sc, err := yue2.ParseScore([]byte(*rq.ABC))
			if err != nil || sc.Bars != r.bars || sc.SecondsPerBar != 2 {
				t.Errorf("score bars=%d spb=%v err=%v", sc.Bars, sc.SecondsPerBar, err)
			}
			if !strings.Contains(*rq.ABC, "V:1") || !strings.Contains(*rq.ABC, "V:2") {
				t.Error("the score is not two-voice")
			}
			if s.ExpectedAudioS != r.secs || s.Bars != r.bars {
				t.Errorf("expected audio %d s bars %d", s.ExpectedAudioS, s.Bars)
			}
		})
	}
	if _, err := runpod.NewCanarySpec("bogus", day, 1); err == nil {
		t.Error("an unknown case was accepted")
	}
	if _, err := runpod.ParseCanaryCase("bogus"); err == nil {
		t.Error("ParseCanaryCase accepted bogus")
	}
	for _, c := range runpod.CanaryCases {
		if got, err := runpod.ParseCanaryCase(string(c)); err != nil || got != c {
			t.Errorf("ParseCanaryCase(%q) = %q %v", c, got, err)
		}
	}
	// the ceiling check uses the estimate's ceiling against --max-usd
	lim := runpod.DefaultCanaryMaxUSD
	if lim != 250_000 {
		t.Errorf("default limit = %d", lim)
	}
	for _, c := range []struct {
		ceiling money.MicroUSD
		want    bool
	}{{0, false}, {1, true}, {250_000, true}, {250_001, false}} {
		if got := runpod.CeilingAllowed(c.ceiling, lim); got != c.want {
			t.Errorf("CeilingAllowed(%d) = %v", c.ceiling, got)
		}
	}
	if runpod.ExitPass != 0 || runpod.ExitFail != 1 || runpod.ExitUsage != 2 || runpod.ExitRefused != 3 || runpod.ExitAborted != 4 {
		t.Error("exit codes changed")
	}
}

// ---- a scripted REST server -----------------------------------------------------------------------------

type restStub struct {
	*httptest.Server
	mu        sync.Mutex
	provider  map[string]any
	ceiling   int64
	jobs      []map[string]any // successive wait answers; the last repeats
	waits     int
	calls     map[string]int
	idemKeys  []string
	bearers   []string
	estimate  []byte
	cancelled bool
	patches   []string
	// providerAfter replaces the provider answer after the job reached a terminal wait
	providerAfter map[string]any
	// providerAfterCancel replaces the provider answer once the job was cancelled
	providerAfterCancel map[string]any
	terminalSeen        bool
}

func baseProvider() map[string]any {
	return map[string]any{"generation_enabled": true, "disabled_reason": nil, "armed": true, "trip": nil,
		"cold_start_s": 180, "kill_slack_s": 15, "max_runtime_s": 900,
		"workers":  map[string]any{"running": 0, "idle": 0, "initializing": 0},
		"endpoint": map[string]any{"idle_timeout_s": 20},
		"price":    map[string]any{"per_hour_micro_usd": 1_100_000},
		"meter":    map[string]any{"billed_seconds_24h": 100}}
}

func job(state string, extra map[string]any) map[string]any {
	j := map[string]any{"id": "job-1", "state": state, "outcome": nil, "error_code": nil, "track_id": nil, "audio_s": nil, "timeout_s": 186,
		"artifacts": []any{}, "timings": map[string]any{"execution_ms": nil, "cold_start_s": nil}}
	for k, v := range extra {
		j[k] = v
	}
	return j
}

func newRestStub(t *testing.T, jobs ...map[string]any) *restStub {
	t.Helper()
	s := &restStub{provider: baseProvider(), ceiling: 200_000, jobs: jobs, calls: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *restStub) count(key string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls[key] }

func (s *restStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/jobs/") && strings.HasSuffix(r.URL.Path, "/wait"):
		key = "wait"
	case strings.HasPrefix(r.URL.Path, "/api/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel"):
		key = "cancel"
	case strings.HasPrefix(r.URL.Path, "/api/tracks/"):
		key = "patch_track"
	}
	s.calls[key]++
	s.bearers = append(s.bearers, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	out := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch key {
	case "GET /api/provider":
		p := s.provider
		if s.cancelled && s.providerAfterCancel != nil {
			p = s.providerAfterCancel
		}
		if s.terminalSeen && s.providerAfter != nil {
			p = s.providerAfter
		}
		out(200, p)
	case "POST /api/generations/estimate":
		s.estimate = body
		out(200, map[string]any{"confirmation_token": "mmc_" + strings.Repeat("a", 52), "takes": 1, "requires_human_approval": false,
			"cost": map[string]any{"ceiling_micro_usd": s.ceiling}})
	case "POST /api/generations":
		s.idemKeys = append(s.idemKeys, r.Header.Get("Idempotency-Key"))
		out(201, map[string]any{"id": "batch-1", "state": "active", "jobs": []any{map[string]any{"id": "job-1"}},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": 100_000, "reconciled": true}})
	case "wait":
		i := min(s.waits, len(s.jobs)-1)
		s.waits++
		j := s.jobs[i]
		if st, _ := j["state"].(string); st == "completed" || st == "failed" || st == "cancelled" {
			s.terminalSeen = true
		}
		out(200, map[string]any{"job": j, "changed": true, "timed_out": false})
	case "cancel":
		s.cancelled = true
		s.jobs = []map[string]any{job("cancelled", map[string]any{"error_code": "CANCELLED_BY_USER"})}
		s.waits = 0
		out(200, job("cancelling", nil))
	case "patch_track":
		s.patches = append(s.patches, r.URL.Path+" "+string(body))
		out(200, map[string]any{})
	case "GET /api/batches/batch-1":
		out(200, map[string]any{"id": "batch-1", "state": "finished", "jobs": []any{},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": 100_000, "reconciled": true}})
	default:
		out(404, map[string]any{"code": "NOT_FOUND", "message": "no route " + key})
	}
}

func canaryOpts(s *restStub, c runpod.CanaryCase) runpod.CanaryOptions {
	return runpod.CanaryOptions{BaseURL: s.URL, Token: canary("mm-token"), Case: c, Day: t0, N: 1,
		WaitS: 1, SweepEvery: time.Millisecond, AbortAfter: 150 * time.Millisecond, AfterCancelWait: time.Millisecond}
}

func goodMain() []map[string]any {
	return []map[string]any{
		job("queued", nil), job("starting", nil), job("generating", nil),
		job("completed", map[string]any{"outcome": "clean", "track_id": "trk-1", "audio_s": 28.5,
			"artifacts": []any{map[string]any{"name": "audio.flac", "verified": true}, map[string]any{"name": "mm-result.json", "verified": true}},
			"timings":   map[string]any{"execution_ms": 41000, "cold_start_s": 95}}),
	}
}

func TestCanary_RefusesOverCeilingAndDisarmed(t *testing.T) {
	t.Parallel()
	t.Run("ceiling over --max-usd", func(t *testing.T) {
		t.Parallel()
		s := newRestStub(t, goodMain()...)
		s.ceiling = 300_000
		rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
		if err != nil || rep.Exit != runpod.ExitRefused || rep.Outcome != "refused" {
			t.Fatalf("rep %+v err %v", rep, err)
		}
		if s.count("POST /api/generations") != 0 {
			t.Error("a batch was created above the ceiling")
		}
		o := canaryOpts(s, runpod.CaseMain)
		o.MaxUSD = 300_000
		if rep, err = runpod.RunCanary(context.Background(), o); err != nil || rep.Exit != runpod.ExitPass {
			t.Fatalf("with a higher limit: %+v %v", rep, err)
		}
	})
	t.Run("disarmed", func(t *testing.T) {
		t.Parallel()
		s := newRestStub(t, goodMain()...)
		s.provider["armed"] = false
		rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
		if err != nil || rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "disarmed") {
			t.Fatalf("rep %+v err %v", rep, err)
		}
		if s.count("POST /api/generations/estimate") != 0 || s.count("POST /api/generations") != 0 {
			t.Error("the canary went past the disarmed check")
		}
	})
	t.Run("tripped and disabled", func(t *testing.T) {
		t.Parallel()
		s := newRestStub(t, goodMain()...)
		s.provider["trip"] = map[string]any{"id": "x"}
		if rep, _ := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain)); rep == nil || rep.Exit != runpod.ExitRefused {
			t.Errorf("tripped: %+v", rep)
		}
		s.provider["trip"] = nil
		s.provider["generation_enabled"] = false
		s.provider["disabled_reason"] = "endpoint_inactive"
		rep, _ := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
		if rep == nil || rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "endpoint_inactive") {
			t.Errorf("disabled: %+v", rep)
		}
	})
	t.Run("timeout case needs MM_JOB_MAX_RUNTIME=30s", func(t *testing.T) {
		t.Parallel()
		s := newRestStub(t, goodMain()...)
		rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseTimeout))
		if err != nil || rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "30s") {
			t.Fatalf("rep %+v err %v", rep, err)
		}
		if s.count("POST /api/generations/estimate") != 0 {
			t.Error("estimated before the max_runtime check")
		}
	})
}

func TestCanary_MainPasses(t *testing.T) {
	t.Parallel()
	s := newRestStub(t, goodMain()...)
	s.providerAfter = baseProvider()
	s.provider["workers"] = map[string]any{"running": 1, "idle": 0, "initializing": 0}
	rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
	if err != nil || rep.Exit != runpod.ExitPass || rep.Outcome != "pass" {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if rep.BatchID != "batch-1" || rep.JobID != "job-1" || rep.FinalState != "completed" {
		t.Errorf("rep %+v", rep)
	}
	if len(s.idemKeys) != 1 || s.idemKeys[0] != "canary-mm-canary-20261014-main-1" {
		t.Errorf("idempotency keys %v", s.idemKeys)
	}
	var est struct {
		Items []struct {
			Mode   string          `json:"mode"`
			Recipe json.RawMessage `json:"recipe"`
		} `json:"items"`
	}
	if err := json.Unmarshal(s.estimate, &est); err != nil || len(est.Items) != 1 || est.Items[0].Mode != "compose_new" {
		t.Fatalf("estimate body %s", s.estimate)
	}
	if _, err := yue2.ParseRequest(est.Items[0].Recipe, yue2.ParseOptions{}); err != nil {
		t.Errorf("recipe: %v", err)
	}
	if len(s.patches) != 1 || !strings.Contains(s.patches[0], "/api/tracks/trk-1") || !strings.Contains(s.patches[0], "discarded") {
		t.Errorf("patches %v", s.patches)
	}
	for _, b := range s.bearers {
		if b != "Bearer "+canary("mm-token") {
			t.Errorf("authorization %q", b)
		}
	}
	out, _ := json.Marshal(rep)
	if strings.Contains(string(out), canary("mm-token")) || strings.Contains(string(out), "mmc_") {
		t.Errorf("the report leaks: %s", out)
	}
	if rep.Measured["max_workers_seen"] != 1 {
		t.Errorf("measured %v", rep.Measured)
	}
	// --keep leaves the take
	s2 := newRestStub(t, goodMain()...)
	o := canaryOpts(s2, runpod.CaseMain)
	o.Keep = true
	if rep, err = runpod.RunCanary(context.Background(), o); err != nil || rep.Exit != runpod.ExitPass || s2.count("patch_track") != 0 {
		t.Errorf("keep: %+v %v patches=%d", rep, err, s2.count("patch_track"))
	}
}

func TestCanary_FailsOnUnverifiedArtifactOrLongAudio(t *testing.T) {
	t.Parallel()
	for name, mod := range map[string]func(j map[string]any){
		"unverified": func(j map[string]any) {
			j["artifacts"] = []any{map[string]any{"name": "audio.flac", "verified": false}}
		},
		"too long": func(j map[string]any) { j["audio_s"] = 46.0 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			js := goodMain()
			mod(js[3])
			s := newRestStub(t, js...)
			rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
			if err != nil || rep.Exit != runpod.ExitFail {
				t.Fatalf("rep %+v err %v", rep, err)
			}
			if s.count("patch_track") != 1 {
				t.Error("the failing take was not discarded")
			}
		})
	}
}

func TestCanary_AbortsWhenNeverGenerating(t *testing.T) {
	t.Parallel()
	s := newRestStub(t, job("queued", nil))
	rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseMain))
	if err != nil || rep.Exit != runpod.ExitAborted || rep.Outcome != "aborted" {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if !s.cancelled {
		t.Error("the job was not cancelled")
	}
	if !strings.Contains(rep.Reason, "workers are 0") {
		t.Errorf("reason %q does not confirm zero workers", rep.Reason)
	}
}

func withWorkers(running int) map[string]any {
	p := baseProvider()
	p["workers"] = map[string]any{"running": running, "idle": 0, "initializing": 0}
	return p
}

func TestCanary_StartWindowDefaultIsTwelveMinutes(t *testing.T) {
	t.Parallel()
	if runpod.DefaultCanaryStartTimeout != 12*time.Minute {
		t.Errorf("default start timeout %s", runpod.DefaultCanaryStartTimeout)
	}
}

func TestCanary_StartWindowExpiryCancelsAndConfirmsWorkersZero(t *testing.T) {
	t.Parallel()
	s := newRestStub(t, job("queued", nil), job("starting", nil))
	s.provider = withWorkers(1)
	s.providerAfterCancel = withWorkers(0)
	o := canaryOpts(s, runpod.CaseMain)
	o.AbortAfter = 0
	o.StartTimeout = 150 * time.Millisecond
	o.ConfirmWindow = 2 * time.Second
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil || rep.Exit != runpod.ExitAborted {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if !s.cancelled {
		t.Error("the job was not cancelled")
	}
	if !strings.Contains(rep.Reason, "workers are 0") || strings.Contains(rep.Reason, "still up") {
		t.Errorf("reason %q does not confirm zero workers", rep.Reason)
	}
	if rep.Measured["workers_after_cancel"] != 0 {
		t.Errorf("measured %v", rep.Measured)
	}
}

func TestCanary_StartWindowExpiryWithWorkersStillUpPrintsTheManualCommand(t *testing.T) {
	t.Parallel()
	s := newRestStub(t, job("queued", nil), job("starting", nil))
	s.provider = withWorkers(1) // the worker keeps crash-looping after the cancel
	o := canaryOpts(s, runpod.CaseMain)
	o.AbortAfter = 0
	o.StartTimeout = 100 * time.Millisecond
	o.ConfirmWindow = 100 * time.Millisecond
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil || rep.Exit != runpod.ExitAborted {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if !strings.Contains(rep.Reason, "still up") || !strings.Contains(rep.Reason, "metamusic runpod provision --deactivate") {
		t.Errorf("reason %q", rep.Reason)
	}
}

func TestCanary_TerminateCancelsAtStarting(t *testing.T) {
	t.Parallel()
	s := newRestStub(t, job("queued", nil), job("starting", nil), job("generating", nil))
	rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseTerminate))
	if err != nil || rep.Exit != runpod.ExitPass || rep.FinalState != "cancelled" {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if s.count("cancel") != 1 {
		t.Errorf("cancel calls = %d", s.count("cancel"))
	}
	if _, ok := rep.Measured["workers_after_cancel_wait"]; !ok {
		t.Errorf("measured %v", rep.Measured)
	}
}

func TestCanary_TimeoutCase(t *testing.T) {
	t.Parallel()
	mk := func(exec int64, code string) []map[string]any {
		return []map[string]any{job("generating", nil), job("failed", map[string]any{"error_code": code,
			"timings": map[string]any{"execution_ms": exec, "cold_start_s": 90}})}
	}
	for _, c := range []struct {
		name string
		js   []map[string]any
		exit int
	}{
		{"killed by the provider", mk(33_000, "DEADLINE"), runpod.ExitPass},
		{"kill too slow", mk(60_000, "DEADLINE"), runpod.ExitFail},
		{"worker stopped itself", mk(20_000, "WORKER_ERROR"), runpod.ExitFail},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newRestStub(t, c.js...)
			s.provider["max_runtime_s"] = 30
			rep, err := runpod.RunCanary(context.Background(), canaryOpts(s, runpod.CaseTimeout))
			if err != nil || rep.Exit != c.exit {
				t.Fatalf("rep %+v err %v", rep, err)
			}
			if c.name == "worker stopped itself" && !strings.Contains(strings.Join(noteList(rep), "|"), "--timeout-case") {
				t.Errorf("checks %+v", rep.Checks)
			}
		})
	}
}

func noteList(r *runpod.CanaryReport) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Note)
	}
	return out
}

func TestCanary_UsageErrors(t *testing.T) {
	t.Parallel()
	s := newRestStub(t)
	if _, err := runpod.RunCanary(context.Background(), runpod.CanaryOptions{BaseURL: s.URL, Case: runpod.CaseMain}); err == nil {
		t.Error("no token accepted")
	}
	if _, err := runpod.RunCanary(context.Background(), runpod.CanaryOptions{BaseURL: "ftp://x", Token: "t", Case: runpod.CaseMain}); err == nil {
		t.Error("bad URL accepted")
	}
	if _, err := runpod.RunCanary(context.Background(), runpod.CanaryOptions{BaseURL: s.URL, Token: "t"}); err == nil {
		t.Error("no case accepted")
	}
}

func TestCanary_VerifyBilling(t *testing.T) {
	t.Parallel()
	s := newRestStub(t)
	rep, err := runpod.VerifyBilling(context.Background(), runpod.CanaryOptions{BaseURL: s.URL, Token: "t"}, "batch-1")
	if err != nil || rep.Exit != runpod.ExitPass || rep.Billing == nil || !rep.Billing.Reconciled || !rep.Billing.BilledLEReserved {
		t.Fatalf("rep %+v err %v", rep, err)
	}
	if rep.Billing.RatioMilli == nil || rep.Billing.RatioNoIdleMilli == nil {
		t.Errorf("billing %+v", rep.Billing)
	}
}
