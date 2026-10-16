package prompts_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/prompts"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	now      = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	readOnly = auth.Principal{Kind: auth.KindAgent, ID: "t", Name: "t", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	canGen   = auth.Principal{Kind: auth.KindAgent, ID: "t", Name: "t", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}}
)

const (
	trackA   = "01900000-0000-7000-8000-000000000101"
	trackRef = "01900000-0000-7000-8000-000000000106"
	ctxBody  = `{"track_id":"01900000-0000-7000-8000-000000000101","perception":{"timed":true,"blind_spots":["vocals"]}}`
)

// fake implements prompts.Sources over fixed synthetic data; one per test (no shared state).
type fake struct {
	mu             sync.Mutex
	ctxCalls       int
	sinceSeen      time.Time
	limitSeen      int
	sessionToServe listening.Session
}

func (f *fake) ContextJSON(_ context.Context, _ auth.Principal, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxCalls++
	return []byte(ctxBody), nil
}

func (f *fake) Session(_ context.Context, since time.Time, limit int) (listening.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinceSeen, f.limitSeen = since, limit
	s := f.sessionToServe
	s.Since, s.Until = since, now
	return s, nil
}

func (f *fake) Title(_ context.Context, id string) (string, error) {
	switch id {
	case trackA:
		return "Synthetic Song", nil
	case trackRef:
		return "Synthetic Reference", nil
	}
	return "", library.ErrNotFound
}

func (f *fake) RecipeKnown(_ context.Context, id string) (bool, error) { return id != trackRef, nil }

func newFake() *fake {
	two := 4
	return &fake{sessionToServe: listening.Session{
		Tracks: []listening.SessionTrack{{TrackID: trackA, Title: "Synthetic Song", Plays: 3, Completed: 1, Skipped: 1, SecondsHeard: 95,
			LastPlayedAt: now.Add(-time.Hour), Stars: &two, OpenComments: 2}},
		Totals: listening.SessionTotals{Tracks: 1, Plays: 3, SecondsHeard: 95},
	}}
}

func golden(t *testing.T, file string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", file)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", file, diff)
	}
}

type out struct {
	Spec     prompts.Spec
	Messages []prompts.Message
}

func TestPrompts_RenderGolden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		file string
		p    auth.Principal
		name string
		args map[string]string
	}{
		{"critique_track.golden.json", readOnly, "critique_track", map[string]string{"track_id": trackA}},
		{"write_variant.golden.json", readOnly, "write_variant", map[string]string{"track_id": trackA}},
		{"write_variant_goal_generate.golden.json", canGen, "write_variant", map[string]string{"track_id": trackA, "goal": "make the bridge calmer"}},
		{"session_review.golden.json", readOnly, "session_review", map[string]string{"since": "2026-10-06T08:00:00Z"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			t.Parallel()
			sp, msgs, err := prompts.Render(context.Background(), c.p, c.name, c.args, newFake(), now)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, c.file, out{sp, msgs})
		})
	}
}

// TestPrompts_WriteVariantEndings: the closing paragraph follows the caller's generate scope.
func TestPrompts_WriteVariantEndings(t *testing.T) {
	t.Parallel()
	args := map[string]string{"track_id": trackA}
	_, with, err := prompts.Render(context.Background(), canGen, "write_variant", args, newFake(), now)
	if err != nil {
		t.Fatal(err)
	}
	_, without, err := prompts.Render(context.Background(), readOnly, "write_variant", args, newFake(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with[0].Content.Text, "estimate_variant") || strings.Contains(with[0].Content.Text, "does not have") {
		t.Errorf("generate ending: %s", with[0].Content.Text)
	}
	if strings.Contains(without[0].Content.Text, "estimate_variant") || !strings.Contains(without[0].Content.Text, "generate scope, which this connection does not have") {
		t.Errorf("no-generate ending: %s", without[0].Content.Text)
	}
	if !strings.Contains(without[0].Content.Text, "improve the weakest section") {
		t.Errorf("default goal missing: %s", without[0].Content.Text)
	}
}

func TestPrompts_CritiqueEmbedsContext(t *testing.T) {
	t.Parallel()
	_, msgs, err := prompts.Render(context.Background(), readOnly, "critique_track", map[string]string{"track_id": trackA}, newFake(), now)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("%v %v", msgs, err)
	}
	r := msgs[1].Content
	if msgs[0].Role != "user" || msgs[0].Content.Type != "text" || r.Type != "resource" || r.Resource == nil ||
		r.Resource.URI != "metamusic://tracks/"+trackA+"/context" || r.Resource.MIMEType != "application/json" || r.Resource.Text != ctxBody {
		t.Errorf("resource message = %+v", msgs[1])
	}
	for _, want := range []string{"Synthetic Song", trackA, "perception.blind_spots", "kind=fix", "perception.timed"} {
		if !strings.Contains(msgs[0].Content.Text, want) {
			t.Errorf("critique text lacks %q", want)
		}
	}
}

func TestPrompts_SessionReview(t *testing.T) {
	t.Parallel()
	f := newFake()
	_, msgs, err := prompts.Render(context.Background(), readOnly, "session_review", nil, f, now)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("%v %v", msgs, err)
	}
	if want := now.Add(-12 * time.Hour); !f.sinceSeen.Equal(want) || f.limitSeen != 20 {
		t.Errorf("default since = %v limit %d, want %v and 20", f.sinceSeen, f.limitSeen, want)
	}
	txt := msgs[0].Content.Text
	start := strings.Index(txt, "```json\n")
	end := strings.Index(txt[start+8:], "\n```")
	if start < 0 || end < 0 {
		t.Fatalf("no fenced json block: %s", txt)
	}
	var got listening.Session
	if err := json.Unmarshal([]byte(txt[start+8:start+8+end]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Totals != f.sessionToServe.Totals || len(got.Tracks) != 1 || got.Tracks[0].SecondsHeard != 95 {
		t.Errorf("embedded session = %+v", got)
	}
	// an empty session still renders a JSON array
	f2 := &fake{}
	_, msgs, err = prompts.Render(context.Background(), readOnly, "session_review", nil, f2, now)
	if err != nil || !strings.Contains(msgs[0].Content.Text, `"tracks": []`) {
		t.Errorf("empty session: %v %v", msgs, err)
	}
	// an explicit since reaches the source
	if _, _, err := prompts.Render(context.Background(), readOnly, "session_review", map[string]string{"since": "2026-10-05T00:00:00Z"}, f, now); err != nil ||
		!f.sinceSeen.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("explicit since: %v %v", f.sinceSeen, err)
	}
}

func TestPrompts_UnknownTrack404(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"critique_track", "write_variant"} {
		_, _, err := prompts.Render(context.Background(), readOnly, name, map[string]string{"track_id": "nope"}, newFake(), now)
		if !errors.Is(err, library.ErrNotFound) {
			t.Errorf("%s: %v, want library.ErrNotFound", name, err)
		}
	}
}

func TestPrompts_WriteVariantRefusesReferenceTrack(t *testing.T) {
	t.Parallel()
	f := newFake()
	_, _, err := prompts.Render(context.Background(), canGen, "write_variant", map[string]string{"track_id": trackRef}, f, now)
	var pe *prompts.Error
	if !errors.As(err, &pe) || pe.Status != 422 || pe.Code != "RECIPE_UNKNOWN" || pe.Details["track_id"] != trackRef {
		t.Fatalf("error = %v", err)
	}
	if f.ctxCalls != 0 {
		t.Error("the context bundle was built before the refusal")
	}
	// critique of a reference track is fine
	if _, _, err := prompts.Render(context.Background(), canGen, "critique_track", map[string]string{"track_id": trackRef}, f, now); err != nil {
		t.Errorf("critique of a reference track: %v", err)
	}
}

func TestPrompts_RenderValidation(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		args  map[string]string
		field string
	}{
		{"critique_track", nil, "track_id"},
		{"write_variant", map[string]string{"track_id": " "}, "track_id"},
		{"write_variant", map[string]string{"track_id": trackA, "goal": strings.Repeat("g", 501)}, "goal"},
		{"session_review", map[string]string{"since": "yesterday"}, "since"},
		{"session_review", map[string]string{"since": now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)}, "since"},
		{"nope", nil, "name"},
	} {
		_, _, err := prompts.Render(context.Background(), readOnly, c.name, c.args, newFake(), now)
		var pe *prompts.Error
		if !errors.As(err, &pe) || pe.Status != 422 || pe.Code != "VALIDATION_FAILED" || pe.Details["field"] != c.field {
			t.Errorf("%s %v: %v, want 422 VALIDATION_FAILED on %s", c.name, c.args, err, c.field)
		}
	}
	// exactly 30 days back is accepted
	if _, _, err := prompts.Render(context.Background(), readOnly, "session_review", map[string]string{"since": now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}, newFake(), now); err != nil {
		t.Errorf("30 days: %v", err)
	}
}

// failing wraps fake and fails one named source call.
type failing struct {
	*fake
	fail string
}

var errBoom = errors.New("boom")

func (f failing) ContextJSON(ctx context.Context, p auth.Principal, id string) ([]byte, error) {
	if f.fail == "context" {
		return nil, errBoom
	}
	return f.fake.ContextJSON(ctx, p, id)
}

func (f failing) Session(ctx context.Context, s time.Time, n int) (listening.Session, error) {
	if f.fail == "session" {
		return listening.Session{}, errBoom
	}
	return f.fake.Session(ctx, s, n)
}

func (f failing) RecipeKnown(ctx context.Context, id string) (bool, error) {
	if f.fail == "recipe" {
		return false, errBoom
	}
	return f.fake.RecipeKnown(ctx, id)
}

// TestPrompts_SourceErrorsPropagate: a failing source surfaces its error (the api layer answers 500).
func TestPrompts_SourceErrorsPropagate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ fail, name string }{
		{"context", "critique_track"}, {"context", "write_variant"}, {"recipe", "write_variant"}, {"session", "session_review"},
	} {
		_, _, err := prompts.Render(context.Background(), canGen, c.name, map[string]string{"track_id": trackA}, failing{newFake(), c.fail}, now)
		if !errors.Is(err, errBoom) {
			t.Errorf("%s/%s: %v", c.fail, c.name, err)
		}
	}
}
