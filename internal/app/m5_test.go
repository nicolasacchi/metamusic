package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/lineage"
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
