package crates_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	agent = auth.Principal{Kind: auth.KindAgent, ID: "tok", Name: "tok", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
	human = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}, Via: "proxy"}
)

type world struct {
	t   *testing.T
	r   *servertest.Rig
	svc *crates.Service
	ctx context.Context
}

// newWorld is the settled L2 rig (FixtureL2 over the L1 mirror).
func newWorld(t *testing.T) *world {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	r.Settle(t)
	return &world{t: t, r: r, svc: r.Services.Crates, ctx: context.Background()}
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s string
	if err := w.r.Store.R.QueryRow(q, args...).Scan(&s); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (w *world) song(label string) string {
	return w.str(`SELECT id FROM songs WHERE title = ?`, "Synthetic Song "+label)
}

func (w *world) take(title string) string {
	return w.str(`SELECT id FROM tracks WHERE title = ?`, "Synthetic Take "+strings.ToUpper(title))
}

func (w *world) crate(name string) string {
	return w.str(`SELECT id FROM crates WHERE name = ?`, name)
}

// songLabels maps song ids back to S1..S7 (other Songs of the L1 mirror keep their id).
func (w *world) songLabels(items []songs.Summary) []string {
	var out []string
	for _, it := range items {
		out = append(out, strings.TrimPrefix(it.Title, "Synthetic Song "))
	}
	slices.Sort(out)
	return out
}

func code(err error) (string, int) {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Status
	}
	return fmt.Sprint(err), 0
}

func wantCode(t *testing.T, what string, err error, wantCode string, wantStatus int) {
	t.Helper()
	if c, s := code(err); c != wantCode || s != wantStatus {
		t.Errorf("%s: %q %d, want %q %d", what, c, s, wantCode, wantStatus)
	}
}

func (w *world) listSongs(f songs.ListFilter) []string {
	w.t.Helper()
	f.Limit = 100
	p, err := w.r.Services.Songs.List(w.ctx, f)
	if err != nil {
		w.t.Fatalf("list songs %+v: %v", f, err)
	}
	return w.songLabels(p.Items)
}

// TestAcceptance_L2_SmartCrate: the worked example of C125.5 on FixtureL2: {S1, S6}, identical through the Crate's expansion,
// the explicit parameters and the Crate's own count.
func TestAcceptance_L2_SmartCrate(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	id := w.crate("Synthetic Unheard Reggae")
	yes, no, d240 := true, false, 240.0
	explicit := songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae"}}, Unheard: &yes, MinDurationS: &d240, PickDecided: &no}
	want := []string{"S1", "S6"}
	if got := w.listSongs(explicit); !slices.Equal(got, want) {
		t.Fatalf("explicit parameters = %v, want %v", got, want)
	}
	exp, err := w.svc.ExpandSongs(w.ctx, id, songs.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if exp.CrateID != "" {
		t.Error("a smart expansion carries no crate_id")
	}
	if got := w.listSongs(exp); !slices.Equal(got, want) {
		t.Errorf("expanded crate = %v, want %v", got, want)
	}
	c, n, err := w.svc.Get(w.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := w.r.Services.Songs.List(w.ctx, exp)
	if n != 2 || n != p.Total || c.Kind != crates.KindSmart {
		t.Errorf("count %d, total %d", n, p.Total)
	}
	// the manual Song crate through the membership join
	mexp, err := w.svc.ExpandSongs(w.ctx, w.crate("Synthetic Remake Candidates"), songs.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.listSongs(mexp); !slices.Equal(got, []string{"S1", "S4", "S6"}) {
		t.Errorf("manual song crate = %v", got)
	}
	// the smart Take crate counts the same as its expansion
	tid := w.crate("Synthetic Unheard Drafts")
	_, tn, err := w.svc.Get(w.ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := w.svc.ExpandTracks(w.ctx, tid, library.SearchParams{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := library.New(w.r.Store, nil, func() time.Time { return librarytest.FixedNow }).SearchTracks(w.ctx, tp)
	if err != nil || page.Total != tn {
		t.Errorf("smart take crate count %d, search total %d (%v)", tn, page.Total, err)
	}
}

func TestCrates_CreateRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s1, t1 := w.song("S1"), w.take("s1a")
	for _, subject := range []crates.Subject{crates.SubjectSong, crates.SubjectTake} {
		item := crates.Member{SongID: s1}
		filter := json.RawMessage(`{"unheard":true}`)
		if subject == crates.SubjectTake {
			item = crates.Member{TrackID: t1}
		}
		c, n, err := w.svc.Create(w.ctx, agent, crates.NewCrate{Name: "  Synthetic   Crate  ", Kind: crates.KindManual, Subject: subject, Items: []crates.Member{item, item}})
		if err != nil || n != 1 || c.Name != "Synthetic Crate" || c.Kind != crates.KindManual || c.Revision != 1 || c.CreatedBy != agent.Key() {
			t.Errorf("manual %s: %+v %d %v", subject, c, n, err)
		}
		c, n, err = w.svc.Create(w.ctx, human, crates.NewCrate{Name: "Smart " + string(subject), Kind: crates.KindSmart, Subject: subject, Filter: filter})
		if err != nil || c.Kind != crates.KindSmart || n < 0 {
			t.Errorf("smart %s: %+v %d %v", subject, c, n, err)
		}
	}
	// the default subject is song
	if c, _, err := w.svc.Create(w.ctx, human, crates.NewCrate{Name: "Default", Kind: crates.KindManual}); err != nil || c.Subject != crates.SubjectSong {
		t.Errorf("default subject: %+v %v", c, err)
	}
	big := json.RawMessage(`{"q":"` + strings.Repeat("a", 4100) + `"}`)
	for _, tc := range []struct {
		name string
		in   crates.NewCrate
		code string
		st   int
	}{
		{"smart without filter", crates.NewCrate{Name: "x", Kind: crates.KindSmart}, "VALIDATION_FAILED", 422},
		{"manual with filter", crates.NewCrate{Name: "x", Kind: crates.KindManual, Filter: json.RawMessage(`{}`)}, "VALIDATION_FAILED", 422},
		{"smart with items", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{}`), Items: []crates.Member{{SongID: s1}}}, "CRATE_IS_SMART", 409},
		{"unknown key", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"nope":1}`)}, "UNKNOWN_FILTER_KEY", 422},
		{"non-saved key", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"limit":5}`)}, "UNKNOWN_FILTER_KEY", 422},
		{"crate_id inside", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"crate_id":"c"}`)}, "UNKNOWN_FILTER_KEY", 422},
		{"origin is not a take key", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Subject: crates.SubjectTake, Filter: json.RawMessage(`{"origin":["job"]}`)}, "UNKNOWN_FILTER_KEY", 422},
		{"take key on a song crate", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"status":["draft"]}`)}, "UNKNOWN_FILTER_KEY", 422},
		{"wrong type", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"unheard":"yes"}`)}, "INVALID_FILTER", 422},
		{"out of range", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"min_stars":9}`)}, "INVALID_FILTER", 422},
		{"empty array", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"tag":[]}`)}, "INVALID_FILTER", 422},
		{"null value", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"q":null}`)}, "INVALID_FILTER", 422},
		{"bad state", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"state":["gone"]}`)}, "INVALID_FILTER", 422},
		{"bad sound", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"sound":["nofacet:x"]}`)}, "INVALID_FILTER", 422},
		{"min_stars with unrated", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`{"min_stars":3,"unrated":true}`)}, "INVALID_FILTER", 422},
		{"not an object", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: json.RawMessage(`[1]`)}, "INVALID_FILTER", 422},
		{"too large", crates.NewCrate{Name: "x", Kind: crates.KindSmart, Filter: big}, "FILTER_TOO_LARGE", 422},
		{"empty name", crates.NewCrate{Name: "  ", Kind: crates.KindManual}, "VALIDATION_FAILED", 422},
		{"long name", crates.NewCrate{Name: strings.Repeat("n", 81), Kind: crates.KindManual}, "VALIDATION_FAILED", 422},
		{"control character", crates.NewCrate{Name: "a\x07b", Kind: crates.KindManual}, "VALIDATION_FAILED", 422},
		{"long description", crates.NewCrate{Name: "x", Description: strings.Repeat("d", 501), Kind: crates.KindManual}, "VALIDATION_FAILED", 422},
		{"bad kind", crates.NewCrate{Name: "x", Kind: "hybrid"}, "VALIDATION_FAILED", 422},
		{"wrong subject member", crates.NewCrate{Name: "x", Kind: crates.KindManual, Items: []crates.Member{{TrackID: t1}}}, "SUBJECT_MISMATCH", 422},
		{"unknown song", crates.NewCrate{Name: "x", Kind: crates.KindManual, Items: []crates.Member{{SongID: "nope"}}}, "UNKNOWN_SONG", 422},
		{"unknown take", crates.NewCrate{Name: "x", Kind: crates.KindManual, Subject: crates.SubjectTake, Items: []crates.Member{{TrackID: "nope"}}}, "UNKNOWN_TRACK", 422},
		{"member with both ids", crates.NewCrate{Name: "x", Kind: crates.KindManual, Items: []crates.Member{{SongID: s1, TrackID: t1}}}, "VALIDATION_FAILED", 422},
		{"too many items", crates.NewCrate{Name: "x", Kind: crates.KindManual, Items: make([]crates.Member, 201)}, "VALIDATION_FAILED", 422},
	} {
		_, _, err := w.svc.Create(w.ctx, agent, tc.in)
		wantCode(t, tc.name, err, tc.code, tc.st)
	}
	if n := w.str(`SELECT COUNT(*) FROM crates WHERE name = 'x'`); n != "0" {
		t.Errorf("%s refused Crates were stored", n)
	}
	// a retired Song cannot be added
	w.retiredSong()
}

// retiredSong merges the takes-less S7 into S3 through SQL and checks SONG_RETIRED on create.
func (w *world) retiredSong() string {
	w.t.Helper()
	s7, s3 := w.song("S7"), w.song("S3")
	if _, err := w.r.Store.W.Exec(`UPDATE songs SET retired_at = '2026-01-01T00:00:00.000Z', retired_into = ? WHERE id = ?`, s3, s7); err != nil {
		w.t.Fatal(err)
	}
	_, _, err := w.svc.Create(w.ctx, agent, crates.NewCrate{Name: "r", Kind: crates.KindManual, Items: []crates.Member{{SongID: s7}}})
	wantCode(w.t, "retired song on create", err, "SONG_RETIRED", 409)
	return s7
}

func TestCrates_UpdateRules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	smart := w.crate("Synthetic Unheard Reggae")
	manual := w.crate("Synthetic Remake Candidates")
	sp := func(s string) *string { return &s }
	yes := true

	c, n, err := w.svc.Update(w.ctx, agent, smart, crates.Patch{Revision: 1, Name: sp(" Renamed  smart ")})
	if err != nil || c.Name != "Renamed smart" || c.Revision != 2 || n != 2 || c.UpdatedBy != agent.Key() {
		t.Fatalf("rename: %+v %d %v", c, n, err)
	}
	_, _, err = w.svc.Update(w.ctx, agent, smart, crates.Patch{Revision: 1, Name: sp("again")})
	wantCode(t, "stale revision", err, "STALE_REVISION", 409)
	_, _, err = w.svc.Update(w.ctx, agent, smart, crates.Patch{Revision: 2})
	wantCode(t, "empty patch", err, "VALIDATION_FAILED", 422)
	_, _, err = w.svc.Update(w.ctx, agent, manual, crates.Patch{Revision: 1, Filter: json.RawMessage(`{"unheard":true}`)})
	wantCode(t, "filter on a manual crate", err, "VALIDATION_FAILED", 422)
	_, _, err = w.svc.Update(w.ctx, agent, smart, crates.Patch{Revision: 2, Filter: json.RawMessage(`{"nope":1}`)})
	wantCode(t, "bad filter", err, "UNKNOWN_FILTER_KEY", 422)
	_, _, err = w.svc.Update(w.ctx, agent, "missing", crates.Patch{Revision: 1, Name: sp("x")})
	wantCode(t, "missing crate", err, "NOT_FOUND", 404)

	// the live count follows the new filter
	c, n, err = w.svc.Update(w.ctx, agent, smart, crates.Patch{Revision: 2, Filter: json.RawMessage(`{"unheard":true}`)})
	if err != nil || c.Revision != 3 || n <= 2 || !strings.Contains(string(c.Filter), "unheard") {
		t.Errorf("new filter: %+v %d %v", c, n, err)
	}
	// kind and subject never change, hide and unhide
	c, _, err = w.svc.Update(w.ctx, human, manual, crates.Patch{Revision: 1, Hidden: &yes})
	if err != nil || c.HiddenAt == nil || c.Kind != crates.KindManual || c.Subject != crates.SubjectSong {
		t.Errorf("hide: %+v %v", c, err)
	}
	no := false
	if c, _, err = w.svc.Update(w.ctx, human, manual, crates.Patch{Revision: 2, Hidden: &no}); err != nil || c.HiddenAt != nil {
		t.Errorf("unhide: %+v %v", c, err)
	}
	if _, err := w.r.Store.W.Exec(`UPDATE crates SET kind = 'smart' WHERE id = ?`, manual); err == nil {
		t.Error("kind is frozen by the schema")
	}
}

func TestCrates_EditItems(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	id := w.crate("Synthetic Remake Candidates") // S1 S4 S6
	s1, s2, s3 := w.song("S1"), w.song("S2"), w.song("S3")

	r, err := w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{SongID: s1}, {SongID: s2}, {SongID: s2}}, Remove: []crates.Member{{SongID: s3}, {SongID: w.song("S4")}}})
	if err != nil || r != (crates.ItemsResult{Added: 1, Already: 1, Removed: 1, NotMember: 1, Count: 3}) {
		t.Fatalf("edit: %+v %v", r, err)
	}
	c, _, _ := w.svc.Get(w.ctx, id)
	if c.Revision != 1 {
		t.Errorf("item edits do not bump the revision: %d", c.Revision)
	}
	// re-adding a removed member creates a new row; the old one stays
	if r, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{SongID: w.song("S4")}}}); err != nil || r.Added != 1 || r.Count != 4 {
		t.Fatalf("re-add: %+v %v", r, err)
	}
	if n := w.str(`SELECT COUNT(*) FROM crate_items WHERE crate_id = ? AND song_id = ?`, id, w.song("S4")); n != "2" {
		t.Errorf("%s rows for S4, want 2 (one removed, one live)", n)
	}
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{TrackID: w.take("s1a")}}})
	wantCode(t, "subject mismatch", err, "SUBJECT_MISMATCH", 422)
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Remove: []crates.Member{{TrackID: w.take("s1a")}}})
	wantCode(t, "subject mismatch on remove", err, "SUBJECT_MISMATCH", 422)
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{SongID: "nope"}}})
	wantCode(t, "unknown song", err, "UNKNOWN_SONG", 422)
	_, err = w.svc.EditItems(w.ctx, agent, w.crate("Synthetic To Judge"), crates.ItemsEdit{Add: []crates.Member{{TrackID: "nope"}}})
	wantCode(t, "unknown take", err, "UNKNOWN_TRACK", 422)
	_, err = w.svc.EditItems(w.ctx, agent, w.crate("Synthetic Unheard Reggae"), crates.ItemsEdit{Add: []crates.Member{{SongID: s1}}})
	wantCode(t, "smart crate", err, "CRATE_IS_SMART", 409)
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: make([]crates.Member, 201)})
	wantCode(t, "too many adds", err, "VALIDATION_FAILED", 422)
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{})
	wantCode(t, "empty edit", err, "VALIDATION_FAILED", 422)
	_, err = w.svc.EditItems(w.ctx, agent, "missing", crates.ItemsEdit{Add: []crates.Member{{SongID: s1}}})
	wantCode(t, "missing crate", err, "NOT_FOUND", 404)

	// a retired Song cannot be added
	s2r := w.retiredSong()
	_, err = w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{SongID: s2r}}})
	wantCode(t, "retired song", err, "SONG_RETIRED", 409)

}

// TestCrates_CrateFull: the 5 000 member limit, on the row-only scale fixture.
func TestCrates_CrateFull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "full.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	librarytest.FixtureL2Scale(t, st, librarytest.FixedNow, crates.MaxManualItems+2, 1, 1)
	svc := crates.New(st, nil, func() time.Time { return librarytest.FixedNow }, nil)
	c, _, err := svc.Create(ctx, agent, crates.NewCrate{Name: "Full", Kind: crates.KindManual})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`INSERT INTO crate_items(id, crate_id, song_id, added_by, added_at)
		SELECT 'it-' || id, ?, id, 'human:h', '2026-01-01T00:00:00.000Z' FROM songs ORDER BY id LIMIT ?`, c.ID, crates.MaxManualItems-1); err != nil {
		t.Fatal(err)
	}
	var rest []string
	rows, err := st.R.Query(`SELECT id FROM songs WHERE id NOT IN (SELECT song_id FROM crate_items WHERE crate_id = ?) ORDER BY id LIMIT 2`, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		rest = append(rest, id)
	}
	rows.Close()
	if r, err := svc.EditItems(ctx, agent, c.ID, crates.ItemsEdit{Add: []crates.Member{{SongID: rest[0]}}}); err != nil || r.Count != crates.MaxManualItems {
		t.Fatalf("the 5000th member: %+v %v", r, err)
	}
	_, err = svc.EditItems(ctx, agent, c.ID, crates.ItemsEdit{Add: []crates.Member{{SongID: rest[1]}}})
	wantCode(t, "5001st member", err, "CRATE_FULL", 409)
	if r, err := svc.EditItems(ctx, agent, c.ID, crates.ItemsEdit{Add: []crates.Member{{SongID: rest[1]}}, Remove: []crates.Member{{SongID: rest[0]}}}); err != nil || r.Count != crates.MaxManualItems {
		t.Errorf("a removal makes room in the same call: %+v %v", r, err)
	}
}

func TestCrates_Expand_Merge(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	yes, no, d300 := true, false, 300.0
	smart := w.crate("Synthetic Unheard Reggae") // sound reggae, unheard, min 240 s, pick_decided false

	// equal values are fine, fields only one side sets are kept
	got, err := w.svc.ExpandSongs(w.ctx, smart, songs.ListFilter{Unheard: &yes, PickDecided: &no, Q: "S1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Q != "S1" || got.MinDurationS == nil || *got.MinDurationS != 240 {
		t.Errorf("merge lost a field: %+v", got)
	}
	if ids := w.listSongs(got); !slices.Equal(ids, []string{"S1"}) {
		t.Errorf("AND with q = %v", ids)
	}
	// a conflicting value names the field
	for field, base := range map[string]songs.ListFilter{
		"unheard": {Unheard: &no}, "pick_decided": {PickDecided: &yes}, "min_duration_s": {MinDurationS: &d300},
	} {
		_, err := w.svc.ExpandSongs(w.ctx, smart, base)
		c, st := code(err)
		var pe *problem.Error
		if !errors.As(err, &pe) || c != "FILTER_CONFLICT" || st != 409 || pe.Details["field"] != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	// list fields intersect; an empty intersection matches nothing
	_, _, err = w.svc.Create(w.ctx, agent, crates.NewCrate{Name: "Lists", Kind: crates.KindSmart, Filter: json.RawMessage(`{"state":["rendered","judged"],"sound":["genre_family:reggae","genre_family:dance"],"tag":["a","b"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	lc := w.crate("Lists")
	got, err = w.svc.ExpandSongs(w.ctx, lc, songs.ListFilter{States: []songs.State{"judged", "picked"}, Sounds: map[string][]string{"genre_family": {"dance", "dub"}}, Tags: []string{"b", "c"}})
	if err != nil || !slices.Equal(got.States, []songs.State{"judged"}) || !slices.Equal(got.Sounds["genre_family"], []string{"dance"}) || !slices.Equal(got.Tags, []string{"b"}) || got.None {
		t.Errorf("intersection: %+v %v", got, err)
	}
	got, err = w.svc.ExpandSongs(w.ctx, lc, songs.ListFilter{States: []songs.State{"picked"}})
	if err != nil || !got.None {
		t.Errorf("empty intersection must match nothing: %+v %v", got, err)
	}
	if ids := w.listSongs(got); len(ids) != 0 {
		t.Errorf("an empty intersection matched %v", ids)
	}
	// an absent request list is unset; the other side's list is kept
	got, _ = w.svc.ExpandSongs(w.ctx, lc, songs.ListFilter{})
	if len(got.States) != 2 || got.None {
		t.Errorf("unset base list: %+v", got)
	}

	// a Crate of the other subject is refused; unknown ones are 404; a hidden Crate is usable
	_, err = w.svc.ExpandTracks(w.ctx, smart, library.SearchParams{})
	wantCode(t, "subject mismatch", err, "SUBJECT_MISMATCH", 422)
	_, err = w.svc.ExpandSongs(w.ctx, "missing", songs.ListFilter{})
	wantCode(t, "missing crate", err, "NOT_FOUND", 404)
	if _, _, err := w.svc.Update(w.ctx, human, smart, crates.Patch{Revision: 1, Hidden: &yes}); err != nil {
		t.Fatal(err)
	}
	if exp, err := w.svc.ExpandSongs(w.ctx, smart, songs.ListFilter{}); err != nil || !slices.Equal(w.listSongs(exp), []string{"S1", "S6"}) {
		t.Errorf("hidden smart crate: %v", err)
	}

	// Take crates merge the same way
	tcrate := w.crate("Synthetic Unheard Drafts") // status draft, unheard
	tp, err := w.svc.ExpandTracks(w.ctx, tcrate, library.SearchParams{Status: []string{"draft", "final"}, Unheard: &yes})
	if err != nil || !slices.Equal(tp.Status, []string{"draft"}) {
		t.Errorf("take intersect: %+v %v", tp, err)
	}
	_, err = w.svc.ExpandTracks(w.ctx, tcrate, library.SearchParams{Unheard: &no})
	if c, _ := code(err); c != "FILTER_CONFLICT" {
		t.Errorf("take conflict: %v", err)
	}
	tp, _ = w.svc.ExpandTracks(w.ctx, tcrate, library.SearchParams{Status: []string{"final"}})
	if !tp.None {
		t.Error("an empty status intersection matches nothing")
	}

	// a filter naming a deleted Muse matches nothing and warns
	gone, _, err := w.svc.Create(w.ctx, agent, crates.NewCrate{Name: "Gone", Kind: crates.KindSmart, Filter: json.RawMessage(`{"muse_id":"01900000-dead-7000-8000-000000000000","session_id":"nope"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, n, _ := w.svc.Get(w.ctx, gone.ID); n != 0 {
		t.Errorf("count of a deleted-id filter = %d", n)
	}
	warn, err := w.svc.Warnings(w.ctx, gone)
	if err != nil || len(warn) != 2 {
		t.Errorf("warnings = %v %v", warn, err)
	}
	if warn, _ := w.svc.Warnings(w.ctx, mustGet(t, w, smart)); len(warn) != 0 {
		t.Errorf("a healthy filter warns: %v", warn)
	}
}

func mustGet(t *testing.T, w *world, id string) crates.Crate {
	t.Helper()
	c, _, err := w.svc.Get(w.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCrates_RetiredSongFollowed(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	id := w.crate("Synthetic Remake Candidates") // S1 S4 S6
	s7, s2 := w.song("S7"), w.song("S2")
	if _, err := w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{SongID: s7}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.r.Store.W.Exec(`UPDATE songs SET retired_at = '2026-01-01T00:00:00.000Z', retired_into = ? WHERE id = ?`, s2, s7); err != nil {
		t.Fatal(err)
	}
	exp, err := w.svc.ExpandSongs(w.ctx, id, songs.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.listSongs(exp); !slices.Equal(got, []string{"S1", "S2", "S4", "S6"}) {
		t.Errorf("the retired member is followed to its successor: %v", got)
	}
	// removing the retired member by its own id still works
	if r, err := w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Remove: []crates.Member{{SongID: s7}}}); err != nil || r.Removed != 1 {
		t.Errorf("remove retired: %+v %v", r, err)
	}
	if got := w.listSongs(exp); !slices.Equal(got, []string{"S1", "S4", "S6"}) {
		t.Errorf("after the removal: %v", got)
	}
}

func TestCrates_CountsOnRead(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p, err := w.svc.List(w.ctx, crates.ListFilter{})
	if err != nil || p.Total != 4 || len(p.Items) != 4 {
		t.Fatalf("list: %+v %v", p, err)
	}
	counts := map[string]*int{}
	for _, it := range p.Items {
		counts[it.Name] = it.Count
	}
	if c := counts["Synthetic Remake Candidates"]; c == nil || *c != 3 {
		t.Errorf("manual count = %v", c)
	}
	if c := counts["Synthetic To Judge"]; c == nil || *c != 4 {
		t.Errorf("manual take count = %v", c)
	}
	if counts["Synthetic Unheard Reggae"] != nil || counts["Synthetic Unheard Drafts"] != nil {
		t.Error("smart counts are computed only with counts=true")
	}
	p, err = w.svc.List(w.ctx, crates.ListFilter{Counts: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Count == nil {
			t.Errorf("%s: no count with Counts", it.Name)
		}
		if it.Name == "Synthetic Unheard Reggae" && *it.Count != 2 {
			t.Errorf("smart song count = %d", *it.Count)
		}
	}
	// the count is read live, never stored
	if _, err := w.svc.EditItems(w.ctx, agent, w.crate("Synthetic Remake Candidates"), crates.ItemsEdit{Add: []crates.Member{{SongID: w.song("S2")}}}); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := w.svc.Get(w.ctx, w.crate("Synthetic Remake Candidates")); n != 4 {
		t.Errorf("count after add = %d", n)
	}
	// filters and paging
	if p, _ = w.svc.List(w.ctx, crates.ListFilter{Kind: crates.KindSmart}); p.Total != 2 {
		t.Errorf("kind smart: %d", p.Total)
	}
	if p, _ = w.svc.List(w.ctx, crates.ListFilter{Subject: crates.SubjectTake}); p.Total != 2 {
		t.Errorf("subject take: %d", p.Total)
	}
	if p, _ = w.svc.List(w.ctx, crates.ListFilter{Q: "judge"}); p.Total != 1 {
		t.Errorf("q: %d", p.Total)
	}
	var seen []string
	cur := ""
	for {
		p, err = w.svc.List(w.ctx, crates.ListFilter{Limit: 1, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		if p.NextCursor == nil {
			break
		}
		cur = *p.NextCursor
	}
	if len(seen) != 4 {
		t.Errorf("paging saw %d crates", len(seen))
	}
	_, err = w.svc.List(w.ctx, crates.ListFilter{Cursor: "!!"})
	wantCode(t, "bad cursor", err, "VALIDATION_FAILED", 422)
	hidden := true
	if _, _, err := w.svc.Update(w.ctx, human, w.crate("Synthetic To Judge"), crates.Patch{Revision: 1, Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	if p, _ = w.svc.List(w.ctx, crates.ListFilter{}); p.Total != 3 {
		t.Errorf("hidden crates are listed: %d", p.Total)
	}
	if p, _ = w.svc.List(w.ctx, crates.ListFilter{IncludeHidden: true}); p.Total != 4 {
		t.Errorf("include_hidden: %d", p.Total)
	}
}

// TestParseFilter_Table covers value shapes the create rules do not.
func TestParseFilter_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		subject crates.Subject
		raw     string
		ok      bool
	}{
		{crates.SubjectSong, `{}`, true},
		{crates.SubjectSong, `{"min_stars":5,"tag":["a","b"],"min_duration_s":0}`, true},
		{crates.SubjectSong, `{"min_stars":2.5}`, false},
		{crates.SubjectSong, `{"min_duration_s":-1}`, false},
		{crates.SubjectSong, `{"tag":["1","2","3","4","5","6"]}`, false},
		{crates.SubjectTake, `{"recorded_from":"2026-01-02","recorded_to":"2026-01-03T00:00:00Z"}`, true},
		{crates.SubjectTake, `{"recorded_from":"yesterday"}`, false},
		{crates.SubjectTake, `{"sound":["genre_family:reggae","mood:calm"],"is_pick":false,"unrated":true}`, true},
		{crates.SubjectTake, `{"min_stars":2,"unrated":true}`, false},
		{crates.SubjectTake, `{"status":["bogus"]}`, false},
		{crates.SubjectTake, `{"q":"a","q":"b"}`, true},
		{"muse", `{}`, false},
	} {
		_, err := crates.ParseFilter(tc.subject, json.RawMessage(tc.raw))
		if (err == nil) != tc.ok {
			t.Errorf("%s %s: %v", tc.subject, tc.raw, err)
		}
	}
	f, err := crates.ParseFilter(crates.SubjectSong, json.RawMessage(`{"has_pick":false,"pick_decided":true,"min_stars":3}`))
	if err != nil || f.Subject() != crates.SubjectSong || *f.Songs().HasPick || !*f.Songs().PickDecided || *f.Songs().MinStars != 3 {
		t.Errorf("mapping: %+v %v", f, err)
	}
}

// TestCrates_TakeCrates covers the Take subject: item edits, the membership expansion and the warnings of a smart Take crate.
func TestCrates_TakeCrates(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	id := w.crate("Synthetic To Judge") // s1a b1 c2 s6a
	r, err := w.svc.EditItems(w.ctx, agent, id, crates.ItemsEdit{Add: []crates.Member{{TrackID: w.take("a1")}, {TrackID: w.take("s1a")}}, Remove: []crates.Member{{TrackID: w.take("b1")}, {TrackID: w.take("b2")}}})
	if err != nil || r != (crates.ItemsResult{Added: 1, Already: 1, Removed: 1, NotMember: 1, Count: 4}) {
		t.Fatalf("edit: %+v %v", r, err)
	}
	p, err := w.svc.ExpandTracks(w.ctx, id, library.SearchParams{Q: "keep"})
	if err != nil || p.CrateID != id || p.Q != "keep" {
		t.Errorf("manual expansion: %+v %v", p, err)
	}
	_, err = w.svc.ExpandSongs(w.ctx, id, songs.ListFilter{})
	wantCode(t, "take crate on songs", err, "SUBJECT_MISMATCH", 422)
	gone, _, err := w.svc.Create(w.ctx, agent, crates.NewCrate{Name: "Gone takes", Kind: crates.KindSmart, Subject: crates.SubjectTake, Filter: json.RawMessage(`{"session_id":"nope"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if warn, err := w.svc.Warnings(w.ctx, gone); err != nil || len(warn) != 1 {
		t.Errorf("warnings: %v %v", warn, err)
	}
	if warn, err := w.svc.Warnings(w.ctx, mustGet(t, w, id)); err != nil || len(warn) != 0 {
		t.Errorf("a manual crate has no warnings: %v %v", warn, err)
	}
	// a Service without a Counter cannot count a smart Crate but still counts a manual one
	bare := crates.New(w.r.Store, nil, nil, nil)
	if _, _, err := bare.Get(w.ctx, gone.ID); err == nil {
		t.Error("a smart count needs a Counter")
	}
	if _, n, err := bare.Get(w.ctx, id); err != nil || n != 4 {
		t.Errorf("manual count without a Counter: %d %v", n, err)
	}
	if c, _, err := bare.Create(w.ctx, human, crates.NewCrate{Name: "Bare", Kind: crates.KindManual}); err != nil || c.ID == "" {
		t.Errorf("default id generator: %+v %v", c, err)
	}
}
