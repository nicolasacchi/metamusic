package trackctx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/attention"
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

// envM5 is the M5 fixture (a three-generation lineage with comments on the grandparent) with the close-the-loop reader wired.
type envM5 struct {
	*env
	info lt.M5Info
	att  *attention.Service
}

func newEnvM5(t *testing.T, wired bool) *envM5 {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return lt.FixedNow }
	info := lt.FixtureM5(t, st, lt.FixedNow)
	lib := library.New(st, nil, now)
	n := 0
	j := judgments.New(st, lib, now, func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd800+n) })
	b := trackctx.New(lib, j, listening.New(st, now), serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), now), "https://mm.example.com/", now)
	b.UseM2b(ears.New(ears.Options{Store: st, Library: lib, Now: now}), nil)
	att := attention.New(attention.Options{Store: st})
	if wired {
		b.UseCloseLoop(att)
	}
	return &envM5{env: &env{st: st, b: b, lib: lib, now: now}, info: info, att: att}
}

func (e *envM5) build(t *testing.T, id string, include ...string) (trackctx.Bundle, []byte) {
	t.Helper()
	b, err := e.b.Build(context.Background(), generator, id, include)
	if err != nil {
		t.Fatal(err)
	}
	return b, marshal(t, b)
}

func golden(t *testing.T, name string, raw []byte) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), pretty.String()); diff != "" {
		t.Errorf("%s (-want +got):\n%s", name, diff)
	}
}

type commentView struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Relation         string         `json:"relation"`
	Spot             map[string]any `json:"spot"`
	LyricLines       []string       `json:"lyric_lines"`
	InFlightPatchIDs []string       `json:"in_flight_patch_ids"`
}

func commentsOf(t *testing.T, raw []byte) (cs []commentView, j map[string]any) {
	t.Helper()
	var doc struct {
		Judgments map[string]any `json:"judgments"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	cb, _ := json.Marshal(doc.Judgments["comments"])
	if err := json.Unmarshal(cb, &cs); err != nil {
		t.Fatal(err)
	}
	return cs, doc.Judgments
}

// TestContext_Goldens_PreM5Unchanged: the close-the-loop block adds no byte to a take that has no ancestors, no in-flight patch
// and no hotspot, and none at all without a reader, for every take the pre-M5 goldens cover.
func TestContext_Goldens_PreM5Unchanged(t *testing.T) {
	t.Parallel()
	plain, wired := newEnvM5(t, false), newEnvM5(t, true)
	// G0 is the root of the lineage (comments, listening, no ancestors); the take with a Song and the lyrics-only imports too
	for _, id := range []string{plain.info.Lineage[0], plain.info.Takes["T1"], plain.info.Takes["T5"], lt.TrackLuna} {
		_, a := plain.build(t, id)
		_, b := wired.build(t, id)
		if !bytes.Equal(a, b) {
			t.Errorf("%s: the reader changed the bundle of a take with nothing to report:\n%s\n%s", id, a, b)
		}
	}
	// and the existing golden of the base fixtures is untouched by the wiring: TestContext_Golden runs without a reader
	_, raw := wired.build(t, wired.info.Lineage[0])
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var lis, jud map[string]json.RawMessage
	_ = json.Unmarshal(doc["listening"], &lis)
	_ = json.Unmarshal(doc["judgments"], &jud)
	if _, ok := lis["hotspots"]; ok {
		t.Error("hotspots appear without a hotspot")
	}
	if _, ok := jud["ancestor_open_fix"]; ok {
		t.Error("ancestor_open_fix appears on a take without ancestors")
	}
}

// TestContext_AncestorComments_M5: the grandchild shows the open fix and note comments of the grandparent, mapped onto itself,
// newest first, and counts the open fixes.
func TestContext_AncestorComments_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	c := e.info.Comments
	_, raw := e.build(t, e.info.Lineage[1])
	cs, j := commentsOf(t, raw)
	if j["ancestor_open_fix"] != float64(2) {
		t.Errorf("ancestor_open_fix = %v, want 2 (hidden and resolved fixes do not count)", j["ancestor_open_fix"])
	}
	got := map[string]commentView{}
	for _, c := range cs {
		got[c.ID] = c
	}
	if len(cs) != 3 { // two fixes and the note; the keep, the hidden and the resolved comment are not shown
		t.Fatalf("comments = %+v", cs)
	}
	chorus, verse := got[c["fix_chorus"]], got[c["fix_verse"]]
	if chorus.Relation != "ancestor" || chorus.Spot == nil || chorus.Spot["basis"] != "section" || chorus.Spot["label"] != "chorus 1" {
		t.Errorf("chorus fix = %+v", chorus)
	}
	// G0 plan chorus 1 is 30-50 s, a comment at 40 s is halfway; G1's manual chorus 1 is 36-58 s: halfway is 47 s
	if at := chorus.Spot["at_s"].(float64); at < 46.99 || at > 47.01 {
		t.Errorf("chorus spot at_s = %v, want 47 (G1 manual sections)", at)
	}
	if conf := chorus.Spot["confidence"].(float64); conf != 0.8 { // the lower of manual (1) and plan (0.8)
		t.Errorf("confidence = %v", conf)
	}
	if want := []string{"fixture hook b1", "fixture hook b2", "fixture hook b3", "fixture hook b4"}; !cmp.Equal(chorus.LyricLines, want) {
		t.Errorf("chorus lyric lines = %v", chorus.LyricLines)
	}
	if verse.Spot == nil || verse.Spot["label"] != "verse 1" || len(verse.LyricLines) != 4 || verse.LyricLines[0] != "fixture line a1" {
		t.Errorf("verse fix = %+v", verse)
	}
	if n := got[c["note"]]; n.Relation != "ancestor" || len(n.LyricLines) != 1 || n.LyricLines[0] != "fixture tail d1" {
		t.Errorf("note = %+v", n)
	}
	if _, ok := got[c["keep"]]; ok {
		t.Error("a keep comment of the ancestor is listed")
	}
}

// TestContext_Spot_Proportional_M5: G2 has lyrics only, so the ancestors' spots map proportionally (confidence 0.3); the
// stored section label still finds the lyric lines.
func TestContext_Spot_Proportional_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	c := e.info.Comments
	_, raw := e.build(t, e.info.Lineage[2])
	cs, j := commentsOf(t, raw)
	if j["ancestor_open_fix"] != float64(2) {
		t.Errorf("ancestor_open_fix = %v", j["ancestor_open_fix"])
	}
	for _, cv := range cs {
		if cv.ID != c["fix_chorus"] {
			continue
		}
		if cv.Spot["basis"] != "proportional" || cv.Spot["confidence"] != 0.3 || cv.Spot["label"] != nil {
			t.Errorf("spot = %v", cv.Spot)
		}
		if at := cv.Spot["at_s"].(float64); at < 39.99 || at > 40.01 { // same fraction of the same 120 s
			t.Errorf("at_s = %v", at)
		}
		if len(cv.LyricLines) != 4 || cv.LyricLines[0] != "fixture hook b1" { // from the label the comment stored
			t.Errorf("lyric lines = %v", cv.LyricLines)
		}
		return
	}
	t.Fatal("the chorus fix is missing")
}

// TestContext_OwnComments_M5: once the bundle carries the loop fields, a take's own comments are marked self with their spot.
func TestContext_OwnComments_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	// a comment on G1 itself, at 12 s: G1's manual verse 1 is 4-36 s
	ctx := context.Background()
	at := 12.0
	if _, err := judgments.New(e.st, e.lib, e.now, func() string { return "01900000-0000-7000-8000-00000000f001" }).AddComment(ctx,
		auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}, "web", e.info.Lineage[1],
		judgments.NewComment{Body: "own remark", AtSecond: &at, Kind: "fix"}); err != nil {
		t.Fatal(err)
	}
	_, raw := e.build(t, e.info.Lineage[1])
	cs, j := commentsOf(t, raw)
	if j["ancestor_open_fix"] != float64(2) {
		t.Errorf("ancestor_open_fix = %v", j["ancestor_open_fix"])
	}
	var own *commentView
	for i := range cs {
		if cs[i].Relation == "self" {
			own = &cs[i]
		}
	}
	if own == nil || own.Spot["at_s"] != 12.0 || own.Spot["label"] != "verse 1" || len(own.LyricLines) != 4 {
		t.Fatalf("own comment = %+v", own)
	}
	if cs[0].Relation != "self" { // own comments come first, ancestors fill the rest of the limit
		t.Errorf("order: %+v", cs)
	}
}

// TestContext_InFlightPatch_M5: a comment addressed by a patch with a running render lists the patch id; the take with the
// comment turns the loop fields on even without ancestors.
func TestContext_InFlightPatch_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	c := e.info.Comments
	g0 := e.info.Lineage[0]
	var batch, recipe string
	if err := e.st.R.QueryRow(`SELECT b.id, j.recipe_id FROM jobs j JOIN batches b ON b.id = j.batch_id ORDER BY j.id LIMIT 1`).Scan(&batch, &recipe); err != nil {
		t.Fatal(err)
	}
	pid := "01900000-0000-7000-8000-00000000f101"
	e.exec(t, `INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,'h','style_edit','[]','{}','{}','{}','r','agent','tok','claude','2026-01-01T00:00:00Z')`, pid, g0)
	e.exec(t, `INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, pid, c["fix_chorus"])
	e.exec(t, `INSERT INTO jobs(id, batch_id, take_index, kind, engine, backend, recipe_id, operator, mutation_json, title, expected_audio_s, expected_s, timeout_s,
		expected_micro_usd, reserved_micro_usd, state, next_attempt_at, created_at, updated_at, patch_id)
		VALUES ('01900000-0000-7000-8000-00000000f102',?,77,'generate','yue2','fake',?,'style_edit','{}','T',100,50,120,10,20,'queued','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',?)`, batch, recipe, pid)
	_, raw := e.build(t, g0)
	cs, j := commentsOf(t, raw)
	if _, ok := j["ancestor_open_fix"]; !ok {
		t.Error("ancestor_open_fix missing once the loop fields are on")
	}
	seen := 0
	for _, cv := range cs {
		switch {
		case cv.ID == c["fix_chorus"]:
			seen++
			if !cmp.Equal(cv.InFlightPatchIDs, []string{pid}) || cv.Relation != "self" || cv.Spot["label"] != "chorus 1" {
				t.Errorf("addressed comment = %+v", cv)
			}
		case len(cv.InFlightPatchIDs) > 0:
			t.Errorf("comment %s lists a patch", cv.ID)
		}
	}
	if seen != 1 {
		t.Fatalf("comments = %+v", cs)
	}
}

// TestContext_Hotspots_M5: listening.hotspots appears when a take has replay hotspots.
func TestContext_Hotspots_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	g0 := e.info.Lineage[0]
	for i := range 3 {
		e.exec(t, `INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, to_ms, source, principal_kind, principal_id, client_at, received_at)
			VALUES (?,?,'replay-session','segment',0,20000,'web','human','p','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
			fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd900+i), g0)
	}
	_, raw := e.build(t, g0)
	var doc struct {
		Listening struct {
			Plays    int `json:"plays"`
			Hotspots []struct {
				FromS float64 `json:"from_s"`
				ToS   float64 `json:"to_s"`
				Score int     `json:"score"`
				Label string  `json:"label"`
			} `json:"hotspots"`
		} `json:"listening"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	h := doc.Listening.Hotspots
	if len(h) != 1 || h[0].FromS != 0 || h[0].ToS != 20 || h[0].Score != 3 || h[0].Label != "verse 1" {
		t.Errorf("hotspots = %+v", h)
	}
	if keys := keysOf(t, raw, "listening"); !cmp.Equal(keys, []string{"plays", "completion_rate", "last_played_at", "hotspots"}) {
		t.Errorf("listening keys = %v", keys)
	}
}

func keysOf(t *testing.T, raw []byte, sec string) []string {
	t.Helper()
	return keys(t, section(t, raw, sec))
}

// TestContext_Goldens_M5: the grandchild (lyrics only) and the child (manual sections) bundles, with ancestors, spots and lyric lines.
func TestContext_Goldens_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	_, g1 := e.build(t, e.info.Lineage[1])
	golden(t, "context_m5_child.golden.json", g1)
	_, g2 := e.build(t, e.info.Lineage[2])
	golden(t, "context_m5_grandchild.golden.json", g2)
}

// TestContext_Trim_M5: under a tight limit the close-the-loop fields go first, in the documented order, before any comment.
func TestContext_Trim_M5(t *testing.T) {
	t.Parallel()
	e := newEnvM5(t, true)
	g := e.info.Lineage[1]
	full, _ := e.build(t, g)
	if len(full.Budget.Truncated) != 0 {
		t.Fatalf("default limit truncates: %v", full.Budget.Truncated)
	}
	chars := full.Budget.Chars
	var order []string
	for limit := chars - 1; limit > chars-1200; limit -= 40 {
		b, _ := e.b.Build(context.Background(), generator, g, nil)
		trackctx.Fit(&b, limit, nil)
		if len(b.Budget.Truncated) > len(order) {
			order = b.Budget.Truncated
		}
	}
	want := []string{"comments.lyric_lines", "comments.spot", "comments"}
	idx := -1
	for _, n := range order {
		for i, w := range want {
			if n == w && i < idx {
				t.Errorf("trim order %v, want %v first", order, want)
			}
			if n == w {
				idx = i
			}
		}
	}
	if len(order) == 0 || order[0] != "comments.lyric_lines" {
		t.Errorf("first trimmed part = %v, want comments.lyric_lines", order)
	}
	// a protected comment list is never trimmed by the loop fields: include=comments keeps every comment
	b, _ := e.b.Build(context.Background(), generator, g, []string{"comments"})
	if n := len(commentsOfBundle(t, b)); n != 3 {
		t.Errorf("include=comments lists %d comments", n)
	}
}

func commentsOfBundle(t *testing.T, b trackctx.Bundle) []commentView {
	t.Helper()
	cs, _ := commentsOf(t, marshal(t, b))
	return cs
}
