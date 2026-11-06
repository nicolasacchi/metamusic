package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeHook struct{ tx, commit int }

func (f *fakeHook) AfterTakeTx(context.Context, *sql.Tx, jobs.TakeInfo) error { f.tx++; return nil }
func (f *fakeHook) AfterTakeCommit(context.Context, jobs.TakeInfo)            { f.commit++ }

type fakeGate struct{ pending, limit int }

func (f fakeGate) Backlog(context.Context, lineage.Querier, string) (int, error) {
	return f.pending, nil
}
func (f fakeGate) Limit() int { return f.limit }

// TestM5Late_NilTargetsBehaveUnwired: a seam whose service is not wired changes nothing (the take hook does nothing, the backlog
// gate lets everything through) or reports errors.ErrUnsupported (the patch source), so Build before the M5 lanes is today's behaviour.
func TestM5Late_NilTargetsBehaveUnwired(t *testing.T) {
	t.Parallel()
	var l m5Late
	ctx := t.Context()
	if err := l.hook.AfterTakeTx(ctx, nil, jobs.TakeInfo{}); err != nil {
		t.Errorf("nil hook target: %v", err)
	}
	l.hook.AfterTakeCommit(ctx, jobs.TakeInfo{})
	if n, err := l.backlog.Backlog(ctx, nil, "root"); n != 0 || err != nil || l.backlog.Limit() != 0 {
		t.Errorf("nil gate target: %d %v limit %d, want the gate off", n, err, l.backlog.Limit())
	}
	if _, err := l.patches.PatchForEstimate(ctx, nil, "p"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("PatchForEstimate: %v", err)
	}
	if err := l.patches.RecheckComments(ctx, nil, "p"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("RecheckComments: %v", err)
	}
	if _, err := l.patches.RenderCount(ctx, nil, "p"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("RenderCount: %v", err)
	}
}

// TestM5Late_Delegates: once a lane points a seam at its service the calls reach it.
func TestM5Late_Delegates(t *testing.T) {
	t.Parallel()
	var l m5Late
	h := &fakeHook{}
	l.hook.target = h
	l.backlog.target = fakeGate{pending: 4, limit: 6}
	_ = l.hook.AfterTakeTx(t.Context(), nil, jobs.TakeInfo{})
	l.hook.AfterTakeCommit(t.Context(), jobs.TakeInfo{})
	if h.tx != 1 || h.commit != 1 {
		t.Errorf("hook calls %d/%d", h.tx, h.commit)
	}
	if n, _ := l.backlog.Backlog(t.Context(), nil, "r"); n != 4 || l.backlog.Limit() != 6 {
		t.Errorf("gate %d limit %d", n, l.backlog.Limit())
	}
	var _ jobs.TakeHook = &l.hook
	var _ generation.PatchSource = &l.patches
	var _ generation.BacklogGate = &l.backlog
}

// TestM5Late_SongPRNeverWithoutCallbacks: generation can create song_pr jobs only when the PatchSource is wired, and then the take hook
// (callbacks) and the backlog gate must be wired too: a Song PR child may never land without its callbacks, and an unconfigured callbacks
// service would make the take fail ingest (C1's hook errors with ErrUnsupported). The check Build runs refuses every other combination.
func TestM5Late_SongPRNeverWithoutCallbacks(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		patches, hook   bool
		backlog, wantOK bool
	}{
		{"nothing wired", false, false, false, true},
		{"callbacks without Song PR", false, true, true, true},
		{"Song PR alone", true, false, false, false},
		{"Song PR with the hook, no gate", true, true, false, false},
		{"Song PR with the gate, no hook", true, false, true, false},
		{"all three", true, true, true, true},
	} {
		var l m5Late
		if c.patches {
			l.patches.target = fakeSource{}
		}
		if c.hook {
			l.hook.target = &fakeHook{}
		}
		if c.backlog {
			l.backlog.target = fakeGate{limit: 6}
		}
		if err := l.check(); (err == nil) != c.wantOK {
			t.Errorf("%s: check() = %v, want ok=%v", c.name, err, c.wantOK)
		}
	}
}

type fakeSource struct{}

func (fakeSource) PatchForEstimate(context.Context, lineage.Querier, string) (generation.PatchRow, error) {
	return generation.PatchRow{}, nil
}
func (fakeSource) RecheckComments(context.Context, lineage.Querier, string) error { return nil }
func (fakeSource) RenderCount(context.Context, lineage.Querier, string) (int, error) {
	return 0, nil
}

// TestBuildM5_WiresEverySeam: the production wiring fills all three late seams from the one buildM5 call, so Build's check passes and
// a Song PR take reaches the callbacks service.
func TestBuildM5_WiresEverySeam(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	late := &m5Late{}
	s := buildM5(m5Inputs{st: st, now: time.Now, newID: func() string { return "id" }, notifier: notify.Nop{}, late: late})
	if s.SongPR == nil || s.Callbacks == nil || late.patches.target == nil || late.hook.target == nil || late.backlog.target == nil {
		t.Fatalf("seams: songpr %v callbacks %v patches %v hook %v backlog %v", s.SongPR != nil, s.Callbacks != nil,
			late.patches.target != nil, late.hook.target != nil, late.backlog.target != nil)
	}
	if err := late.check(); err != nil {
		t.Errorf("check: %v", err)
	}
	if late.hook.target != jobs.TakeHook(s.Callbacks) {
		t.Error("the take hook is not the callbacks service")
	}
}
