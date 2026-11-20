package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/jobs"
)

type logHook struct {
	name string
	log  *[]string
	err  error
}

func (h logHook) AfterTakeTx(context.Context, *sql.Tx, jobs.TakeInfo) error {
	*h.log = append(*h.log, h.name+":tx")
	return h.err
}
func (h logHook) AfterTakeCommit(context.Context, jobs.TakeInfo) {
	*h.log = append(*h.log, h.name+":commit")
}

// TestLateTakeHook_ListRunsCallbacksFirst: the late hook runs the M5 callbacks target first and then every hook a track added, in
// order; the first error stops the chain (the take rolls back); with nothing wired it does nothing.
func TestLateTakeHook_ListRunsCallbacksFirst(t *testing.T) {
	t.Parallel()
	var log []string
	var l lateTakeHook
	if err := l.AfterTakeTx(t.Context(), nil, jobs.TakeInfo{}); err != nil {
		t.Fatal(err)
	}
	l.AfterTakeCommit(t.Context(), jobs.TakeInfo{})
	l.target = logHook{name: "callbacks", log: &log}
	l.extra = []jobs.TakeHook{logHook{name: "ears", log: &log}, logHook{name: "desk", log: &log}}
	if err := l.AfterTakeTx(t.Context(), nil, jobs.TakeInfo{}); err != nil {
		t.Fatal(err)
	}
	l.AfterTakeCommit(t.Context(), jobs.TakeInfo{})
	want := []string{"callbacks:tx", "ears:tx", "desk:tx", "callbacks:commit", "ears:commit", "desk:commit"}
	if diff := cmp.Diff(want, log); diff != "" {
		t.Errorf("order: %s", diff)
	}
	log = nil
	boom := errors.New("boom")
	l.extra = []jobs.TakeHook{logHook{name: "ears", log: &log, err: boom}, logHook{name: "desk", log: &log}}
	if err := l.AfterTakeTx(t.Context(), nil, jobs.TakeInfo{}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if diff := cmp.Diff([]string{"callbacks:tx", "ears:tx"}, log); diff != "" {
		t.Errorf("the chain must stop at the first error: %s", diff)
	}
}

// TestM5Late_KindMapIsShared: the map the ingest bridge is built with is the one the build hooks fill.
func TestM5Late_KindMapIsShared(t *testing.T) {
	t.Parallel()
	var l m5Late
	m := l.kindIngesters()
	e := &BuildEnv{late: &l}
	if err := e.AddKindIngester(jobs.KindPlan, nilKind{}); err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || l.kindIngesters() == nil {
		t.Errorf("map %v", m)
	}
	if err := (&BuildEnv{}).AddKindIngester(jobs.KindPlan, nilKind{}); err == nil {
		t.Error("an environment with no ingest wiring must refuse")
	}
}

type nilKind struct{}

func (nilKind) IngestKind(context.Context, *sql.Tx, jobs.KindIngest) (*string, string, error) {
	return nil, "", nil
}
func (nilKind) AfterKindCommit(context.Context, jobs.KindIngest) {}
