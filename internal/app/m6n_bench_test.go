package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/criticbench"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestWireCriticBench_SourcesFollowTheEarsTrack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.CriticBench == nil || s.Ears == nil || s.Deps.Diction == nil {
		t.Fatalf("precondition: bench %v ears %v diction %v", s.Deps.CriticBench != nil, s.Ears != nil, s.Deps.Diction != nil)
	}
	full := &BuildEnv{Store: st, Now: time.Now, Services: s}
	if n := len(benchSources(full)); n != 2 {
		t.Errorf("a build with the Ears track adds the Tier B and label sources, got %d", n)
	}
	bare := &BuildEnv{Store: st, Now: time.Now, Services: &Services{}}
	if n := len(benchSources(bare)); n != 0 {
		t.Errorf("without Ears and Diction nothing is added, got %d", n)
	}
	// the wired bench answers and the planner's trust adapter reads it
	if b, err := s.Deps.CriticBench.Bench(context.Background(), criticbench.Query{IncludeUnknown: true}); err != nil || len(b.Critics) != 0 {
		t.Fatalf("empty library: %v %v", b, err)
	}
	got, err := BenchTrust{Bench: s.Deps.CriticBench}.TrustedMetrics(context.Background(), []string{"x"})
	if err != nil || len(got) != 0 {
		t.Errorf("no trusted critic on an empty library: %v %v", got, err)
	}
}

func TestWireCriticBench_EarsWithoutDictionFailsLoudly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Ears == nil {
		t.Fatal("precondition: a full build wires the Ears track")
	}

	// Ears set, Diction nil: the hook order broke, so the label source would be dropped silently
	svcs := &Services{Ears: s.Ears}
	err = wireCriticBench(&BuildEnv{Store: st, Now: time.Now, Services: svcs})
	if err == nil || !strings.Contains(err.Error(), "before the Ears hook set Deps.Diction") {
		t.Fatalf("Ears without Diction must fail loudly, got %v", err)
	}
	if svcs.Deps.CriticBench != nil {
		t.Error("a failed wiring must not leave a bench behind")
	}

	// no Ears track: the bench still wires, with no Tier B or label source
	bare := &Services{}
	if err := wireCriticBench(&BuildEnv{Store: st, Now: time.Now, Services: bare}); err != nil {
		t.Fatalf("a build without Ears wires the bench: %v", err)
	}
	if bare.Deps.CriticBench == nil {
		t.Error("bench not wired without Ears")
	}
}
