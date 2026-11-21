package runpod_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

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
