package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/morning"
	"github.com/nicolasacchi/metamusic/internal/morning/morningtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestWireMorning: a full build wires the Morning Crate service into Deps, its resolver and its three loops, hands it the injected
// builder, and fails a building row a dead process left behind.
func TestWireMorning(t *testing.T) {
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
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	if _, err := st.W.Exec(`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at) VALUES('dead','manual','building',1,'{}','human:alice',?)`,
		store.FormatTime(now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	fake := &morningtest.Fake{}
	var seen []any
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{
		Now: func() time.Time { return now }, MorningBuilder: fake,
		Hooks: []BuildHook{func(env *BuildEnv) error {
			for _, r := range env.resolvers {
				seen = append(seen, r)
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.Morning == nil {
		t.Fatal("Deps.Morning not set")
	}
	found := false
	for _, r := range seen {
		if r == any(s.Deps.Morning) {
			found = true
		}
	}
	if !found {
		t.Error("the morning service is not among the media resolvers")
	}
	names := map[string]bool{}
	for _, l := range s.Loops {
		names[l.Name] = true
	}
	for _, want := range []string{"morning-crate", "morning-sweep", "morning-close"} {
		if !names[want] {
			t.Errorf("loop %s missing from %v", want, names)
		}
	}
	var state, code string
	if err := st.R.QueryRow(`SELECT state, COALESCE(error_code,'') FROM morning_crates WHERE id = 'dead'`).Scan(&state, &code); err != nil || state != "failed" || code != morning.CodeInterrupted {
		t.Errorf("dead build = %s %s %v", state, code, err)
	}
	set, err := s.Deps.Morning.Settings(context.Background())
	if err != nil || set.At != "07:00" {
		t.Errorf("settings %+v %v", set, err)
	}
	// without media tools and without an injected builder the service still wires (a build then fails TOOL_FAILED)
	s2, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{Media: &MediaOptions{ClipsMissing: context.Canceled, TierAMissing: context.Canceled}})
	if err != nil || s2.Deps.Morning == nil {
		t.Fatalf("no-builder build: %v", err)
	}
}
