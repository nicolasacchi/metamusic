package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// fbHooks is a scripted FallbackHooks.
type fbHooks struct {
	mu                    sync.Mutex
	drills                []string
	drillSecs             int
	drillErr              error
	via, total            int
	pending, pendingPolls int
	pendingAfter          int // polls before pending reaches 0 (-1 = never)
	leaks                 bool
}

func (h *fbHooks) Drill(_ context.Context, jobID string, seconds int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drills, h.drillSecs = append(h.drills, jobID), seconds
	return h.drillErr
}

func (h *fbHooks) Receipts(context.Context, string) (int, int, error) { return h.via, h.total, nil }

func (h *fbHooks) ObjectsPending(context.Context, string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingPolls++
	if h.pendingAfter >= 0 && h.pendingPolls > h.pendingAfter {
		return 0, nil
	}
	return h.pending, nil
}

func (h *fbHooks) Leaks(context.Context, string) (bool, error) { return h.leaks, nil }

// fbProvider is a provider status of a server with the bucket configured (or not) and the given capabilities open.
func fbProvider(configured bool, caps ...string) map[string]any {
	p := withWorkers(1)
	p["worker"] = map[string]any{"image_digest": nil, "caps": caps, "changed_at": nil, "via": "none"}
	p["upload_fallback"] = map[string]any{"configured": configured, "objects_pending": 0, "last_used_at": nil}
	return p
}

func fbRun(t *testing.T, s *restStub, h *fbHooks, mod func(*runpod.CanaryOptions)) *runpod.CanaryReport {
	t.Helper()
	o := canaryOpts(s, runpod.CaseFallback)
	if h != nil {
		o.Fallback = h
	}
	if mod != nil {
		mod(&o)
	}
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCanary_FallbackCase(t *testing.T) {
	t.Parallel()
	newStub := func(t *testing.T, configured bool, caps ...string) *restStub {
		s := newRestStub(t, v2Job()...)
		s.provider = fbProvider(configured, caps...)
		s.providerAfter = fbProvider(configured, caps...)
		s.providerAfter["workers"] = map[string]any{"running": 0, "idle": 0, "initializing": 0}
		return s
	}
	good := func() *fbHooks { return &fbHooks{via: 2, total: 5} }
	t.Run("pass", func(t *testing.T) {
		t.Parallel()
		s, h := newStub(t, true, "v2", "fallback"), good()
		h.pending, h.pendingAfter = 3, 2
		rep := fbRun(t, s, h, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"completed", "artifacts_verified", "drill_written", "upload_via_fallback", "objects_deleted", "no_secret_in_events"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		if len(h.drills) != 1 || h.drillSecs != 300 {
			t.Errorf("drills %v for %d s", h.drills, h.drillSecs)
		}
		if h.pendingPolls < 3 {
			t.Errorf("the deletion was not waited for (%d polls)", h.pendingPolls)
		}
		if rep.RequestID != "mm-canary-20261014-fallback-1" {
			t.Errorf("request id %q", rep.RequestID)
		}
	})
	for name, tc := range map[string]struct {
		hooks      *fbHooks
		configured bool
		caps       []string
		want       string
	}{
		"no hooks":       {nil, true, []string{"v2", "fallback"}, "app host"},
		"not configured": {good(), false, []string{"v2", "fallback"}, "not configured"},
		"cap closed":     {good(), true, []string{"v2"}, "contract open"},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			t.Parallel()
			s := newStub(t, tc.configured, tc.caps...)
			rep := fbRun(t, s, tc.hooks, nil)
			if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, tc.want) {
				t.Fatalf("rep %+v", rep)
			}
			if s.count("POST /api/generations/estimate") != 0 || s.count("POST /api/generations") != 0 {
				t.Error("the canary went past a refusal")
			}
		})
	}
	t.Run("a server without the block is refused", func(t *testing.T) {
		t.Parallel()
		s := newStub(t, true, "v2", "fallback")
		delete(s.provider, "upload_fallback")
		if rep := fbRun(t, s, good(), nil); rep.Exit != runpod.ExitRefused {
			t.Fatalf("rep %+v", rep)
		}
	})
	fails := map[string]struct {
		mod   func(*fbHooks)
		check string
	}{
		"no upload went through the fallback": {func(h *fbHooks) { h.via = 0 }, "upload_via_fallback"},
		"an object is never deleted":          {func(h *fbHooks) { h.pending, h.pendingAfter = 1, -1 }, "objects_deleted"},
		"a signature shows in a row":          {func(h *fbHooks) { h.leaks = true }, "no_secret_in_events"},
		"the drill row cannot be written":     {func(h *fbHooks) { h.drillErr = errors.New("boom") }, "drill_written"},
	}
	for name, tc := range fails {
		t.Run("fail: "+name, func(t *testing.T) {
			t.Parallel()
			h := good()
			tc.mod(h)
			rep := fbRun(t, newStub(t, true, "v2", "fallback"), h, nil)
			if c, _ := checkOf(rep, tc.check); rep.Exit != runpod.ExitFail || c.OK {
				t.Fatalf("rep %+v check %+v", rep, c)
			}
		})
	}
}

func TestParseCanaryCase_ListsFallback(t *testing.T) {
	t.Parallel()
	if c, err := runpod.ParseCanaryCase("fallback"); err != nil || c != runpod.CaseFallback {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := runpod.ParseCanaryCase("nope"); err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Errorf("the usage error does not list the case: %v", err)
	}
}

// ---- pod-selfterm (BULK-P7, C399.10 client side) ------------------------------------------------------------------

// podStub scripts the REST surface the pod-selfterm case reads. The batch awaits approval for approveAfter polls, is active until the
// pod is gone, then finished; the pod is listed running, then terminated; the safety verdict changes after the pod is gone.
type podStub struct {
	*httptest.Server
	mu           sync.Mutex
	calls        map[string]int
	requiresHum  bool
	approveAfter int // batch polls answered awaiting_confirmation (-1: never approved)
	podPolls     int
	podGoneAfter int // pod polls answered running before terminated (-1: never)
	podMissing   bool
	safetyBefore string
	safetyAfter  string
	digestOK     bool
	liveAfter    int // live pods still shown after the run
	keyPresent   bool
	probe        string
	armed        bool
	caps         []string
	settled      int64
	batchPolls   int
	cancelled    bool
	estimateBody string
	genCalls     int
}

func newPodStub(t *testing.T) *podStub {
	t.Helper()
	s := &podStub{calls: map[string]int{}, requiresHum: true, podGoneAfter: 1, safetyBefore: "unverified", safetyAfter: "verified", digestOK: true,
		keyPresent: true, probe: "ok", armed: true, caps: []string{"agent"}, settled: 50_000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *podStub) goneNow() bool { return s.podGoneAfter >= 0 && s.podPolls > s.podGoneAfter }

func (s *podStub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	s.calls[key]++
	w.Header().Set("Content-Type", "application/json")
	out := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch key {
	case "GET /api/provider":
		safety, live := s.safetyBefore, []any{}
		if s.goneNow() {
			safety = s.safetyAfter
			for i := 0; i < s.liveAfter; i++ {
				live = append(live, map[string]any{"name": "mm-x", "state": "ready"})
			}
		}
		pod := map[string]any{"backend": "runpod-pod", "engine": "yue2", "generation_enabled": true, "disabled_reason": nil, "armed": s.armed, "trip": nil,
			"cold_start_s": 1200, "kill_slack_s": 15, "max_runtime_s": 900, "worker": map[string]any{"caps": s.caps},
			"pods": map[string]any{"key_present": s.keyPresent, "probe": s.probe, "safety": safety, "safety_digest_matches": s.digestOK, "live": live, "alerts": []any{}}}
		serverless := map[string]any{"backend": "runpod", "engine": "yue2", "generation_enabled": true, "armed": false, "trip": nil}
		out(200, map[string]any{"backend": "runpod", "generation_enabled": true, "armed": false, "trip": nil, "backends": []any{serverless, pod}})
	case "POST /api/generations/estimate":
		s.estimateBody = string(body)
		out(200, map[string]any{"confirmation_token": "mmc_" + strings.Repeat("a", 52), "takes": 1, "requires_human_approval": s.requiresHum,
			"cost": map[string]any{"ceiling_micro_usd": 300_000}})
	case "POST /api/generations":
		s.genCalls++
		out(201, map[string]any{"id": "batch-1", "state": "awaiting_confirmation", "jobs": []any{map[string]any{"id": "job-1"}},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": 0, "reconciled": false}})
	case "GET /api/batches/batch-1":
		st := "active"
		if s.approveAfter < 0 || s.batchPolls < s.approveAfter {
			st = "awaiting_confirmation"
		} else if s.goneNow() {
			st = "finished"
		}
		s.batchPolls++
		out(200, map[string]any{"id": "batch-1", "state": st, "jobs": []any{},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": s.settled, "reconciled": st == "finished"}})
	case "POST /api/batches/batch-1/cancel":
		s.cancelled = true
		out(200, map[string]any{"id": "batch-1", "state": "finished", "jobs": []any{}, "cost": map[string]any{}})
	case "GET /api/pods":
		if r.URL.Query().Get("batch_id") != "batch-1" || r.URL.Query().Get("state") != "all" {
			out(422, map[string]any{"code": "VALIDATION_FAILED"})
			return
		}
		if s.podMissing {
			out(200, map[string]any{"items": []any{}})
			return
		}
		s.podPolls++
		st, life := "booting", any(nil)
		if s.goneNow() {
			st, life = "terminated", 150
		}
		out(200, map[string]any{"items": []any{map[string]any{"id": "p1", "name": "mm-p1", "kind": "canary", "batch_id": "batch-1", "state": st, "lifetime_s": life, "error_code": nil}}})
	case "GET /api/jobs/job-1":
		out(200, map[string]any{"id": "job-1", "state": "failed", "error_code": "NOT_SUBMITTED"})
	default:
		out(404, map[string]any{"code": "NOT_FOUND", "message": "no route " + key})
	}
}

func podRun(t *testing.T, s *podStub, mod func(*runpod.CanaryOptions)) *runpod.CanaryReport {
	t.Helper()
	o := canaryOpts(&restStub{Server: s.Server}, runpod.CasePodSelfterm)
	o.PodApprovalWait, o.PodWait = time.Minute, time.Minute
	if mod != nil {
		mod(&o)
	}
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestCanary_PodSelftermCase(t *testing.T) {
	t.Parallel()
	t.Run("pass: approved, the pod is gone, the verdict is verified", func(t *testing.T) {
		t.Parallel()
		s := newPodStub(t)
		s.approveAfter = 3
		rep := podRun(t, s, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"pod_created", "pod_terminated", "verdict_verified", "no_live_pods", "settled_le_reserved"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		for _, want := range []string{`"backend":"runpod-pod"`, `"pod_canary":"self_terminate"`} {
			if !strings.Contains(s.estimateBody, want) {
				t.Errorf("the estimate %s lacks %s", s.estimateBody, want)
			}
		}
		if s.genCalls != 1 || s.cancelled {
			t.Errorf("batches created %d, cancelled %v", s.genCalls, s.cancelled)
		}
		if rep.Measured["pod_lifetime_s"] != 150 && rep.Measured["pod_lifetime_s"] != float64(150) {
			t.Errorf("measured %v", rep.Measured)
		}
		if rep.RequestID != "mm-canary-20261014-pod-selfterm-1" {
			t.Errorf("request id %q", rep.RequestID)
		}
	})
	for name, tc := range map[string]struct {
		mod  func(*podStub)
		want string
	}{
		"estimate without approval": {func(s *podStub) { s.requiresHum = false }, "approval"},
		"disarmed":                  {func(s *podStub) { s.armed = false }, "disarmed"},
		"no pod key":                {func(s *podStub) { s.keyPresent = false }, "pod key"},
		"no probe":                  {func(s *podStub) { s.probe = "missing" }, "probe"},
		"agent capability closed":   {func(s *podStub) { s.caps = nil }, "capabilities agent"},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			t.Parallel()
			s := newPodStub(t)
			tc.mod(s)
			rep := podRun(t, s, nil)
			if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, tc.want) {
				t.Fatalf("rep %+v", rep)
			}
			if s.genCalls != 0 {
				t.Error("a batch was created past the refusal")
			}
		})
	}
	t.Run("never approved: aborted, nothing spent", func(t *testing.T) {
		t.Parallel()
		s := newPodStub(t)
		s.approveAfter = -1
		rep := podRun(t, s, func(o *runpod.CanaryOptions) { o.PodApprovalWait = 50 * time.Millisecond })
		if rep.Exit != runpod.ExitAborted || !strings.Contains(rep.Reason, "approv") {
			t.Fatalf("rep %+v", rep)
		}
		if !s.cancelled {
			t.Error("the unapproved batch was left standing")
		}
	})
	t.Run("the pod never goes away: failed and the batch is cancelled", func(t *testing.T) {
		t.Parallel()
		s := newPodStub(t)
		s.podGoneAfter = -1
		rep := podRun(t, s, func(o *runpod.CanaryOptions) { o.PodWait = 50 * time.Millisecond })
		if rep.Exit != runpod.ExitFail {
			t.Fatalf("rep %+v", rep)
		}
		if c, _ := checkOf(rep, "pod_terminated"); c.OK {
			t.Errorf("pod_terminated passed: %+v", c)
		}
		if !s.cancelled {
			t.Error("the batch was not cancelled")
		}
	})
	for name, tc := range map[string]struct {
		mod   func(*podStub)
		check string
	}{
		"verdict failed":       {func(s *podStub) { s.safetyAfter = "failed" }, "verdict_verified"},
		"verdict inconclusive": {func(s *podStub) { s.safetyAfter = "inconclusive" }, "verdict_verified"},
		"other digest":         {func(s *podStub) { s.digestOK = false }, "verdict_verified"},
		"no canary pod":        {func(s *podStub) { s.podMissing = true }, "pod_created"},
		"a pod is still live":  {func(s *podStub) { s.liveAfter = 1 }, "no_live_pods"},
	} {
		t.Run("fail: "+name, func(t *testing.T) {
			t.Parallel()
			s := newPodStub(t)
			tc.mod(s)
			rep := podRun(t, s, func(o *runpod.CanaryOptions) { o.PodWait = 50 * time.Millisecond })
			if c, _ := checkOf(rep, tc.check); rep.Exit != runpod.ExitFail || c.OK {
				t.Fatalf("rep %+v check %+v", rep, c)
			}
		})
	}
}

func TestParseCanaryCase_ListsPodSelfterm(t *testing.T) {
	t.Parallel()
	if c, err := runpod.ParseCanaryCase("pod-selfterm"); err != nil || c != runpod.CasePodSelfterm {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := runpod.ParseCanaryCase("nope"); err == nil || !strings.Contains(err.Error(), "pod-selfterm") {
		t.Errorf("the usage error does not list the case: %v", err)
	}
}
