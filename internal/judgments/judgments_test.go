package judgments_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	st  *store.Store
	lib *library.Service
	svc *judgments.Service
	dir string
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, dir: t.TempDir(), now: lt.FixedNow.Add(time.Hour)}
	lt.Fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, e.dir)
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e.lib = library.New(st, root, func() time.Time { return e.now })
	var n atomic.Int64
	n.Store(0x5000)
	e.svc = judgments.New(st, e.lib, func() time.Time { return e.now }, func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1))
	})
	return e
}

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}}
	device = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "Phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent2 = auth.Principal{Kind: auth.KindAgent, ID: "tok-2", Name: "other", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

func ip(i int) *int       { return &i }
func sp(s string) *string { return &s }

func exec(t *testing.T, e *env, q string, a ...any) {
	t.Helper()
	if _, err := e.st.W.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// addTake inserts a synthetic take of the given duration (and parent) and returns its id.
func addTake(t *testing.T, e *env, n int, dur float64, parent string) string {
	t.Helper()
	ctx := context.Background()
	p, err := yue2.ParseRequest([]byte(fmt.Sprintf(`{"id":"jt-%d","style":"pop","lyrics":"[Verse]\nx\n[Chorus]\ny","cot":"full","seed":%d}`, n, 7000+n)), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rid, _, err := e.lib.InsertRecipe(ctx, tx, p, "import")
	if err != nil {
		t.Fatal(err)
	}
	bw := 16000
	id := lt.TrackIDForTest(n)
	nt := library.NewTake{
		ID: id, Title: fmt.Sprintf("Judged %03d", n), Slug: fmt.Sprintf("judged_%03d", n), RecipeID: rid, ParentTrackID: parent, RootTrackID: parent,
		DurationS: dur, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: int64(dur * 48000),
		MasterPath: fmt.Sprintf("audio/masters/judged_%03d.flac", n), MasterSHA256: lt.Sha(fmt.Sprintf("jm%d", n)), MasterBytes: 10,
		SourceSHA256: lt.Sha(fmt.Sprintf("js%d", n)), FramesSHA256: lt.Sha(fmt.Sprintf("jf%d", n)), AudioMD5: strings.Repeat("0", 32),
		ImportRule: "slug", PrincipalID: "cli", RecordedAt: lt.FixedNow.Add(-time.Duration(n) * time.Minute),
		Loudness: analysis.Loudness{IntegratedLUFS: -14}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
	}
	if parent != "" {
		nt.Generation = 1
		nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "same_request_id"}
	}
	if err := e.lib.InsertTake(ctx, tx, nt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func wantCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var je *judgments.Error
	if !errors.As(err, &je) || je.Code != code || je.Status != status {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

func wantInvalid(t *testing.T, err error, field string) {
	t.Helper()
	var ve *library.ValidationError
	if !errors.As(err, &ve) || ve.Field != field {
		t.Fatalf("err = %v, want a validation error on %q", err, field)
	}
}

func TestRate_SourceFromPrincipal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id := lt.TrackShort
	// the human rates as the operator
	r, err := e.svc.Rate(ctx, human, id, ip(4), sp("good"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Human == nil || *r.Human.Stars != 4 || *r.Human.Verdict != "good" || r.Human.By.Kind != "human" || r.Human.By.ID != "human:alice" || r.Critic != nil {
		t.Fatalf("human rating %+v", r)
	}
	// a device rates as the operator too, and replaces the human row
	r, err = e.svc.Rate(ctx, device, id, ip(5), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Human == nil || *r.Human.Stars != 5 || r.Human.Verdict != nil || r.Human.By.Kind != "device" || r.Human.By.ID != "device:dev-1" || r.Critic != nil {
		t.Fatalf("device rating %+v", r)
	}
	var kind string
	if err := e.st.R.QueryRow(`SELECT principal_kind FROM ratings WHERE track_id=? AND source='human'`, id).Scan(&kind); err != nil || kind != "device" {
		t.Fatalf("stored principal_kind %q %v", kind, err)
	}
	// an agent rates as the critic and never touches the human row
	r, err = e.svc.Rate(ctx, agent, id, ip(1), sp("meh"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Human == nil || *r.Human.Stars != 5 || r.Critic == nil || *r.Critic.Stars != 1 || r.Critic.By.ID != "agent:tok-1" {
		t.Fatalf("agent rating %+v", r)
	}
	// run it twice: the same call leaves one row per source
	if _, err := e.svc.Rate(ctx, agent, id, ip(1), sp("meh")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE track_id=?`, id).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d %v", n, err)
	}
	if _, err := e.svc.Rate(ctx, human, "nope", ip(3), nil); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	if _, err := e.svc.Rate(ctx, human, id, ip(6), nil); err == nil {
		t.Error("6 stars accepted")
	}
}

func TestRate_ClearAndVerdict(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Rate(ctx, human, lt.TrackLuna, nil, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := e.svc.Ratings(ctx, lt.TrackLuna)
	if r.Human == nil || r.Human.Stars != nil || r.Human.Verdict != nil || r.Critic == nil || *r.Critic.Stars != 2 {
		t.Fatalf("cleared rating %+v", r)
	}
	if got, err := e.svc.Rate(ctx, human, lt.TrackLuna, ip(2), sp(strings.Repeat("v", 280))); err != nil || len(*got.Human.Verdict) != 280 {
		t.Fatalf("280 chars: %v", err)
	}
	_, err := e.svc.Rate(ctx, human, lt.TrackLuna, ip(2), sp(strings.Repeat("v", 281)))
	wantInvalid(t, err, "verdict")
	if got, _ := e.svc.Rate(ctx, human, lt.TrackLuna, ip(2), sp("   ")); got.Human.Verdict != nil {
		t.Errorf("blank verdict kept: %q", *got.Human.Verdict)
	}
}

func TestHumanStars(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	got, err := e.svc.HumanStars(context.Background(), []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]int{lt.TrackLuna: 5, lt.TrackCitta: 3}, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	exec(t, e, `UPDATE ratings SET stars = NULL WHERE track_id = ? AND source = 'human'`, lt.TrackCitta)
	got, _ = e.svc.HumanStars(context.Background(), []string{lt.TrackCitta})
	if len(got) != 0 {
		t.Errorf("cleared rating reported: %v", got)
	}
	big := make([]string, 1200)
	for i := range big {
		big[i] = fmt.Sprintf("x%d", i)
	}
	big = append(big, lt.TrackLuna)
	if got, err := e.svc.HumanStars(context.Background(), big); err != nil || got[lt.TrackLuna] != 5 {
		t.Errorf("chunked lookup: %v %v", got, err)
	}
}

func TestUpdateTrack_TitleAndStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	var path, sha string
	if err := e.st.R.QueryRow(`SELECT master_path, master_sha256 FROM tracks WHERE id=?`, lt.TrackLuna).Scan(&path, &sha); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(e.dir, path)
	if err := os.Chmod(full, 0o444); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(full)

	if err := e.svc.UpdateTrack(ctx, agent, lt.TrackLuna, nil, sp("to_relisten")); err != nil {
		t.Fatal(err)
	}
	d, _ := e.lib.Track(ctx, lt.TrackLuna)
	if d.Status != "to_relisten" || d.StatusSetBy != "agent:tok-1" || d.Title != "Luna Park" {
		t.Fatalf("status change: %q by %q title %q", d.Status, d.StatusSetBy, d.Title)
	}
	if err := e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("  Moon Garden  "), nil); err != nil {
		t.Fatal(err)
	}
	d, _ = e.lib.Track(ctx, lt.TrackLuna)
	if d.Title != "Moon Garden" || d.Status != "to_relisten" || d.StatusSetBy != "agent:tok-1" || d.Slug != "luna_park" {
		t.Fatalf("title change: %+v", d)
	}
	page, err := e.lib.SearchTracks(ctx, library.SearchParams{Q: "moon"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != lt.TrackLuna {
		t.Fatalf("FTS finds the new title: %v %v", page.Items, err)
	}
	if old, _ := e.lib.SearchTracks(ctx, library.SearchParams{Q: "park"}); len(old.Items) != 0 {
		t.Error("FTS still finds the old title")
	}
	if err := e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("Both"), sp("final")); err != nil {
		t.Fatal(err)
	}
	d, _ = e.lib.Track(ctx, lt.TrackLuna)
	if d.Title != "Both" || d.Status != "final" || d.StatusSetBy != "human:alice" {
		t.Fatalf("both: %+v", d)
	}
	// the master is untouched
	after, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(full)
	if lt.Sha(string(b)) != sha || after.Mode() != before.Mode() || after.Mode().Perm() != 0o444 || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("master changed: mode %v -> %v, mtime %v -> %v", before.Mode(), after.Mode(), before.ModTime(), after.ModTime())
	}
	for name, c := range map[string]func() error{
		"bad status": func() error { return e.svc.UpdateTrack(ctx, human, lt.TrackLuna, nil, sp("deleted")) },
		"empty":      func() error { return e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("   "), nil) },
		"201 chars":  func() error { return e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp(strings.Repeat("a", 201)), nil) },
		"C0":         func() error { return e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("a\x07b"), nil) },
		"C1":         func() error { return e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("a\u0085b"), nil) },
	} {
		if err := c(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := e.svc.UpdateTrack(ctx, human, "nope", sp("x"), nil); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

func TestUpdateTrack_EmptyPatch422(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	wantInvalid(t, e.svc.UpdateTrack(context.Background(), human, lt.TrackLuna, nil, nil), "body")
}

func TestNormalizeTag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"  Ballad   Rock ", "ballad rock"},
		{"Ünï", "ünï"},
		{"é", "é"}, // NFD input composes
		{"R&B", "r&b"},
		{"drum'n'bass", "drum'n'bass"},
		{"3/4 time", "3/4 time"},
		{"lo-fi_beats.v2+", "lo-fi_beats.v2+"},
		{"日本語", "日本語"},
		{strings.Repeat("a", 40), strings.Repeat("a", 40)},
	} {
		got, err := judgments.NormalizeTag(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeTag(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 41), "-lead", "&amp", "a<b", "a\x00b", "emoji \U0001F3B5", "a;b", "tab\tin"[:3] + "#"} {
		_, err := judgments.NormalizeTag(bad)
		wantCode(t, err, 422, "INVALID_TAG")
	}
}

func tagMap(tt library.TrackTags) map[string][]string {
	m := map[string][]string{}
	for _, tg := range tt.Tags {
		m[tg.Name] = tg.Sources
	}
	return m
}

func TestSetTags_Sources(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id := lt.TrackCitta // no tags in the fixture
	// an agent adds: an agent row, never a human one
	tt, err := e.svc.SetTags(ctx, agent, id, []string{"Chill", "chill", "rainy day"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]string{"chill": {"agent"}, "rainy day": {"agent"}}, tagMap(tt)); diff != "" {
		t.Fatalf("agent add (-want +got):\n%s", diff)
	}
	// a human adds the same name: both sources carry it
	tt, _ = e.svc.SetTags(ctx, human, id, []string{"chill", "  Ballad   Rock "}, nil)
	if diff := cmp.Diff(map[string][]string{"ballad rock": {"human"}, "chill": {"human", "agent"}, "rainy day": {"agent"}}, tagMap(tt)); diff != "" {
		t.Fatalf("human add (-want +got):\n%s", diff)
	}
	// an agent cannot remove a human tag
	tt, _ = e.svc.SetTags(ctx, agent, id, nil, []string{"ballad rock"})
	if _, ok := tagMap(tt)["ballad rock"]; !ok {
		t.Fatal("agent removed a human tag")
	}
	// ... but removes its own
	tt, _ = e.svc.SetTags(ctx, agent, id, nil, []string{"rainy day", "chill"})
	if diff := cmp.Diff(map[string][]string{"ballad rock": {"human"}, "chill": {"human"}}, tagMap(tt)); diff != "" {
		t.Fatalf("agent remove (-want +got):\n%s", diff)
	}
	// a human removal wins over agent tags (and inherited ones)
	if _, err := e.svc.SetTags(ctx, agent, id, []string{"agent only"}, nil); err != nil {
		t.Fatal(err)
	}
	tt, _ = e.svc.SetTags(ctx, device, id, nil, []string{"agent only", "chill"})
	if diff := cmp.Diff(map[string][]string{"ballad rock": {"human"}}, tagMap(tt)); diff != "" {
		t.Fatalf("human remove (-want +got):\n%s", diff)
	}
	if tt, _ = e.svc.SetTags(ctx, human, lt.TrackTwinB, nil, []string{"ballad"}); len(tt.Tags) != 0 {
		t.Fatalf("inherited tag not removed: %+v", tt)
	}
	// the removed rows are kept (soft removal) and a re-add clears removed_at
	var removed int
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM track_tags WHERE removed_at IS NOT NULL AND removed_by IS NOT NULL`).Scan(&removed); err != nil || removed < 4 {
		t.Fatalf("soft-removed rows = %d %v", removed, err)
	}
	e.now = e.now.Add(time.Minute)
	tt, _ = e.svc.SetTags(ctx, agent, id, []string{"agent only"}, nil)
	if diff := cmp.Diff(map[string][]string{"agent only": {"agent"}, "ballad rock": {"human"}}, tagMap(tt)); diff != "" {
		t.Fatalf("re-add (-want +got):\n%s", diff)
	}
	var rows, nulls int
	if err := e.st.R.QueryRow(`SELECT COUNT(*), SUM(removed_at IS NULL AND removed_by IS NULL) FROM track_tags WHERE track_id=? AND tag_id=(SELECT id FROM tags WHERE name='agent only')`, id).Scan(&rows, &nulls); err != nil || rows != 1 || nulls != 1 {
		t.Fatalf("re-added row: rows=%d active=%d %v", rows, nulls, err)
	}
	// run it twice: no duplicate rows
	if _, err := e.svc.SetTags(ctx, agent, id, []string{"agent only"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM track_tags WHERE track_id=?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}

	// limits
	many := make([]string, 31)
	for i := range many {
		many[i] = fmt.Sprintf("tag%02d", i)
	}
	if _, err := e.svc.SetTags(ctx, human, lt.TrackShort, many[:20], nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetTags(ctx, human, lt.TrackShort, many[20:30], nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetTags(ctx, human, lt.TrackShort, many[30:], nil)
	wantInvalid(t, err, "add")
	if tt, _ = e.svc.Tags(ctx, lt.TrackShort); len(tt.Tags) != 30 {
		t.Errorf("the failed call left %d tags", len(tt.Tags))
	}
	_, err = e.svc.SetTags(ctx, agent, lt.TrackShort, many[:21], nil)
	wantInvalid(t, err, "add")
	// per source: the agent has its own 30
	if _, err := e.svc.SetTags(ctx, agent, lt.TrackShort, many[:20], nil); err != nil {
		t.Errorf("agent budget is separate: %v", err)
	}
	_, err = e.svc.SetTags(ctx, human, id, nil, nil)
	wantInvalid(t, err, "body")
	_, err = e.svc.SetTags(ctx, human, id, []string{"bad<tag"}, nil)
	wantCode(t, err, 422, "INVALID_TAG")
	if _, err := e.svc.SetTags(ctx, human, "nope", []string{"x"}, nil); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	if _, err := e.svc.Tags(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("Tags unknown track: %v", err)
	}
}

func TestTagCounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	got, err := e.svc.TagCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []judgments.TagCount{
		{Name: "ballad", Tracks: 2, BySource: judgments.TagSourceCount{Human: 1, Inherited: 1}},
		{Name: "night", Tracks: 1, BySource: judgments.TagSourceCount{Human: 1}},
		{Name: "upbeat", Tracks: 1, BySource: judgments.TagSourceCount{Agent: 1}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestInheritTags_OnInsertTake(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// the parent has human ballad+night, agent upbeat; a removed human tag and an inherited one
	if _, err := e.svc.SetTags(ctx, human, lt.TrackTwinA, []string{"removed soon"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetTags(ctx, human, lt.TrackTwinA, nil, []string{"removed soon"}); err != nil {
		t.Fatal(err)
	}
	child := addTake(t, e, 40, 8, lt.TrackTwinA)
	tt, err := e.svc.Tags(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	// human tags inherit; the agent tag and the removed one do not
	if diff := cmp.Diff(map[string][]string{"ballad": {"inherited"}, "night": {"inherited"}}, tagMap(tt)); diff != "" {
		t.Fatalf("inherited tags (-want +got):\n%s", diff)
	}
	var from, by string
	if err := e.st.R.QueryRow(`SELECT inherited_from, principal_id FROM track_tags WHERE track_id=? LIMIT 1`, child).Scan(&from, &by); err != nil || from != lt.TrackTwinA || by != "system:inherit" {
		t.Fatalf("inherited row: %q %q %v", from, by, err)
	}
	// a grandchild inherits the inherited tags too
	grand := addTake(t, e, 41, 8, child)
	if tt, _ = e.svc.Tags(ctx, grand); len(tt.Tags) != 2 {
		t.Errorf("grandchild tags: %+v", tt)
	}
	// a root has none
	if tt, _ = e.svc.Tags(ctx, addTake(t, e, 42, 8, "")); len(tt.Tags) != 0 {
		t.Errorf("root tags: %+v", tt)
	}
}

// ---- comments ----

func addTimedSections(t *testing.T, e *env, track, source string, secs [][3]any) {
	t.Helper()
	for i, s := range secs {
		var conf any
		if source == "plan" {
			conf = 0.7
		}
		exec(t, e, `INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json) VALUES (?,?,?,?,?,?,?,?,?,'[]')`,
			track, source, i, "verse", s[0], s[0], s[1], s[2], conf)
	}
}

func TestAddComment_SectionLabelOnlyFromTimed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	add := func(p auth.Principal, track string, at float64) judgments.Comment {
		t.Helper()
		c, err := e.svc.AddComment(ctx, p, "api", track, judgments.NewComment{Body: "x", AtSecond: &at})
		if err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
		return c
	}
	label := func(c judgments.Comment) string {
		if c.Section == nil {
			return "<nil>"
		}
		return c.Section.Label
	}
	// Luna: plan sections verse 0-4, chorus 4-8, outro 8-12 (duration 12)
	for _, tc := range []struct {
		at   float64
		want string
	}{{0, "verse"}, {3.9, "verse"}, {4, "chorus"}, {7.95, "outro"}, {8, "outro"}, {11.9, "outro"}, {12, "outro"}} {
		c := add(human, lt.TrackLuna, tc.at)
		if got := label(c); got != tc.want {
			t.Errorf("t=%v: section %s, want %s", tc.at, got, tc.want)
		}
		if c.Section != nil && (c.Section.Source != "plan" || c.Section.Confidence != 0.7) {
			t.Errorf("t=%v: %+v", tc.at, c.Section)
		}
	}
	// untimed track: lyrics sections only, so never a label
	if c := add(human, lt.TrackShort, 1); c.Section != nil {
		t.Errorf("untimed track labelled: %+v", c.Section)
	}
	// a timed track whose plan ends 2 s before its duration (Citta: 6 s)
	addTimedSections(t, e, lt.TrackCitta, "plan", [][3]any{{"intro", 0.0, 2.0}, {"verse", 2.0, 4.0}})
	for _, tc := range []struct {
		at   float64
		want string
	}{{1, "intro"}, {4, "verse"}, {4.1, "<nil>"}, {6, "<nil>"}} {
		if got := label(add(human, lt.TrackCitta, tc.at)); got != tc.want {
			t.Errorf("tail t=%v: %s, want %s", tc.at, got, tc.want)
		}
	}
	// inserted manual sections beat plan, and never fall back to it
	addTimedSections(t, e, lt.TrackCitta, "manual", [][3]any{{"hook", 0.0, 1.0}})
	if c := add(human, lt.TrackCitta, 0.5); label(c) != "hook" || c.Section.Source != "manual" || c.Section.Confidence != 1.0 {
		t.Errorf("manual: %+v", c.Section)
	}
	if c := add(human, lt.TrackCitta, 3); c.Section != nil {
		t.Errorf("fell back to plan under a manual gap: %+v", c.Section)
	}
	// measured sits between manual and plan
	addTimedSections(t, e, lt.TrackTwinA, "plan", [][3]any{{"plan-a", 0.0, 8.0}})
	addTimedSections(t, e, lt.TrackTwinA, "measured", [][3]any{{"measured-a", 0.0, 8.0}})
	if c := add(human, lt.TrackTwinA, 1); label(c) != "measured-a" || c.Section.Source != "measured" {
		t.Errorf("measured: %+v", c.Section)
	}
	// out of range and rounding
	over := 12.5
	_, err := e.svc.AddComment(ctx, human, "web", lt.TrackLuna, judgments.NewComment{Body: "x", AtSecond: &over})
	wantCode(t, err, 422, "AT_SECOND_OUT_OF_RANGE")
	long := addTake(t, e, 50, 20, "")
	if c := add(human, long, 12.345); *c.AtSecond != 12.3 || c.Link != "/tracks/"+long+"?t=12.3" {
		t.Errorf("rounding: %v %q", *c.AtSecond, c.Link)
	}
	neg := -1.0
	_, err = e.svc.AddComment(ctx, human, "web", lt.TrackLuna, judgments.NewComment{Body: "x", AtSecond: &neg})
	wantInvalid(t, err, "at_second")
	// the stored label survives a later plan change: the row is frozen
	var lab string
	if err := e.st.R.QueryRow(`SELECT section_label FROM comments WHERE id=?`, lt.CommentLunaFix).Scan(&lab); err != nil || lab != "chorus" {
		t.Errorf("fixture label %q %v", lab, err)
	}
}

func TestAddComment_Validation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	for name, in := range map[string]judgments.NewComment{
		"empty":     {Body: ""},
		"blank":     {Body: "  \n "},
		"4001":      {Body: strings.Repeat("a", 4001)},
		"NUL":       {Body: "a\x00b"},
		"bad kind":  {Body: "x", Kind: "x"},
		"bad kind2": {Body: "x", Kind: "FIX"},
	} {
		if _, err := e.svc.AddComment(ctx, human, "web", lt.TrackLuna, in); err == nil {
			t.Errorf("%s accepted", name)
		} else {
			var ve *library.ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	if c, err := e.svc.AddComment(ctx, human, "web", lt.TrackLuna, judgments.NewComment{Body: strings.Repeat("é", 4000)}); err != nil || len([]rune(c.Body)) != 4000 {
		t.Errorf("4000 runes: %v", err)
	}
	c, err := e.svc.AddComment(ctx, agent, "mcp", lt.TrackLuna, judgments.NewComment{Body: "  hello  "})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "note" || c.State != "open" || c.Source != "mcp" || c.Body != "hello" || c.AtSecond != nil || c.Link != "" || c.Section != nil ||
		c.Author.Kind != "agent" || c.Author.ID != "agent:tok-1" || c.Author.Name != "claude" || c.EditedAt != nil || c.StateChangedAt != nil || c.Hidden ||
		c.CreatedAt != c.UpdatedAt || c.TrackID != lt.TrackLuna {
		t.Errorf("created comment %+v", c)
	}
	for _, src := range []string{"web", "api", "mcp", "stdio"} {
		if c, err := e.svc.AddComment(ctx, device, src, lt.TrackLuna, judgments.NewComment{Body: "s", Kind: "marker"}); err != nil || c.Source != src || c.Author.Kind != "device" {
			t.Errorf("source %s: %+v %v", src, c, err)
		}
	}
	if _, err := e.svc.AddComment(ctx, human, "subsonic", lt.TrackLuna, judgments.NewComment{Body: "x"}); err == nil {
		t.Error("unknown source accepted")
	}
	if _, err := e.svc.AddComment(ctx, human, "web", "nope", judgments.NewComment{Body: "x"}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}

func TestCommentTransitions(t *testing.T) {
	t.Parallel()
	states := []string{"open", "addressed", "resolved", "regressed"}
	allowed := map[[2]string]bool{
		{"open", "open"}: true, {"open", "resolved"}: true,
		{"addressed", "open"}: true, {"addressed", "addressed"}: true, {"addressed", "resolved"}: true,
		{"resolved", "open"}: true, {"resolved", "resolved"}: true,
		{"regressed", "open"}: true, {"regressed", "resolved"}: true, {"regressed", "regressed"}: true,
	}
	e := newEnv(t)
	ctx := context.Background()
	n := 0
	for _, from := range states {
		for _, to := range states {
			want := allowed[[2]string{from, to}]
			if err := judgments.Transition(from, to); (err == nil) != want || (err != nil && !errors.Is(err, judgments.ErrInvalidTransition)) {
				t.Errorf("Transition(%s,%s) = %v, want allowed=%v", from, to, err, want)
			}
			for _, p := range []auth.Principal{human, agent} {
				n++
				// a fresh comment forced into `from`
				id := lt.CommentLunaNote
				exec(t, e, `UPDATE comments SET state = ? WHERE id = ?`, from, id)
				c, err := e.svc.UpdateComment(ctx, p, id, judgments.CommentPatch{State: &to})
				if want {
					if err != nil || c.State != to {
						t.Errorf("%s->%s as %s: %v %q", from, to, p.Kind, err, c.State)
					}
					if from != to && (c.StateChangedAt == nil) {
						t.Errorf("%s->%s: state_changed_at not set", from, to)
					}
				} else {
					wantCode(t, err, 409, "INVALID_TRANSITION")
					if !errors.Is(err, judgments.ErrInvalidTransition) {
						t.Errorf("error does not wrap ErrInvalidTransition")
					}
					cur, _ := e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{})
					for _, x := range cur.Items {
						if x.ID == id && x.State != from {
							t.Errorf("%s->%s refused but state became %s", from, to, x.State)
						}
					}
				}
				exec(t, e, `UPDATE comments SET state_changed_at = NULL, state_changed_by = NULL WHERE id = ?`, id)
			}
		}
	}
	if n != 32 {
		t.Fatalf("ran %d", n)
	}
	// state_changed_by records the principal
	to := "resolved"
	exec(t, e, `UPDATE comments SET state = 'open' WHERE id = ?`, lt.CommentLunaNote)
	if _, err := e.svc.UpdateComment(ctx, agent, lt.CommentLunaNote, judgments.CommentPatch{State: &to}); err != nil {
		t.Fatal(err)
	}
	var by string
	if err := e.st.R.QueryRow(`SELECT state_changed_by FROM comments WHERE id=?`, lt.CommentLunaNote).Scan(&by); err != nil || by != "agent:tok-1" {
		t.Errorf("state_changed_by %q %v", by, err)
	}
	if err := judgments.Transition("open", "nope"); !errors.Is(err, judgments.ErrInvalidTransition) {
		t.Errorf("unknown state: %v", err)
	}
}

func TestUpdateComment_Permissions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	mk := func(p auth.Principal) string {
		c, err := e.svc.AddComment(ctx, p, "api", lt.TrackLuna, judgments.NewComment{Body: "orig"})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	body := func(s string) judgments.CommentPatch { return judgments.CommentPatch{Body: &s} }
	hide := func(h bool) judgments.CommentPatch { return judgments.CommentPatch{Hidden: &h} }
	e.now = e.now.Add(time.Minute)

	humanC, deviceC, agentC := mk(human), mk(device), mk(agent)
	// a device edits the body of a human's comment; a human edits a device's
	if c, err := e.svc.UpdateComment(ctx, device, humanC, body("by device")); err != nil || c.Body != "by device" || c.EditedAt == nil {
		t.Errorf("device edits human: %v %+v", err, c)
	}
	if c, err := e.svc.UpdateComment(ctx, human, deviceC, body("by human")); err != nil || c.Body != "by human" {
		t.Errorf("human edits device: %v", err)
	}
	// an agent edits a human's comment: refused
	_, err := e.svc.UpdateComment(ctx, agent, humanC, body("nope"))
	wantCode(t, err, 403, "NOT_AUTHOR")
	_, err = e.svc.UpdateComment(ctx, agent, deviceC, judgments.CommentPatch{Kind: sp("fix")})
	wantCode(t, err, 403, "NOT_AUTHOR")
	// a human hides an agent comment but cannot edit its body
	if c, err := e.svc.UpdateComment(ctx, human, agentC, hide(true)); err != nil || !c.Hidden {
		t.Errorf("human hides agent comment: %v %+v", err, c)
	}
	_, err = e.svc.UpdateComment(ctx, human, agentC, body("nope"))
	wantCode(t, err, 403, "NOT_AUTHOR")
	// an agent hides its own (and unhides it) but not another agent's, nor a human's
	if c, err := e.svc.UpdateComment(ctx, agent, agentC, hide(false)); err != nil || c.Hidden {
		t.Errorf("agent unhides own: %v %+v", err, c)
	}
	if c, err := e.svc.UpdateComment(ctx, agent, agentC, hide(true)); err != nil || !c.Hidden {
		t.Errorf("agent hides own: %v", err)
	}
	_, err = e.svc.UpdateComment(ctx, agent2, agentC, hide(false))
	wantCode(t, err, 403, "NOT_AUTHOR")
	_, err = e.svc.UpdateComment(ctx, agent, humanC, hide(true))
	wantCode(t, err, 403, "NOT_AUTHOR")
	if c, err := e.svc.UpdateComment(ctx, agent, agentC, body("mine")); err != nil || c.Body != "mine" {
		t.Errorf("agent edits own: %v", err)
	}
	// anyone with write may change the state
	res := "resolved"
	if c, err := e.svc.UpdateComment(ctx, agent2, humanC, judgments.CommentPatch{State: &res}); err != nil || c.State != "resolved" {
		t.Errorf("agent resolves a human comment: %v", err)
	}
	// a refused call changes nothing, even its permitted part
	_, err = e.svc.UpdateComment(ctx, agent2, humanC, judgments.CommentPatch{Body: sp("x"), State: sp("open")})
	wantCode(t, err, 403, "NOT_AUTHOR")
	if cur, _ := e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{Kind: []string{"note"}}); true {
		for _, c := range cur.Items {
			if c.ID == humanC && c.State != "resolved" {
				t.Error("partial update applied")
			}
		}
	}
	// the kind edit marks edited_at; an empty patch is 422; validation
	if c, err := e.svc.UpdateComment(ctx, human, humanC, judgments.CommentPatch{Kind: sp("fix")}); err != nil || c.Kind != "fix" || c.EditedAt == nil {
		t.Errorf("kind edit: %v %+v", err, c)
	}
	_, err = e.svc.UpdateComment(ctx, human, humanC, judgments.CommentPatch{})
	wantInvalid(t, err, "body")
	_, err = e.svc.UpdateComment(ctx, human, humanC, body(""))
	wantInvalid(t, err, "body")
	_, err = e.svc.UpdateComment(ctx, human, humanC, judgments.CommentPatch{State: sp("done")})
	wantInvalid(t, err, "state")
	if _, err := e.svc.UpdateComment(ctx, human, "nope", body("x")); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown comment: %v", err)
	}
	// the frozen columns stay frozen: author and section label are unchanged
	c, _ := e.svc.UpdateComment(ctx, human, humanC, body("final"))
	if c.Author.ID != "human:alice" || c.Source != "api" {
		t.Errorf("frozen fields changed: %+v", c)
	}
	// an unchanged body is a no-op: edited_at keeps its value
	before := c.EditedAt
	e.now = e.now.Add(time.Hour)
	c2, _ := e.svc.UpdateComment(ctx, human, humanC, body("final"))
	if c2.EditedAt == nil || *c2.EditedAt != *before || c2.UpdatedAt != c.UpdatedAt {
		t.Errorf("no-op edit touched the row: %+v vs %+v", c2, c)
	}
}

func TestListComments_OrderFiltersCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	idsOf := func(p judgments.CommentPage) []string {
		var out []string
		for _, c := range p.Items {
			out = append(out, c.ID)
		}
		return out
	}
	// newest first: the fixture's created_at orders note < fix < keep (-90, -80, -70 min)
	p, err := e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{lt.CommentLunaKeep, lt.CommentLunaFix, lt.CommentLunaNote}, idsOf(p)); diff != "" || p.Total != 3 || p.NextCursor != nil {
		t.Errorf("newest (-want +got):\n%s total=%d", diff, p.Total)
	}
	// by time: at_second asc, NULLs last
	p, _ = e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{Order: "time"})
	if diff := cmp.Diff([]string{lt.CommentLunaKeep, lt.CommentLunaFix, lt.CommentLunaNote}, idsOf(p)); diff != "" {
		t.Errorf("time order (-want +got):\n%s", diff)
	}
	// filters
	p, _ = e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{Kind: []string{"fix", "keep"}})
	if p.Total != 2 {
		t.Errorf("kind filter total %d", p.Total)
	}
	p, _ = e.svc.Comments(ctx, lt.TrackLuna, judgments.CommentFilter{State: []string{"resolved"}})
	if p.Total != 0 || len(p.Items) != 0 || p.Items == nil {
		t.Errorf("state filter %+v", p)
	}
	// hidden comments only on request
	p, _ = e.svc.Comments(ctx, lt.TrackShort, judgments.CommentFilter{})
	if p.Total != 1 || p.Items[0].ID != lt.CommentShortTimed {
		t.Errorf("hidden shown: %v", idsOf(p))
	}
	p, _ = e.svc.Comments(ctx, lt.TrackShort, judgments.CommentFilter{IncludeHidden: true})
	if p.Total != 2 {
		t.Errorf("include_hidden total %d", p.Total)
	}
	var hidden judgments.Comment
	for _, c := range p.Items {
		if c.ID == lt.CommentShortHid {
			hidden = c
		}
	}
	if !hidden.Hidden || hidden.Author.Kind != "agent" {
		t.Errorf("hidden comment %+v", hidden)
	}
	for name, f := range map[string]judgments.CommentFilter{
		"state": {State: []string{"x"}}, "kind": {Kind: []string{"x"}}, "order": {Order: "x"}, "cursor": {Cursor: "!!"},
	} {
		if _, err := e.svc.Comments(ctx, lt.TrackLuna, f); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := e.svc.Comments(ctx, "nope", judgments.CommentFilter{}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}

	// the 90 000-byte page cap: 60 comments of 4000 characters
	track := addTake(t, e, 60, 30, "")
	var want []string
	for i := 0; i < 60; i++ {
		e.now = e.now.Add(time.Second)
		c, err := e.svc.AddComment(ctx, human, "web", track, judgments.NewComment{Body: strings.Repeat(string(rune('a'+i%26)), 4000)})
		if err != nil {
			t.Fatal(err)
		}
		want = append([]string{c.ID}, want...)
	}
	var got []string
	cursor, pages := "", 0
	for {
		pg, err := e.svc.Comments(ctx, track, judgments.CommentFilter{Limit: 200, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(pg)
		if len(b) > 90000 {
			t.Fatalf("page of %d bytes", len(b))
		}
		if pg.Total != 60 || len(pg.Items) == 0 {
			t.Fatalf("page %+v", pg.Total)
		}
		got = append(got, idsOf(pg)...)
		pages++
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	if pages < 3 || !slices.Equal(got, want) {
		t.Errorf("paged %d pages, %d comments, in order=%v", pages, len(got), slices.Equal(got, want))
	}
}

func TestAddComment_NotesCreatedID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := audit.WithCollector(context.Background())
	c, err := e.svc.AddComment(ctx, human, "web", lt.TrackLuna, judgments.NewComment{Body: "x"})
	if err != nil || c.ID == "" {
		t.Fatal(err)
	}
	if got := audit.Created(ctx); !slices.Equal(got, []string{c.ID}) {
		t.Errorf("created ids = %v, want [%s]", got, c.ID)
	}
}
