package app_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type nopKind struct{}

func (nopKind) IngestKind(context.Context, *sql.Tx, jobs.KindIngest) (*string, string, error) {
	return nil, "", nil
}
func (nopKind) AfterKindCommit(context.Context, jobs.KindIngest) {}

type nopTake struct{}

func (nopTake) AfterTakeTx(context.Context, *sql.Tx, jobs.TakeInfo) error { return nil }
func (nopTake) AfterTakeCommit(context.Context, jobs.TakeInfo)            {}

// TestBuildEnv_KindIngesterRegistration: a track registers the ingester of analyze and plan once from its build hook; generate is
// never registrable, a duplicate or a nil ingester is a wiring error that aborts Build, and a take hook may be added.
func TestBuildEnv_KindIngesterRegistration(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		hook func(*app.BuildEnv) error
		want string // "" = Build succeeds
	}{
		// analyze belongs to the Ears track and plan to the Desk track (internal/app/ears.go and desk.go register them in every Build)
		{"take hooks", func(e *app.BuildEnv) error {
			e.AddTakeHook(nopTake{})
			e.AddTakeHook(nil)
			return nil
		}, ""},
		{"plan is registered by the Desk track", func(e *app.BuildEnv) error { return e.AddKindIngester(jobs.KindPlan, nopKind{}) }, "already has an ingester"},
		{"analyze is registered by the Ears track", func(e *app.BuildEnv) error { return e.AddKindIngester(jobs.KindAnalyze, nopKind{}) }, "already has an ingester"},
		{"generate is not registrable", func(e *app.BuildEnv) error { return e.AddKindIngester(jobs.KindGenerate, nopKind{}) }, "no ingester can be registered"},
		{"unknown kind", func(e *app.BuildEnv) error { return e.AddKindIngester("render", nopKind{}) }, "no ingester can be registered"},
		{"nil ingester", func(e *app.BuildEnv) error { return e.AddKindIngester(jobs.KindPlan, nil) }, "nil ingester"},
		{"duplicate", func(e *app.BuildEnv) error {
			return errors.Join(e.AddKindIngester(jobs.KindAnalyze, nopKind{}), e.AddKindIngester(jobs.KindPlan, nopKind{}))
		}, "already has an ingester"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.want == "" {
				build(t, app.Options{Hooks: []app.BuildHook{c.hook}})
				return
			}
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
			_, err = app.Build(context.Background(), cfg, config.Secrets{Key: key}, st, root, app.Options{Hooks: []app.BuildHook{c.hook}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Build error = %v, want %q", err, c.want)
			}
		})
	}
}
