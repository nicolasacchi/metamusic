package trackctx_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

var generator = auth.Principal{Kind: auth.KindAgent, ID: "tok-g", Name: "gen", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}

const hintText = "try a new-seed variant: estimate_variant mode=new_seed n_seeds=3 (the estimate is free; spending needs create_variant)"

// newEnvM3 is newEnv over lt.FixtureM3 (a generated take with its job, environment and cost).
func newEnvM3(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return lt.FixedNow }
	lt.FixtureM3(t, st, lt.FixedNow)
	lib := library.New(st, nil, now)
	n := 0
	j := judgments.New(st, lib, now, func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd000+n) })
	b := trackctx.New(lib, j, listening.New(st, now), serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), now), "https://mm.example.com/", now)
	b.UseM2b(ears.New(ears.Options{Store: st, Library: lib, Now: now}), nil)
	return &env{st: st, b: b, lib: lib, now: now}
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func nextActions(t *testing.T, e *env, p auth.Principal, id string) []string {
	t.Helper()
	b, err := e.b.Build(context.Background(), p, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b.NextActions
}

func TestContext_ProvenanceCostEnv(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	b, err := e.b.Build(context.Background(), generator, lt.TrackJobChild, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	goldenBundle(t, "context_job.golden.json", raw)
	if diff := cmp.Diff([]string{"origin", "import_rule", "truncated", "cost", "env", "job_id"}, keys(t, section(t, raw, "provenance"))); diff != "" {
		t.Errorf("provenance keys:\n%s", diff)
	}
	pv := rawMap(t, raw)["provenance"].(map[string]any)
	cost := pv["cost"].(map[string]any)
	if pv["origin"] != "job" || pv["import_rule"] != "job" || pv["job_id"] != lt.JobDone || cost["micro_usd"] != float64(30216) || cost["usd"] != "0.030216" || cost["basis"] != "reconciled" {
		t.Errorf("provenance %v", pv)
	}
	if env := pv["env"].(map[string]any); env["env_id"] != lt.EnvFixture || env["gpu_class"] != "RTX 4090" {
		t.Errorf("env %v", env)
	}
	// the generation hint comes before the final critique entry
	na := b.NextActions
	if len(na) < 2 || na[len(na)-2] != hintText || !strings.HasPrefix(na[len(na)-1], "record your critique") {
		t.Errorf("next_actions %v", na)
	}
	if b.Budget.Chars > trackctx.DefaultLimit {
		t.Errorf("chars %d", b.Budget.Chars)
	}
	// an imported take: null cost, env and job
	imp, err := e.b.Build(context.Background(), generator, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv := rawMap(t, marshal(t, imp))["provenance"].(map[string]any); pv["cost"] != nil || pv["env"] != nil || pv["job_id"] != nil {
		t.Errorf("imported provenance %v", pv)
	}
}

func TestContext_GenerationHintConditions(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	if na := nextActions(t, e, generator, lt.TrackLuna); !contains(na, hintText) {
		t.Errorf("generate scope with a provider: %v", na)
	}
	if na := nextActions(t, e, agent, lt.TrackLuna); contains(na, hintText) {
		t.Errorf("a read-only agent got the hint: %v", na)
	}
	// the gate says generation is disabled: no hint
	e.b.UseGeneration(func(context.Context) bool { return false })
	if na := nextActions(t, e, generator, lt.TrackLuna); contains(na, hintText) {
		t.Errorf("generation disabled: %v", na)
	}
	e.b.UseGeneration(func(context.Context) bool { return true })
	if na := nextActions(t, e, generator, lt.TrackLuna); !contains(na, hintText) {
		t.Errorf("generation enabled: %v", na)
	}
}

func TestContext_NoGenerationHintForReference(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	for _, id := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		if na := nextActions(t, e, generator, id); contains(na, hintText) {
			t.Errorf("%s: a reference track cannot be a mother: %v", id, na)
		}
	}
}

func TestContext_NextActionsCapDropsGenerationHint(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	// TrackShort: untimed and truncated; add an open fix comment and leave it unrated: all four conditional entries
	e.exec(t, `INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES('c-fix', ?, 'fix the intro', 'fix', 'open', 'api', 'human', 'human:fixture', 'fixture', '2026-10-06T10:00:00Z', '2026-10-06T10:00:00Z')`, lt.TrackShort)
	na := nextActions(t, e, generator, lt.TrackShort)
	if len(na) != 5 || contains(na, hintText) {
		t.Fatalf("four conditional entries plus the critique fill the cap; the hint is dropped: %v", na)
	}
	if !strings.HasPrefix(na[4], "record your critique") {
		t.Errorf("the critique stays last: %v", na)
	}
	// rate the track: three conditional entries, the hint fits and sits before the critique
	e.exec(t, `INSERT INTO ratings(track_id, source, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at) VALUES(?, 'human', 4, NULL, 'human', 'human:fixture', 'fixture', '2026-10-06T11:00:00Z', '2026-10-06T11:00:00Z')`, lt.TrackShort)
	na = nextActions(t, e, generator, lt.TrackShort)
	if len(na) != 5 || na[3] != hintText || !strings.HasPrefix(na[4], "record your critique") {
		t.Errorf("with the hint: %v", na)
	}
	if got := nextActions(t, e, agent, lt.TrackShort); len(got) != 4 {
		t.Errorf("without generate scope: %v", got)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
