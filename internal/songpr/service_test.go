package songpr_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var (
	imageOnce sync.Once
	image     []byte
	fxInfo    lt.M5Info
)

// fixtureImage builds the FixtureM5 database once per process; every rig copies it.
func fixtureImage(t *testing.T) ([]byte, lt.M5Info) {
	imageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-songpr-fixture-")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		p := filepath.Join(dir, "s.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Error(err)
			return
		}
		fxInfo = lt.FixtureM5(t, st, lt.FixedNow)
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
		image, _ = os.ReadFile(p) //nolint:gosec // our own temp dir
	})
	if image == nil {
		t.Fatal("fixture image unavailable")
	}
	return image, fxInfo
}

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Via: "proxy", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "fixture-token", Name: "fixture-agent", Via: "token", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}}
)

type rig struct {
	t   *testing.T
	st  *store.Store
	svc *songpr.Service
	fx  lt.M5Info
	n   atomic.Int64
}

func (r *rig) id() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc000+r.n.Add(1)) }

func newRig(t *testing.T) *rig {
	t.Helper()
	img, fx := fixtureImage(t)
	p := filepath.Join(t.TempDir(), "s.db")
	if err := os.WriteFile(p, img, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, fx: fx}
	r.svc = songpr.New(songpr.Options{Store: st, Now: func() time.Time { return lt.FixedNow }, NewID: r.id})
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }

func replace(sec, line int, text string) songpr.OpJSON {
	return songpr.OpJSON{Op: songpr.OpReplaceLine, SectionIdx: ip(sec), LineIdx: ip(line), Text: sp(text)}
}
func setStyle(s string) songpr.OpJSON { return songpr.OpJSON{Op: songpr.OpSetStyle, Style: sp(s)} }

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want problem %d %s", err, status, code)
	}
	if pe.Status != status || pe.Code != code {
		t.Fatalf("problem = %d %s (%s), want %d %s", pe.Status, pe.Code, pe.Message, status, code)
	}
	return pe
}

// ---- propose ----

func TestPropose_Success_AncestorComments(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g2 := r.fx.Lineage[2]
	// the comments sit on the grandparent G0 (plan-timed: chorus 1 at 40 s, verse 1 at 12 s); the base is G2 (lyrics-only)
	got, err := r.svc.Propose(context.Background(), agent, songpr.ProposeInput{
		BaseTrackID: g2, AddressesCommentIDs: []string{r.fx.Comments["fix_chorus"], r.fx.Comments["fix_verse"]}, Rationale: "land the hook earlier",
		Title: sp("Hook earlier"),
		Ops:   []songpr.OpJSON{replace(1, 0, "a brand new hook line"), replace(0, 1, "a calmer second line")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operator != songpr.OperatorLyricEdit || got.Base.TrackID != g2 || got.Title == nil || *got.Title != "Hook earlier" || got.Author.Key != agent.Key() || got.Author.Kind != "agent" {
		t.Errorf("patch = %+v", got.Summary)
	}
	if len(got.Diff.Lyrics) != 2 || got.Diff.Lyrics[0].SectionIdx != 0 || got.Diff.Lyrics[1].SectionIdx != 1 || got.Diff.Style != nil {
		t.Errorf("diff = %+v", got.Diff)
	}
	if got.Bounds.Bounds.LinesChanged != 2 || len(got.Bounds.Sections) != 2 {
		t.Errorf("bounds = %+v", got.Bounds)
	}
	if got.Compiled.Lyrics == nil || !strings.Contains(*got.Compiled.Lyrics, "a brand new hook line") || got.Compiled.Style != nil {
		t.Errorf("compiled = %+v", got.Compiled)
	}
	if len(got.Addresses) != 2 || len(got.Renders) != 0 || got.Callbacks != (songpr.Tally{}) {
		t.Errorf("addresses/renders/tally = %+v %+v %+v", got.Addresses, got.Renders, got.Callbacks)
	}
	for _, a := range got.Addresses {
		if a.TrackID != r.fx.Lineage[0] || a.State != "open" || a.Kind != "fix" || a.BodyExcerpt == "" {
			t.Errorf("address = %+v", a)
		}
	}
	// stored, immutable, and what Get returns is what Propose returned
	back, err := r.svc.Get(context.Background(), human, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(got, back); d != "" {
		t.Errorf("Get differs (-propose +get):\n%s", d)
	}
	if n := r.count(`SELECT COUNT(*) FROM patches`); n != 1 {
		t.Errorf("%d patches", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM patch_comments WHERE patch_id = ?`, got.ID); n != 2 {
		t.Errorf("%d patch_comments", n)
	}
	if _, err := r.st.W.Exec(`UPDATE patches SET rationale = 'x' WHERE id = ?`, got.ID); err == nil {
		t.Error("a stored patch was updated")
	}
}

func TestPropose_OperatorsAndStyle(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g0 := r.fx.Lineage[0]
	fix := r.fx.Comments["fix_chorus"]
	style := "folk, warm, male vocal, italian, 100bpm" // base: folk, warm, male vocal, italian, 90bpm
	for _, c := range []struct {
		name string
		ops  []songpr.OpJSON
		want string
	}{
		{"style only", []songpr.OpJSON{setStyle(style)}, songpr.OperatorStyleEdit},
		{"lyrics and style", []songpr.OpJSON{replace(1, 2, "other words"), setStyle(style)}, songpr.OperatorMultiEdit},
	} {
		got, err := r.svc.Propose(context.Background(), human, songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{fix}, Rationale: "r", Ops: c.ops})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Operator != c.want || got.Compiled.Style == nil || *got.Compiled.Style != style || got.Diff.Style == nil || got.Bounds.Bounds.BPMDelta != 10 {
			t.Errorf("%s: %+v %+v", c.name, got.Operator, got.Bounds.Bounds)
		}
	}
}

func TestPropose_ErrorTable(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g0, g2 := r.fx.Lineage[0], r.fx.Lineage[2]
	cm := r.fx.Comments
	// a comment on a track outside G2's lineage
	foreign := "01900000-0000-7000-8000-00000000f001"
	r.exec(`INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?, ?, 'foreign', 'fix', 'open', 'web', 'human', 'fixture', 'fixture', '2026-10-06T00:00:00Z', '2026-10-06T00:00:00Z')`, foreign, lt.TrackLuna)
	secLines := func(sec, n int) []songpr.OpJSON {
		var ops []songpr.OpJSON
		for i := 0; i < n; i++ {
			ops = append(ops, replace(sec, i, fmt.Sprintf("line %d-%d", sec, i)))
		}
		return ops
	}
	cases := []struct {
		name   string
		in     songpr.ProposeInput
		status int
		code   string
		detail string // a details key that must be present
	}{
		{"unknown base", songpr.ProposeInput{BaseTrackID: "nope", AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{replace(1, 0, "x")}}, 422, "UNKNOWN_TRACK", "track_id"},
		{"reference base", songpr.ProposeInput{BaseTrackID: lt.TrackRefFlac, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{replace(1, 0, "x")}}, 422, "RECIPE_UNKNOWN", "track_id"},
		{"hidden comment", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["hidden_fix"]}, Ops: []songpr.OpJSON{replace(0, 0, "x")}}, 422, "COMMENT_NOT_ADDRESSABLE", "why"},
		{"resolved comment", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["resolved_fix"]}, Ops: []songpr.OpJSON{replace(2, 0, "x")}}, 422, "COMMENT_NOT_ADDRESSABLE", "why"},
		{"keep comment", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["keep"]}, Ops: []songpr.OpJSON{replace(3, 0, "x")}}, 422, "COMMENT_NOT_ADDRESSABLE", "why"},
		{"foreign lineage", songpr.ProposeInput{BaseTrackID: g2, AddressesCommentIDs: []string{foreign}, Ops: []songpr.OpJSON{replace(0, 0, "x")}}, 422, "COMMENT_NOT_ADDRESSABLE", "why"},
		{"unknown comment", songpr.ProposeInput{BaseTrackID: g2, AddressesCommentIDs: []string{"no-such-comment"}, Ops: []songpr.OpJSON{replace(0, 0, "x")}}, 422, "COMMENT_NOT_ADDRESSABLE", "why"},
		{"line outside the scoped sections", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{replace(2, 0, "x")}}, 422, "PATCH_OUT_OF_BOUNDS", "violations"},
		{"nine lines", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"], cm["fix_verse"]}, ExtraSections: []int{2},
			Ops: append(append(secLines(0, 4), secLines(1, 4)...), secLines(2, 1)...)}, 422, "PATCH_OUT_OF_BOUNDS", "violations"},
		{"four new style terms", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{setStyle("rock, loud, fast, dark, female vocal, italian, 90bpm")}}, 422, "PATCH_OUT_OF_BOUNDS", "violations"},
		{"bpm plus 21", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{setStyle("folk, warm, male vocal, italian, 111bpm")}}, 422, "PATCH_OUT_OF_BOUNDS", "violations"},
		{"no such line", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{replace(1, 9, "x")}}, 422, "PATCH_INVALID", "rule"},
		{"replace_line without a text", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{{Op: songpr.OpReplaceLine, SectionIdx: ip(1), LineIdx: ip(0)}}}, 422, "PATCH_INVALID", "rule"},
		{"replace_line without a section", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{{Op: songpr.OpReplaceLine, LineIdx: ip(0), Text: sp("x")}}}, 422, "PATCH_INVALID", "rule"},
		{"unknown op", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{{Op: "delete_section"}}}, 422, "PATCH_INVALID", "rule"},
		{"same text", songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{cm["fix_chorus"]}, Ops: []songpr.OpJSON{replace(1, 0, "fixture hook b1")}}, 422, "PATCH_NO_CHANGE", "violations"},
	}
	for _, c := range cases {
		c.in.Rationale = "r"
		_, err := r.svc.Propose(context.Background(), agent, c.in)
		pe := wantProblem(t, err, c.status, c.code)
		if _, ok := pe.Details[c.detail]; !ok {
			t.Errorf("%s: details %v lack %q", c.name, pe.Details, c.detail)
		}
		for _, v := range pe.Details {
			if strings.Contains(fmt.Sprint(v), "fixture hook") || strings.Contains(fmt.Sprint(v), "chorus lands late") {
				t.Errorf("%s: details leak lyric or comment text: %v", c.name, v)
			}
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM patches`) + r.count(`SELECT COUNT(*) FROM patch_comments`); n != 0 {
		t.Errorf("a refused proposal wrote %d rows", n)
	}
}

func TestPropose_DiscardedAndLossyBases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g0 := r.fx.Lineage[0]
	in := songpr.ProposeInput{BaseTrackID: g0, AddressesCommentIDs: []string{r.fx.Comments["fix_chorus"]}, Rationale: "r", Ops: []songpr.OpJSON{replace(1, 0, "x")}}
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, g0)
	pe := wantProblem(t, func() error { _, err := r.svc.Propose(context.Background(), human, in); return err }(), 422, "PATCH_INVALID")
	if pe.Details["rule"] != "base_discarded" {
		t.Errorf("details = %v", pe.Details)
	}
	r.exec(`UPDATE tracks SET status = 'draft' WHERE id = ?`, g0)
	r.exec(`DROP TRIGGER tracks_lossy_frozen`)
	r.exec(`UPDATE tracks SET lossy = 1 WHERE id = ?`, g0)
	_, err := r.svc.Propose(context.Background(), human, in)
	wantProblem(t, err, 422, "LOSSY_MASTER")
}

func TestPropose_NotReady(t *testing.T) {
	t.Parallel()
	_, err := songpr.New(songpr.Options{}).Propose(context.Background(), human, songpr.ProposeInput{})
	wantProblem(t, err, 503, "UNAVAILABLE")
	_, err = songpr.New(songpr.Options{}).Get(context.Background(), human, "x")
	wantProblem(t, err, 503, "UNAVAILABLE")
}

// ---- reads ----

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Get(context.Background(), human, "no-such-patch")
	wantProblem(t, err, 404, "NOT_FOUND")
}

func (r *rig) propose(p auth.Principal, base string, comments []string, ops ...songpr.OpJSON) songpr.SongPR {
	r.t.Helper()
	got, err := r.svc.Propose(context.Background(), p, songpr.ProposeInput{BaseTrackID: base, AddressesCommentIDs: comments, Rationale: "r", Ops: ops})
	if err != nil {
		r.t.Fatalf("propose: %v", err)
	}
	return got
}

func TestList_FiltersAndPaging(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g0, g1 := r.fx.Lineage[0], r.fx.Lineage[1]
	cm := r.fx.Comments
	a := r.propose(human, g0, []string{cm["fix_chorus"]}, replace(1, 0, "one"))
	b := r.propose(agent, g0, []string{cm["fix_verse"]}, replace(0, 0, "two"))
	c := r.propose(agent, g1, []string{cm["fix_chorus"]}, replace(1, 1, "three"))
	all, err := r.svc.List(context.Background(), human, songpr.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 3 || len(all.Items) != 3 || all.NextCursor != nil {
		t.Fatalf("all = %d items, total %d", len(all.Items), all.Total)
	}
	ids := func(p songpr.Page) []string {
		var out []string
		for _, i := range p.Items {
			out = append(out, i.ID)
		}
		return out
	}
	// newest first: ids are monotonic in this rig and created_at is equal, so id desc breaks the tie
	if d := cmp.Diff([]string{c.ID, b.ID, a.ID}, ids(all)); d != "" {
		t.Errorf("order (-want +got):\n%s", d)
	}
	for _, f := range []struct {
		name string
		f    songpr.ListFilter
		want []string
	}{
		{"by base take", songpr.ListFilter{TrackID: g1}, []string{c.ID}},
		{"by comment", songpr.ListFilter{CommentID: cm["fix_chorus"]}, []string{c.ID, a.ID}},
		{"by root", songpr.ListFilter{RootTrackID: g0}, []string{c.ID, b.ID, a.ID}},
		{"by an unrelated root", songpr.ListFilter{RootTrackID: lt.TrackLuna}, nil},
	} {
		got, err := r.svc.List(context.Background(), human, f.f)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if d := cmp.Diff(f.want, ids(got)); d != "" || got.Total != len(f.want) {
			t.Errorf("%s (-want +got):\n%s total %d", f.name, d, got.Total)
		}
	}
	p1, err := r.svc.List(context.Background(), human, songpr.ListFilter{Limit: 2})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor == nil || p1.Total != 3 {
		t.Fatalf("page 1 = %+v %v", p1, err)
	}
	p2, err := r.svc.List(context.Background(), human, songpr.ListFilter{Limit: 2, Cursor: *p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != nil {
		t.Fatalf("page 2 = %+v %v", p2, err)
	}
	if got := append(ids(p1), ids(p2)...); cmp.Diff([]string{c.ID, b.ID, a.ID}, got) != "" {
		t.Errorf("paged ids = %v", got)
	}
	_, err = r.svc.List(context.Background(), human, songpr.ListFilter{Cursor: "not-a-cursor"})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// list rows carry no ops and no diff by type; they carry the summary
	if all.Items[0].Operator == "" || len(all.Items[0].Addresses) != 1 {
		t.Errorf("summary = %+v", all.Items[0])
	}
}
