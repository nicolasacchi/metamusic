package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type stubPlans struct{ ids []string }

func (s *stubPlans) PlansForEstimate(_ context.Context, _ lineage.Querier, ids []string) ([]generation.PlanRow, error) {
	s.ids = ids
	return []generation.PlanRow{{ID: ids[0]}}, nil
}

// TestLatePlanSource: a seam with no target answers errors.ErrUnsupported (generation reports 503 UNAVAILABLE {component: desk});
// with a target it delegates.
func TestLatePlanSource(t *testing.T) {
	t.Parallel()
	var l latePlanSource
	if _, err := l.PlansForEstimate(t.Context(), nil, []string{"p"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unwired: %v", err)
	}
	st := &stubPlans{}
	l.target = st
	rows, err := l.PlansForEstimate(t.Context(), nil, []string{"p"})
	if err != nil || len(rows) != 1 || st.ids[0] != "p" {
		t.Fatalf("wired: %v %v", rows, err)
	}
}

// TestWireDesk_FillsPlanSeam: the Desk build hook points generation's late PlanSource at the Desk service, and nothing else
// (a Build without a data root leaves the seam unwired).
func TestWireDesk_FillsPlanSeam(t *testing.T) {
	t.Parallel()
	var seen *BuildEnv
	dir := t.TempDir()
	st, err := store.Open(t.Context(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(t.Context(), cfg, config.Secrets{Key: []byte("0123456789abcdef0123456789abcdef")}, st, root,
		Options{Hooks: []BuildHook{func(env *BuildEnv) error { seen = env; return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.Desk == nil || seen == nil || seen.late == nil || seen.late.plans.target == nil {
		t.Fatalf("plan seam not wired: desk %v env %v", s.Deps.Desk, seen)
	}
	if seen.late.plans.target != s.Deps.Desk {
		t.Error("the plan seam does not point at the Desk service")
	}
}
