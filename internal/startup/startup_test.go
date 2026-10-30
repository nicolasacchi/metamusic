package startup

import (
	"testing"
	"time"
)

func TestHealth_FailClear(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	h := New(func() time.Time { return at })
	if !h.Ready() || len(h.Issues()) != 0 {
		t.Fatal("a new Health must be ready with a non-nil empty list")
	}
	h.Fail("library_settle", "SETTLE_FAILED")
	at = at.Add(time.Minute)
	h.Fail("library_settle", "SETTLE_FAILED")
	h.Fail("a_step", "X")
	got := h.Issues()
	if h.Ready() || len(got) != 2 || got[0].Step != "a_step" || got[1].Attempts != 2 || got[1].Since != "2026-10-30T12:00:00Z" {
		t.Fatalf("issues: %+v", got)
	}
	h.Clear("library_settle")
	h.Clear("a_step")
	if !h.Ready() {
		t.Fatal("cleared steps must leave the Health ready")
	}
}

func TestHealth_NilIsHealthy(t *testing.T) {
	t.Parallel()
	var h *Health
	h.Fail("x", "Y")
	h.Clear("x")
	if !h.Ready() || h.Issues() == nil {
		t.Fatal("nil Health is ready with an empty list")
	}
}
