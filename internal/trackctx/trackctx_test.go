package trackctx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
	"github.com/nicolasacchi/metamusic/internal/trackctx/trackctxtest"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead}}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "claude", Scopes: []auth.Scope{auth.ScopeRead}}
)

type env struct {
	st *store.Store
	b  *trackctx.Builder
}

func newEnv(t *testing.T, fixture bool) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return lt.FixedNow }
	if fixture {
		lt.Fixture(t, st, lt.FixedNow)
	}
	lib := library.New(st, nil, now)
	n := 0
	j := judgments.New(st, lib, now, func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc000+n) })
	b := trackctx.New(lib, j, listening.New(st, now), serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), now), "https://mm.example.com/", now)
	return &env{st: st, b: b}
}

func marshal(t *testing.T, b trackctx.Bundle) []byte {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// keys returns the keys of the JSON object at the top of raw, in document order.
func keys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v %v", tok, err)
	}
	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func section(t *testing.T, raw []byte, name string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m[name]
}

func TestContext_Golden(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	b, err := e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "context.golden.json")
	if *update {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), pretty.String()); diff != "" {
		t.Errorf("golden (-want +got):\n%s", diff)
	}

	// shape and key order of C35
	wantTop := []string{"format", "track", "recipe", "provenance", "score", "lineage", "judgments", "perception", "listening", "_links", "next_actions", "_budget"}
	if diff := cmp.Diff(wantTop, keys(t, raw)); diff != "" {
		t.Errorf("top-level keys (-want +got):\n%s", diff)
	}
	wantTrack := []string{"id", "title", "status", "stars", "critic_stars", "critic_verdict", "tags", "duration_s", "recorded_at", "url"}
	if diff := cmp.Diff(wantTrack, keys(t, section(t, raw, "track"))); diff != "" {
		t.Errorf("track keys (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"engine", "hash", "request"}, keys(t, section(t, raw, "recipe"))); diff != "" {
		t.Errorf("recipe keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"id", "style", "lyrics", "cot", "seed", "abc_present", "cfg_scale"}, keys(t, section(t, section(t, raw, "recipe"), "request"))); diff != "" {
		t.Errorf("request keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"path", "operator", "inferred", "rule", "rationale", "children", "siblings"}, keys(t, section(t, raw, "lineage"))); diff != "" {
		t.Errorf("lineage keys:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"chars", "limit", "truncated"}, keys(t, section(t, raw, "_budget"))); diff != "" {
		t.Errorf("budget keys:\n%s", diff)
	}

	// links: 15 minutes for an agent after the fixed clock, 12 h for the operator
	exp := func(p auth.Principal) string {
		t.Helper()
		bb, err := e.b.Build(context.Background(), p, lt.TrackLuna, nil)
		if err != nil {
			t.Fatal(err)
		}
		return bb.Links["stream"].(interface{ Get(string) any }).Get("expires_at").(string)
	}
	if got, w := exp(agent), lt.FixedNow.Add(15*time.Minute).Format(time.RFC3339); got != w {
		t.Errorf("agent link expires %s, want %s", got, w)
	}
	if got, w := exp(human), lt.FixedNow.Add(12*time.Hour).Format(time.RFC3339); got != w {
		t.Errorf("human link expires %s, want %s", got, w)
	}
	if b.Budget.Chars != utf16Len(string(raw)) || b.Budget.Chars != trackctx.Measure(&b) || b.Budget.Limit != trackctx.DefaultLimit {
		t.Errorf("budget %+v, measured %d", b.Budget, utf16Len(string(raw)))
	}
}

func TestContext_NextActions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	const (
		rate  = "ask the human to rate this take (rate_track)"
		untim = "section times need a score: refer to sections by lyrics, not by time"
		trunc = "the take is truncated (abc/semantic): prefer a shorter arrangement"
		last  = "record your critique as a comment (add_comment) — you cannot hear: see perception.blind_spots"
	)
	fix := func(n int) string {
		return fmt.Sprintf("address %d open fix comments (call_operation list_comments state=open kind=fix)", n)
	}
	// Short: unrated by the human, untimed, truncated; its only fix will be added below.
	tests := []struct {
		name  string
		track string
		want  []string
	}{
		{"rated, timed, one open fix", lt.TrackLuna, []string{fix(1), last}},
		{"rated, untimed, nothing else", lt.TrackCitta, []string{untim, last}},
		{"unrated (critic only), untimed", lt.TrackTwinA, []string{rate, untim, last}},
		{"unrated, untimed, truncated", lt.TrackShort, []string{rate, untim, trunc, last}},
	}
	for _, tc := range tests {
		b, err := e.b.Build(context.Background(), agent, tc.track, nil)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(tc.want, b.NextActions); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tc.name, diff)
		}
	}
	// all five rules at once, in order
	if _, err := e.st.W.Exec(`INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES ('01900000-0000-7000-8000-00000000ff01', ?, 'x', 'fix', 'open', 'api', 'human', 'human:fixture', 'fixture', ?, ?)`,
		lt.TrackShort, lt.FixedNow.Format(time.RFC3339), lt.FixedNow.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	b, err := e.b.Build(context.Background(), agent, lt.TrackShort, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{rate, fix(1), untim, trunc, last}, b.NextActions); diff != "" {
		t.Errorf("all five (-want +got):\n%s", diff)
	}
}

func TestContext_Include(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	ctx := context.Background()
	abc := "X:1\nT:t\nK:C\nCDEF|\n"
	e.b.UseScoreABC(func(_ context.Context, id string) (string, error) {
		if id != lt.TrackLuna {
			return "", library.ErrNotFound
		}
		return abc, nil
	})
	b, _ := e.b.Build(ctx, agent, lt.TrackLuna, nil)
	if _, ok := b.Score["abc"]; ok {
		t.Error("score.abc without include=score")
	}
	if b.Budget.Limit != trackctx.DefaultLimit {
		t.Errorf("limit %d", b.Budget.Limit)
	}
	b, err := e.b.Build(ctx, agent, lt.TrackLuna, []string{"score"})
	if err != nil || b.Score["abc"] != abc || b.Budget.Limit != trackctx.IncludeLimit {
		t.Errorf("include=score: %v %v %d", b.Score["abc"], err, b.Budget.Limit)
	}
	// a track without a score keeps score null even with include=score
	b, err = e.b.Build(ctx, agent, lt.TrackCitta, []string{"score"})
	if err != nil || b.Score != nil {
		t.Errorf("no score: %v %v", b.Score, err)
	}

	// comments: default lists at most 20; include=comments up to 500
	for i := range 40 {
		if _, err := e.st.W.Exec(`INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
			VALUES (?, ?, 'x', 'note', 'open', 'api', 'agent', 'agent:a', 'a', ?, ?)`, fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd000+i), lt.TrackCitta,
			lt.FixedNow.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339), lt.FixedNow.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	count := func(include ...string) (listed, total int) {
		b, err := e.b.Build(ctx, agent, lt.TrackCitta, include)
		if err != nil {
			t.Fatal(err)
		}
		return len(b.Judgments["comments"].([]any)), b.Judgments["comments_total"].(int)
	}
	if l, tot := count(); l != 20 || tot != 40 {
		t.Errorf("default: %d of %d", l, tot)
	}
	if l, tot := count("comments"); l != 40 || tot != 40 {
		t.Errorf("include=comments: %d of %d", l, tot)
	}
	// open fix comments first, then newest
	b, _ = e.b.Build(ctx, agent, lt.TrackLuna, nil)
	first := b.Judgments["comments"].([]any)[0].(interface{ Get(string) any })
	if first.Get("kind") != "fix" || first.Get("by") != "human" {
		t.Errorf("first comment %v", first)
	}

	// unknown include: a validation error
	_, err = e.b.Build(ctx, agent, lt.TrackLuna, []string{"foo"})
	var ve *library.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("include=foo: %v", err)
	}
	if _, err = e.b.Build(ctx, agent, "nope", nil); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

func TestContext_NeverInventsListening(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	b, err := e.b.Build(context.Background(), agent, lt.TrackCitta, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Listening map[string]any `json:"listening"`
	}
	if err := json.Unmarshal(marshal(t, b), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"plays": 0.0, "completion_rate": nil, "last_played_at": nil}, got.Listening); diff != "" {
		t.Errorf("listening (-want +got):\n%s", diff)
	}
	// and Luna's recorded events show up
	b, _ = e.b.Build(context.Background(), agent, lt.TrackLuna, nil)
	if b.Listening["plays"] != 2 || b.Listening["completion_rate"] == nil {
		t.Errorf("luna listening %v", b.Listening)
	}
}

func TestContext_StarsSplit(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	get := func(id string) (stars, critic any) {
		b, err := e.b.Build(context.Background(), agent, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Track map[string]any `json:"track"`
		}
		if err := json.Unmarshal(marshal(t, b), &got); err != nil {
			t.Fatal(err)
		}
		return got.Track["stars"], got.Track["critic_stars"]
	}
	if s, c := get(lt.TrackLuna); s != 5.0 || c != 2.0 {
		t.Errorf("luna stars %v critic %v", s, c)
	}
	if s, c := get(lt.TrackTwinA); s != nil || c != 4.0 {
		t.Errorf("twin A stars %v critic %v (the critic must not leak into stars)", s, c)
	}
}

func TestContext_Lineage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	b, err := e.b.Build(context.Background(), agent, lt.TrackTwinB, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	var l struct {
		Lineage struct {
			Path     []map[string]any `json:"path"`
			Operator string           `json:"operator"`
			Inferred bool             `json:"inferred"`
			Rule     string           `json:"rule"`
			Children []any            `json:"children"`
			Siblings int              `json:"siblings"`
		} `json:"lineage"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, p := range l.Lineage.Path {
		ids = append(ids, p["id"].(string))
	}
	if !slices.Equal(ids, []string{lt.TrackTwinA, lt.TrackTwinB}) || l.Lineage.Operator != "inferred_sibling" || !l.Lineage.Inferred ||
		l.Lineage.Rule != "same_request_id" || len(l.Lineage.Children) != 0 || l.Lineage.Siblings != 1 {
		t.Errorf("lineage %+v", l.Lineage)
	}
	b, _ = e.b.Build(context.Background(), agent, lt.TrackTwinA, nil)
	var p struct {
		Lineage struct {
			Children []map[string]any `json:"children"`
		} `json:"lineage"`
	}
	_ = json.Unmarshal(marshal(t, b), &p)
	if len(p.Lineage.Children) != 1 || p.Lineage.Children[0]["id"] != lt.TrackTwinB || p.Lineage.Children[0]["stars"] != nil {
		t.Errorf("children of twin A: %v", p.Lineage.Children)
	}
}

func TestFit_WorstCase(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	id := trackctxtest.WorstCase(t, e.st, lt.FixedNow)
	ctx := context.Background()

	b, err := e.b.Build(ctx, agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	if n := utf16Len(string(raw)); n > trackctx.DefaultLimit || n != trackctx.Measure(&b) || n != b.Budget.Chars {
		t.Errorf("default: %d units (Measure %d, chars %d), want <= %d", n, trackctx.Measure(&b), b.Budget.Chars, trackctx.DefaultLimit)
	}
	if diff := cmp.Diff([]string{"comments", "children", "lyrics"}, b.Budget.Truncated); diff != "" {
		t.Errorf("truncated (-want +got):\n%s", diff)
	}
	var got struct {
		Recipe    struct{ Request map[string]any } `json:"recipe"`
		Judgments map[string]any                   `json:"judgments"`
	}
	_ = json.Unmarshal(raw, &got)
	if got.Recipe.Request["lyrics"] != nil {
		t.Error("lyrics survived the default trimming")
	}
	if got.Judgments["comments_total"] != float64(trackctxtest.Comments) || len(got.Judgments["comments"].([]any)) != 0 {
		t.Errorf("comments_total kept, list emptied: %v", got.Judgments["comments_total"])
	}

	// include=lyrics: up to 96 000 and the lyrics intact
	b, err = e.b.Build(ctx, agent, id, []string{"lyrics"})
	if err != nil {
		t.Fatal(err)
	}
	raw = marshal(t, b)
	if n := utf16Len(string(raw)); n > trackctx.IncludeLimit || n != b.Budget.Chars {
		t.Errorf("include=lyrics: %d units, want <= %d (chars %d)", n, trackctx.IncludeLimit, b.Budget.Chars)
	}
	if slices.Contains(b.Budget.Truncated, "lyrics") {
		t.Errorf("lyrics trimmed under include=lyrics: %v", b.Budget.Truncated)
	}
	_ = json.Unmarshal(raw, &got)
	if l, _ := got.Recipe.Request["lyrics"].(string); len(l) != trackctxtest.LyricsChars {
		t.Errorf("lyrics length %d, want %d", len(l), trackctxtest.LyricsChars)
	}
}

func TestFit_BudgetFixedPoint(t *testing.T) {
	t.Parallel()
	mk := func(pad int) *trackctx.Bundle {
		return &trackctx.Bundle{
			Format: "mm.context/1", Track: map[string]any{"title": strings.Repeat("x", pad)}, Recipe: map[string]any{}, Provenance: map[string]any{},
			Lineage: map[string]any{}, Judgments: map[string]any{}, Perception: map[string]any{}, Listening: map[string]any{}, Links: map[string]any{},
			NextActions: []string{}, Budget: trackctx.Budget{Limit: 20000, Truncated: []string{}},
		}
	}
	// find the padding for which the bundle is 9 997 units with a one-digit chars: writing four digits gives
	// exactly 10 000, which needs five digits, which makes 10 001.
	pad := -1
	for p := 8000; p < 10500; p++ {
		b := mk(p)
		if trackctx.Measure(b) == 9997 {
			pad = p
			break
		}
	}
	if pad < 0 {
		t.Fatal("no padding found")
	}
	b := mk(pad)
	chars, trunc := trackctx.Fit(b, 20000, nil)
	if len(trunc) != 0 {
		t.Errorf("truncated %v", trunc)
	}
	raw := marshal(t, *b)
	if chars != 10001 || b.Budget.Chars != 10001 || utf16Len(string(raw)) != 10001 {
		t.Errorf("chars %d, budget %d, measured %d; want 10001", chars, b.Budget.Chars, utf16Len(string(raw)))
	}
	if b.Budget.Limit != 20000 {
		t.Errorf("limit %d", b.Budget.Limit)
	}
	// and the other side of the boundary: 9 998 with one digit is 9 999 with four
	for p := pad; p < pad+3; p++ {
		b := mk(p)
		chars, _ := trackctx.Fit(b, 20000, nil)
		if m := utf16Len(string(marshal(t, *b))); chars != m {
			t.Errorf("pad %d: chars %d != measured %d", p, chars, m)
		}
	}
}

func TestFit_ScoreABCCut(t *testing.T) {
	t.Parallel()
	b := &trackctx.Bundle{
		Format: "mm.context/1", Track: map[string]any{}, Recipe: map[string]any{}, Provenance: map[string]any{},
		Score: map[string]any{"bars": 4, "abc": strings.Repeat("CDEF|", 4000)}, Lineage: map[string]any{}, Judgments: map[string]any{},
		Perception: map[string]any{}, Listening: map[string]any{}, Links: map[string]any{}, NextActions: []string{}, Budget: trackctx.Budget{Truncated: []string{}},
	}
	chars, trunc := trackctx.Fit(b, 5000, []string{"score"})
	if chars > 5000 || b.Score["abc_truncated"] != true || !slices.Contains(trunc, "score.abc") {
		t.Errorf("chars %d, truncated %v, score %v", chars, trunc, len(b.Score["abc"].(string)))
	}
	if utf16Len(string(marshal(t, *b))) != chars {
		t.Error("chars is not the measured size")
	}
}

func TestFit_IncludeCommentsWorstCase(t *testing.T) {
	t.Parallel()
	comments := make([]any, 500)
	for i := range comments {
		comments[i] = map[string]any{"id": i, "body": strings.Repeat("x", 4000)}
	}
	b := &trackctx.Bundle{
		Format: "mm.context/1", Track: map[string]any{}, Recipe: map[string]any{}, Provenance: map[string]any{},
		Score: map[string]any{}, Lineage: map[string]any{},
		Judgments:  map[string]any{"comments": comments, "comments_total": 500},
		Perception: map[string]any{}, Listening: map[string]any{}, Links: map[string]any{}, NextActions: []string{}, Budget: trackctx.Budget{Truncated: []string{}},
	}
	chars, trunc := trackctx.Fit(b, trackctx.IncludeLimit, []string{"comments"})
	if chars > trackctx.IncludeLimit || !slices.Contains(trunc, "comments") {
		t.Errorf("chars %d, truncated %v", chars, trunc)
	}
	if utf16Len(string(marshal(t, *b))) != chars {
		t.Error("chars is not the measured size")
	}
	if b.Judgments["comments_total"] != 500 {
		t.Error("comments_total lost")
	}
}
