package library_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	st  *store.Store
	svc *library.Service
	fx  lt.Fixtures
	clk *clock
	dir string
}

func newEnv(t *testing.T) *env { return newEnvWith(t, lt.FixtureV2) }

// newEnvM2a is the standard fixture with the M2a rows (ratings, tags, comments...).
func newEnvM2a(t *testing.T) *env { return newEnvWith(t, lt.Fixture) }

func newEnvWith(t *testing.T, fixture func(testing.TB, *store.Store, time.Time) lt.Fixtures) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, clk: &clock{t: lt.FixedNow}, dir: t.TempDir()}
	e.fx = fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, e.dir)
	root, err := os.OpenRoot(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e.svc = library.New(st, root, e.clk.Now)
	return e
}

func ids(p library.TrackPage) []string {
	out := []string{}
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

func search(t *testing.T, e *env, p library.SearchParams) []string {
	t.Helper()
	page, err := e.svc.SearchTracks(context.Background(), p)
	if err != nil {
		t.Fatalf("%+v: %v", p, err)
	}
	return ids(page)
}

func sameSet(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if diff := cmp.Diff(w, g, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("%s (-want +got):\n%s", name, diff)
	}
}

func fp(f float64) *float64 { return &f }
func bp(b bool) *bool       { return &b }

func TestFixture_Deterministic(t *testing.T) {
	t.Parallel()
	a, b := newEnv(t), newEnv(t)
	if diff := cmp.Diff(a.fx, b.fx); diff != "" {
		t.Fatalf("fixture ids differ between calls:\n%s", diff)
	}
	if len(a.fx.Tracks) != 5 || len(a.fx.Drafts) != 6 || len(a.fx.Recipes) != 11 || len(a.fx.Runs) != 1 || len(a.fx.Devices) != 1 {
		t.Fatalf("fixture shape %+v", a.fx)
	}
	da, _ := a.svc.Track(context.Background(), lt.TrackLuna)
	db, _ := b.svc.Track(context.Background(), lt.TrackLuna)
	if diff := cmp.Diff(da, db); diff != "" {
		t.Fatalf("track detail differs:\n%s", diff)
	}
}

func TestSearchTracks_FTS(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sameSet(t, "luna", search(t, e, library.SearchParams{Q: "luna"}), lt.TrackLuna)
	sameSet(t, "prefix lun", search(t, e, library.SearchParams{Q: "lun"}), lt.TrackLuna)
	sameSet(t, "accent title", search(t, e, library.SearchParams{Q: "citta"}), lt.TrackCitta)
	sameSet(t, "accent query", search(t, e, library.SearchParams{Q: "Città"}), lt.TrackCitta)
	sameSet(t, "lyrics", search(t, e, library.SearchParams{Q: "sera"}), lt.TrackCitta)
	sameSet(t, "style", search(t, e, library.SearchParams{Q: "guitar"}), lt.TrackTwinA, lt.TrackTwinB)
	sameSet(t, "request id", search(t, e, library.SearchParams{Q: "fx-short"}), lt.TrackShort)
	if a, b := search(t, e, library.SearchParams{Q: "è"}), search(t, e, library.SearchParams{Q: "e"}); !slices.Equal(a, b) {
		t.Errorf("è and e must fold to the same query: %v vs %v", a, b)
	}
	sameSet(t, "AND is a word", search(t, e, library.SearchParams{Q: "luna AND park"}))
	for _, q := range []string{`"`, `"unterminated`, `*`, `(`, `)`, `NEAR`, `NEAR(a b)`, `-x`, `AND`, `OR`, `NOT`, `a:b`, `^x`, `title:luna`, `{x}`, "'", "luna OR", strings.Repeat("a ", 50), "---", ""} {
		if _, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Q: q}); err != nil {
			t.Errorf("q=%q: %v", q, err)
		}
	}
	// a query of only punctuation behaves like no query
	if got := search(t, e, library.SearchParams{Q: "---"}); len(got) != 5 {
		t.Errorf("punctuation-only query: %v", got)
	}
}

func TestSearchTracks_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	all := e.fx.Tracks
	tests := []struct {
		name string
		p    library.SearchParams
		want []string
	}{
		{"status draft", library.SearchParams{Status: []string{"draft"}}, all},
		{"status final", library.SearchParams{Status: []string{"final"}}, nil},
		{"status either", library.SearchParams{Status: []string{"final", "draft"}}, all},
		{"style ambient", library.SearchParams{Style: []string{"ambient"}}, []string{lt.TrackCitta, lt.TrackShort}},
		{"style AND", library.SearchParams{Style: []string{"ambient", "drone"}}, []string{lt.TrackShort}},
		{"style multiword", library.SearchParams{Style: []string{"Dream Pop"}}, []string{lt.TrackLuna}},
		{"min duration", library.SearchParams{MinDurationS: fp(8)}, []string{lt.TrackLuna, lt.TrackTwinA, lt.TrackTwinB}},
		{"max duration", library.SearchParams{MaxDurationS: fp(6)}, []string{lt.TrackCitta, lt.TrackShort}},
		{"recorded from date", library.SearchParams{RecordedFrom: "2026-10-03"}, []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort}},
		{"recorded from datetime", library.SearchParams{RecordedFrom: "2026-10-03T12:00:01Z"}, []string{lt.TrackTwinB, lt.TrackShort}},
		{"recorded to date is inclusive", library.SearchParams{RecordedTo: "2026-10-02"}, []string{lt.TrackLuna, lt.TrackCitta}},
		{"has score", library.SearchParams{HasScore: bp(true)}, []string{lt.TrackLuna, lt.TrackShort}},
		{"no score", library.SearchParams{HasScore: bp(false)}, []string{lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB}},
		{"truncated", library.SearchParams{Truncated: bp(true)}, []string{lt.TrackShort}},
		{"not truncated", library.SearchParams{Truncated: bp(false)}, []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB}},
		{"root", library.SearchParams{RootTrackID: lt.TrackTwinA}, []string{lt.TrackTwinA, lt.TrackTwinB}},
		{"combined", library.SearchParams{Status: []string{"draft"}, Style: []string{"rock"}, MinDurationS: fp(8), HasScore: bp(false), Truncated: bp(false)}, []string{lt.TrackTwinA, lt.TrackTwinB}},
		{"combined with q", library.SearchParams{Q: "twin", Style: []string{"rock"}, RecordedFrom: "2026-10-04"}, []string{lt.TrackTwinB}},
	}
	for _, tc := range tests {
		sameSet(t, tc.name, search(t, e, tc.p), tc.want...)
	}
	for _, bad := range []library.SearchParams{
		{Status: []string{"bogus"}}, {Sort: "bogus"}, {RecordedFrom: "yesterday"}, {RecordedTo: "x"}, {Cursor: "!!"},
		{Style: []string{"a", "b", "c", "d", "e", "f"}},
	} {
		var ve *library.ValidationError
		if _, err := e.svc.SearchTracks(context.Background(), bad); !errors.As(err, &ve) {
			t.Errorf("%+v: err = %v, want ValidationError", bad, err)
		}
	}
}

func TestSearchTracks_SortAndCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := func(sort string) []string { return search(t, e, library.SearchParams{Sort: sort}) }
	want := map[string][]string{
		"recorded_desc": {lt.TrackShort, lt.TrackTwinB, lt.TrackTwinA, lt.TrackCitta, lt.TrackLuna},
		"recorded_asc":  {lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort},
		"duration_desc": {lt.TrackLuna, lt.TrackTwinB, lt.TrackTwinA, lt.TrackShort, lt.TrackCitta},
		"duration_asc":  {lt.TrackCitta, lt.TrackShort, lt.TrackTwinA, lt.TrackTwinB, lt.TrackLuna},
		"title_asc":     {lt.TrackCitta, lt.TrackShort, lt.TrackLuna, lt.TrackTwinA, lt.TrackTwinB},
	}
	for s, w := range want {
		if diff := cmp.Diff(w, order(s)); diff != "" {
			t.Errorf("sort %s (-want +got):\n%s", s, diff)
		}
	}
	if diff := cmp.Diff(want["recorded_desc"], order("")); diff != "" {
		t.Errorf("default sort: %s", diff)
	}
	// relevance without q falls back; with q the best match comes first
	if diff := cmp.Diff(want["recorded_desc"], order("relevance")); diff != "" {
		t.Errorf("relevance without q: %s", diff)
	}
	if got := search(t, e, library.SearchParams{Q: "twin"}); len(got) != 2 {
		t.Errorf("relevance: %v", got)
	}

	// paging with limit 2 through 5 tracks, no duplicate, no miss
	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 5 {
			t.Fatalf("total = %d", page.Total)
		}
		seen = append(seen, ids(page)...)
		pages++
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if pages != 3 || !slices.Equal(seen, want["recorded_desc"]) {
		t.Errorf("pages=%d seen=%v", pages, seen)
	}
}

// addTrack inserts one synthetic track with the given style and returns its id.
func addTrack(t *testing.T, e *env, n int, style string) string {
	t.Helper()
	ctx := context.Background()
	raw := fmt.Sprintf(`{"id":"bulk-%03d","style":%q,"lyrics":"[Verse]\nline %d","cot":"full","seed":%d}`, n, style, n, 5000+n)
	p, err := yue2.ParseRequest([]byte(raw), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rid, _, err := e.svc.InsertRecipe(ctx, tx, p, "import")
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x9000+n)
	err = e.svc.InsertTake(ctx, tx, newTake(id, rid, n))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func newTake(id, rid string, n int) library.NewTake {
	bw := 16000
	return library.NewTake{
		ID: id, Title: fmt.Sprintf("Bulk %03d", n), Slug: fmt.Sprintf("bulk_%03d", n), RecipeID: rid,
		DurationS: 5, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 240000,
		MasterPath: fmt.Sprintf("audio/masters/bulk_%03d.flac", n), MasterSHA256: lt.Sha(fmt.Sprintf("m%d", n)), MasterBytes: 10,
		SourceSHA256: lt.Sha(fmt.Sprintf("s%d", n)), FramesSHA256: lt.Sha(fmt.Sprintf("f%d", n)), AudioMD5: strings.Repeat("0", 32),
		ImportRule: "slug", PrincipalID: "cli", RecordedAt: lt.FixedNow.Add(-time.Duration(n) * time.Minute),
		Loudness: analysis.Loudness{IntegratedLUFS: -14}, Spectrum: analysis.Spectrum{BandwidthHz: &bw}, ToolVersion: "ffmpeg n9.0.1",
	}
}

func TestSearchTracks_PageCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var added []string
	for i := 0; i < 100; i++ {
		terms := make([]string, 20)
		for j := range terms {
			terms[j] = fmt.Sprintf("t%03d%02d", i, j) + strings.Repeat("x", 40-6)
		}
		added = append(added, addTrack(t, e, i, strings.Join(terms, ", ")))
	}
	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Limit: 100, Cursor: cursor, Status: []string{"draft"}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(page)
		if len(b) >= 90000 {
			t.Fatalf("page %d is %d bytes", pages, len(b))
		}
		seen = append(seen, ids(page)...)
		pages++
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if pages < 2 {
		t.Fatalf("expected the byte cap to split the result, got %d page(s)", pages)
	}
	want := append(slices.Clone(e.fx.Tracks), added...)
	sameSet(t, "all pages", seen, want...)
	if len(seen) != len(want) {
		t.Errorf("duplicates across pages: %d vs %d", len(seen), len(want))
	}
}

func TestTrackDetail_SectionsTimedOnlyFromScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d, err := e.svc.Track(ctx, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Timed || len(d.Sections) != 3 || d.Score == nil || !d.Score.Aligned {
		t.Fatalf("aligned track: timed=%v sections=%d score=%+v", d.Timed, len(d.Sections), d.Score)
	}
	for i, s := range d.Sections {
		if s.StartS == nil || s.EndS == nil || s.Source != "plan" || s.Confidence == nil || *s.Confidence != 0.7 {
			t.Errorf("section %d: %+v", i, s)
		}
	}
	if *d.Sections[0].StartS != 0 || *d.Sections[0].EndS != 4 || *d.Sections[2].EndS != 12 {
		t.Errorf("times: %v %v %v", *d.Sections[0].StartS, *d.Sections[0].EndS, *d.Sections[2].EndS)
	}
	for _, id := range []string{lt.TrackCitta, lt.TrackShort} { // no score / untimed score
		d, err := e.svc.Track(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Timed || len(d.Sections) == 0 {
			t.Errorf("%s: timed=%v sections=%d", id, d.Timed, len(d.Sections))
		}
		for _, s := range d.Sections {
			if s.StartS != nil || s.EndS != nil || s.Source != "lyrics" || s.Confidence != nil {
				t.Errorf("%s: untimed section carries times: %+v", id, s)
			}
		}
	}
	if d, _ := e.svc.Track(ctx, lt.TrackShort); d.Score == nil || d.Score.Aligned || d.Score.SecondsPerBar != nil {
		t.Errorf("untimed score: %+v", d.Score)
	}
	if d, _ := e.svc.Track(ctx, lt.TrackCitta); d.Score != nil {
		t.Errorf("no score expected: %+v", d.Score)
	}
	// lineage
	b, _ := e.svc.Track(ctx, lt.TrackTwinB)
	if b.Lineage.RootTrackID != lt.TrackTwinA || b.Lineage.ParentTrackID == nil || *b.Lineage.ParentTrackID != lt.TrackTwinA ||
		b.Lineage.Operator == nil || *b.Lineage.Operator != "inferred_sibling" || b.Lineage.Rule == nil || *b.Lineage.Rule != "same_request_id" ||
		len(b.Lineage.Siblings) != 1 || b.Lineage.Siblings[0].ID != lt.TrackTwinA {
		t.Errorf("lineage: %+v", b.Lineage)
	}
	if _, err := e.svc.Track(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	if s, _ := e.svc.SearchTracks(ctx, library.SearchParams{Q: "twin"}); len(s.Items) == 2 {
		for _, it := range s.Items {
			if it.ID == lt.TrackTwinA && it.ChildrenCount != 1 {
				t.Errorf("children_count = %d", it.ChildrenCount)
			}
		}
	}
}

func TestTrackDetail_Golden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d, err := e.svc.Track(context.Background(), lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(d, "", "  ")
	got = append(got, '\n')
	path := filepath.Join("testdata", "track_detail.golden.json")
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
		t.Errorf("track detail drift (-want +got):\n%s", diff)
	}
}

func listDrafts(t *testing.T, e *env, p library.DraftParams) library.DraftPage {
	t.Helper()
	page, err := e.svc.ListDrafts(context.Background(), p)
	if err != nil {
		t.Fatalf("%+v: %v", p, err)
	}
	return page
}

func TestListDrafts_DefaultBacklogAndOrdering(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	p := listDrafts(t, e, library.DraftParams{})
	if p.Total != 6 || p.GroupsTotal != 2 || len(p.Items) != 6 || p.NextCursor != nil {
		t.Fatalf("page %+v", p)
	}
	var reqs []string
	for _, d := range p.Items {
		if d.Status != "backlog" {
			t.Errorf("non-backlog draft %+v", d)
		}
		reqs = append(reqs, d.RequestID)
	}
	if !slices.IsSorted(reqs) || reqs[0] != "fxb_001_v1" || reqs[5] != "fxb_002_v3" {
		t.Errorf("order %v", reqs)
	}
	first := p.Items[0]
	if first.GroupKey == nil || *first.GroupKey != "fxb_001" || first.Label == nil || *first.Label != "v1" || first.Seed != 201 || first.Cot != "full" ||
		!slices.Equal(first.Collections, []string{"pilot-x"}) || !slices.Equal(first.SectionKinds, []string{"verse", "chorus", "bridge"}) ||
		!slices.Equal(first.StyleTerms, []string{"synth pop"}) {
		t.Errorf("first draft %+v", first)
	}
	// paging keeps order
	var seen []string
	cur := ""
	for {
		pg := listDrafts(t, e, library.DraftParams{Limit: 4, Cursor: cur})
		for _, d := range pg.Items {
			seen = append(seen, d.RequestID)
		}
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if !slices.Equal(seen, reqs) {
		t.Errorf("paged %v want %v", seen, reqs)
	}
}

func TestListDrafts_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if p := listDrafts(t, e, library.DraftParams{Collection: "pilot-x"}); p.Total != 3 || p.GroupsTotal != 1 {
		t.Errorf("collection: %+v", p)
	}
	if p := listDrafts(t, e, library.DraftParams{GroupKey: "fxb_002"}); p.Total != 3 {
		t.Errorf("group: %+v", p)
	}
	if p := listDrafts(t, e, library.DraftParams{Origin: "varianti_import"}); p.Total != 0 {
		t.Errorf("origin: %+v", p)
	}
	if p := listDrafts(t, e, library.DraftParams{Q: "synth"}); p.Total != 6 {
		t.Errorf("q: %+v", p)
	}
	if p := listDrafts(t, e, library.DraftParams{Q: "fxb_002"}); p.Total != 3 {
		t.Errorf("q request id: %+v", p)
	}
	if p := listDrafts(t, e, library.DraftParams{Status: "fulfilled"}); p.Total != 0 {
		t.Errorf("fulfilled: %+v", p)
	}
	var ve *library.ValidationError
	if _, err := e.svc.ListDrafts(context.Background(), library.DraftParams{Status: "bogus"}); !errors.As(err, &ve) {
		t.Errorf("status err %v", err)
	}
}

func TestListDrafts_FulfilledExcluded(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// A take of the recipe behind fxb_001_v1 fulfils its draft.
	d0 := draftRecipe(t, e, e.fx.Drafts[0])
	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	tk := newTake(lt.TrackIDForTest(1), d0, 1)
	if err := e.svc.InsertTake(ctx, tx, tk); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if p := listDrafts(t, e, library.DraftParams{}); p.Total != 5 {
		t.Errorf("backlog after fulfilment: %d", p.Total)
	}
	p := listDrafts(t, e, library.DraftParams{Status: "fulfilled"})
	if p.Total != 1 || p.Items[0].FulfilledByTrackID == nil || *p.Items[0].FulfilledByTrackID != tk.ID {
		t.Errorf("fulfilled: %+v", p)
	}
}

func draftRecipe(t *testing.T, e *env, draftID string) string {
	t.Helper()
	var rid string
	if err := e.st.R.QueryRow(`SELECT source_recipe_id FROM recipe_drafts WHERE id = ?`, draftID).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	return rid
}

func TestGetDraft_ReferenceHumanOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	h, err := e.svc.Draft(ctx, e.fx.Drafts[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasReference || h.Reference["variant_type"] != "slower" {
		t.Errorf("human: %+v", h.Reference)
	}
	a, err := e.svc.Draft(ctx, e.fx.Drafts[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if !a.HasReference || a.Reference != nil {
		t.Errorf("agent: has=%v ref=%v", a.HasReference, a.Reference)
	}
	// the reference never leaks into the request or any other field
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), "Synthetic Reference Title") || strings.Contains(string(b), "slower") {
		t.Errorf("reference leaked into agent view: %s", b)
	}
	req, _ := json.Marshal(h.Request)
	if strings.Contains(string(req), "Synthetic") {
		t.Errorf("reference inside request: %s", req)
	}
	// a draft without reference
	n, _ := e.svc.Draft(ctx, e.fx.Drafts[1], true)
	if n.HasReference || n.Reference != nil {
		t.Errorf("no reference expected: %+v", n)
	}
	if h.Source == nil || h.Source.RecipeHash == "" || len(h.Source.Sources) != 1 || h.Source.Sources[0].Path != "music/backlog-variants.jsonl" ||
		len(h.Group) != 3 || len(h.Sections) != 3 || !slices.Equal(h.Collections, []string{"pilot-x"}) || h.Request.Seed != 201 {
		t.Errorf("detail: %+v", h)
	}
	if _, err := e.svc.Draft(ctx, "nope", false); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown draft: %v", err)
	}
}

func TestGetStats_RealCounts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Tracks.Total != 5 || s.Tracks.ByStatus["draft"] != 5 || len(s.Tracks.ByStatus) != 1 || s.Tracks.ByOrigin["import"] != 5 ||
		s.Tracks.WithScore != 2 || s.Tracks.Timed != 1 || s.Tracks.Truncated != 1 || s.Tracks.DurationS != 40 {
		t.Errorf("tracks %+v", s.Tracks)
	}
	if s.Backlog.Drafts != 6 || s.Backlog.Fulfilled != 0 || s.Backlog.Groups != 2 ||
		!slices.Equal(s.Backlog.Collections, []library.CollectionCount{{Name: "pilot-x", Drafts: 3}}) {
		t.Errorf("backlog %+v", s.Backlog)
	}
	if s.Recipes.Total != 11 || s.Recipes.Sources != 11 {
		t.Errorf("recipes %+v", s.Recipes)
	}
	l := s.Loudness
	if l.N != 5 || *l.IntegratedLUFSMin != -16 || *l.IntegratedLUFSMax != -12 || *l.SpreadLU != 4 || !l.ListenFlacRecommended {
		t.Errorf("loudness %+v", l)
	}
	b := s.Bandwidth
	if b.N != 5 || *b.MinHz != 8000 || *b.MedianHz != 16000 || *b.MaxHz != 20000 {
		t.Errorf("bandwidth %+v", b)
	}
	if s.Storage.MastersBytes == 0 || s.Storage.DerivedBytes == 0 {
		t.Errorf("storage %+v", s.Storage)
	}
	// two tracks sharing one content-addressed rendition count it once
	var sha string
	var nb int64
	if err := e.st.W.QueryRow(`SELECT sha256, bytes FROM renditions WHERE kind='peaks' LIMIT 1`).Scan(&sha, &nb); err != nil {
		t.Fatal(err)
	}
	before := s.Storage.DerivedBytes
	if _, err := e.st.W.Exec(`INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at)
		SELECT id, 'shared_probe', ?, ?, 'derived/shared.bin', '{}', '2026-10-06T12:00:00Z' FROM tracks LIMIT 2`, sha, nb); err != nil {
		t.Fatal(err)
	}
	s2, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s2.Storage.DerivedBytes != before {
		t.Errorf("shared rendition double counted: %d -> %d", before, s2.Storage.DerivedBytes)
	}
	if s.Imports.Runs != 1 || s.Imports.LastState == nil || *s.Imports.LastState != "completed" {
		t.Errorf("imports %+v", s.Imports)
	}
	// empty library: nulls, not zeros
	st, _ := store.Open(context.Background(), filepath.Join(t.TempDir(), "e.db"))
	defer st.Close()
	es, err := library.New(st, nil, nil).Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if es.Tracks.Total != 0 || es.Loudness.SpreadLU != nil || es.Loudness.ListenFlacRecommended || es.Bandwidth.MedianHz != nil || es.Imports.LastRunAt != nil {
		t.Errorf("empty %+v", es)
	}
}

func TestDeviceProfile_PutUpsert(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	in := library.DeviceProfileIn{DeviceID: "phone-abcdef01", Label: "Phone", UserAgent: "UA/1", Quality: "auto", QualitySource: "default", ProbeResult: "none", ProbeVersion: 1}
	p1 := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}
	p2 := auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "agent"}
	d1, err := e.svc.PutDeviceProfile(ctx, p1, in)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Add(time.Hour)
	in.Quality, in.QualitySource, in.ProbeResult, in.FlacCanPlay = "high", "probe", "failed", "maybe"
	in.FlacDecodeOK = bp(false)
	d2, err := e.svc.PutDeviceProfile(ctx, p2, in)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var createdBy, updatedBy, createdAt, updatedAt string
	if err := e.st.R.QueryRow(`SELECT COUNT(*), MAX(created_by), MAX(updated_by), MAX(created_at), MAX(updated_at) FROM device_profiles WHERE device_id = ?`, in.DeviceID).
		Scan(&n, &createdBy, &updatedBy, &createdAt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if n != 1 || createdBy != "alice" || updatedBy != "tok-1" || createdAt != d1.UpdatedAt || updatedAt == createdAt || updatedAt != d2.UpdatedAt {
		t.Errorf("rows=%d createdBy=%s updatedBy=%s createdAt=%s updatedAt=%s", n, createdBy, updatedBy, createdAt, updatedAt)
	}
	if d1.EffectiveFormat != "flac" || d2.EffectiveFormat != "mp3" || d2.FlacDecodeOK == nil || *d2.FlacDecodeOK || d2.FlacSeekOK != nil {
		t.Errorf("d1=%+v d2=%+v", d1, d2)
	}
	list, err := e.svc.DeviceProfiles(ctx, 0, "")
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	for name, mut := range map[string]func(*library.DeviceProfileIn){
		"short id":    func(i *library.DeviceProfileIn) { i.DeviceID = "short" },
		"bad id char": func(i *library.DeviceProfileIn) { i.DeviceID = "bad id with space" },
		"quality":     func(i *library.DeviceProfileIn) { i.Quality = "ultra" },
		"source":      func(i *library.DeviceProfileIn) { i.QualitySource = "x" },
		"probe":       func(i *library.DeviceProfileIn) { i.ProbeResult = "x" },
		"can play":    func(i *library.DeviceProfileIn) { i.FlacCanPlay = "yes" },
		"label":       func(i *library.DeviceProfileIn) { i.Label = "" },
		"ua":          func(i *library.DeviceProfileIn) { i.UserAgent = strings.Repeat("x", 513) },
		"probed_at":   func(i *library.DeviceProfileIn) { s := "soon"; i.ProbedAt = &s },
	} {
		bad := in
		mut(&bad)
		var ve *library.ValidationError
		if _, err := e.svc.PutDeviceProfile(ctx, p1, bad); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func insertRun(t *testing.T, e *env, id string, report any) {
	t.Helper()
	b, _ := json.Marshal(report)
	_, err := e.st.W.Exec(`INSERT INTO import_runs(id, started_at, heartbeat_at, finished_at, state, principal, inbox_label, plan_digest, summary_json, report_json)
		VALUES (?, '2026-10-06T12:30:00Z', '2026-10-06T12:30:00Z', '2026-10-06T12:31:00Z', 'completed', 'cli', 'inbox', 'd', '{"errors":0,"warnings":0}', ?)`, id, string(b))
	if err != nil {
		t.Fatal(err)
	}
}

func TestImportRun_Sections(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	rl, err := e.svc.ImportRuns(ctx, 10, "")
	runs := rl.Items
	if err != nil || len(runs) != 1 || runs[0].ID != lt.RunID || runs[0].Errors != 1 || runs[0].Warnings != 2 || runs[0].Summary["tracks_new"] != float64(5) {
		t.Fatalf("runs %+v %v", runs, err)
	}
	pg, err := e.svc.ImportRun(ctx, lt.RunID, "", "", 0)
	if err != nil || pg.Section != "errors" || pg.Total != 1 || len(pg.Items) != 1 {
		t.Fatalf("default section: %+v %v", pg, err)
	}
	counts := map[string]int{"errors": 1, "warnings": 2, "unmatched": 1, "ambiguous": 0, "audio": 2, "recipes": 1, "drafts": 0, "duplicates": 0, "ignored": 0}
	for sec, n := range counts {
		pg, err := e.svc.ImportRun(ctx, lt.RunID, sec, "", 50)
		if err != nil || pg.Total != n || len(pg.Items) != n || pg.NextCursor != nil {
			t.Errorf("section %s: %+v %v", sec, pg, err)
		}
	}
	var ve *library.ValidationError
	if _, err := e.svc.ImportRun(ctx, lt.RunID, "inventory", "", 50); !errors.As(err, &ve) {
		t.Errorf("unknown section: %v", err)
	}
	if _, err := e.svc.ImportRun(ctx, "nope", "errors", "", 50); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown run: %v", err)
	}
	// paging
	pg, _ = e.svc.ImportRun(ctx, lt.RunID, "warnings", "", 1)
	if len(pg.Items) != 1 || pg.NextCursor == nil {
		t.Fatalf("limit 1: %+v", pg)
	}
	pg2, _ := e.svc.ImportRun(ctx, lt.RunID, "warnings", *pg.NextCursor, 1)
	if len(pg2.Items) != 1 || pg2.NextCursor != nil || cmp.Diff(pg.Items, pg2.Items) == "" {
		t.Fatalf("second page: %+v", pg2)
	}

	// a 500-recipe report stays under the page cap and pages without loss
	big := make([]map[string]any, 500)
	for i := range big {
		srcs := make([]string, 60)
		for j := range srcs {
			srcs[j] = fmt.Sprintf("music/some/long/path/file-%03d.jsonl:%d", i, j)
		}
		big[i] = map[string]any{"request_id": fmt.Sprintf("big-%03d", i), "hash": lt.Sha(fmt.Sprint(i)), "sources": srcs, "placement": "draft_backlog"}
	}
	const bigID = "01900000-0000-7000-8000-000000000999"
	insertRun(t, e, bigID, map[string]any{"recipes": big})
	cur, got, pages := "", 0, 0
	seen := map[string]bool{}
	for {
		pg, err := e.svc.ImportRun(ctx, bigID, "recipes", cur, 200)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(pg)
		if len(b) >= 90000 {
			t.Fatalf("page %d is %d bytes", pages, len(b))
		}
		for _, it := range pg.Items {
			id := it.(map[string]any)["request_id"].(string)
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
		}
		got += len(pg.Items)
		pages++
		if pg.Total != 500 {
			t.Fatalf("total %d", pg.Total)
		}
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if got != 500 || pages < 2 {
		t.Errorf("got %d items in %d pages", got, pages)
	}
}

func TestImportRun_EverySectionPagesByCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	const id = "01900000-0000-7000-8000-000000000998"
	rep := map[string]any{}
	for _, k := range library.ImportSections {
		if k == "unmatched" || k == "ambiguous" {
			continue
		}
		items := make([]map[string]any, 3)
		for i := range items {
			items[i] = map[string]any{"n": i, "k": k}
		}
		rep[k] = items
	}
	var audio []map[string]any
	for i := 0; i < 5; i++ {
		st := "unmatched"
		if i >= 3 {
			st = "ambiguous"
		}
		audio = append(audio, map[string]any{"n": i, "status": st})
	}
	rep["audio"] = audio
	insertRun(t, e, id, rep)
	want := map[string]int{"unmatched": 3, "ambiguous": 2}
	for _, sec := range library.ImportSections {
		n, ok := want[sec]
		if !ok {
			n = 3
			if sec == "audio" {
				n = 5
			}
		}
		cur, got := "", 0
		for pages := 0; ; pages++ {
			pg, err := e.svc.ImportRun(ctx, id, sec, cur, 1)
			if err != nil {
				t.Fatal(err)
			}
			if pg.Total != n || len(pg.Items) != 1 {
				t.Fatalf("%s page %d: total %d items %d", sec, pages, pg.Total, len(pg.Items))
			}
			got++
			if pg.NextCursor == nil {
				break
			}
			cur = *pg.NextCursor
		}
		if got != n {
			t.Errorf("%s: paged %d items, want %d", sec, got, n)
		}
	}
}

func TestImportRun_PageCapBoundary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// 150 items of exactly 999 bytes: the item bytes alone fill ~90 000, so the
	// envelope (total, run summary, cursor) must be budgeted too.
	items := make([]map[string]any, 150)
	for i := range items {
		items[i] = map[string]any{"p": strings.Repeat("x", 999-len(`{"p":""}`))}
	}
	if b, _ := json.Marshal(items[0]); len(b) != 999 {
		t.Fatalf("item is %d bytes", len(b))
	}
	const id = "01900000-0000-7000-8000-000000000997"
	insertRun(t, e, id, map[string]any{"warnings": items})
	cur, got := "", 0
	for {
		pg, err := e.svc.ImportRun(ctx, id, "warnings", cur, 200)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(pg)
		if len(b) >= 90000 {
			t.Fatalf("page is %d bytes", len(b))
		}
		got += len(pg.Items)
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if got != 150 {
		t.Errorf("got %d items", got)
	}
}

func TestRun_BeginHeartbeatFinish(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest"); !errors.Is(err, library.ErrImportInProgress) {
		t.Fatalf("second begin: %v", err)
	}
	e.clk.Add(90 * time.Second)
	if err := e.svc.Heartbeat(ctx, id); err != nil {
		t.Fatal(err)
	}
	e.clk.Add(90 * time.Second) // 90 s after the heartbeat: still live
	if _, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest"); !errors.Is(err, library.ErrImportInProgress) {
		t.Fatalf("after heartbeat: %v", err)
	}
	e.clk.Add(3 * time.Minute) // stale
	id2, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest")
	if err != nil {
		t.Fatal(err)
	}
	var state, errTxt string
	if err := e.st.R.QueryRow(`SELECT state, error FROM import_runs WHERE id = ?`, id).Scan(&state, &errTxt); err != nil || state != "failed" || errTxt != "stale: no heartbeat" {
		t.Fatalf("stale run: %s %s %v", state, errTxt, err)
	}
	if err := e.svc.FinishRun(ctx, id2, "completed", []byte(`{"errors":2}`), []byte(`{"errors":[]}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.FinishRun(ctx, id2, "failed", nil, nil, errors.New("late")); err != nil { // finished runs are immutable: no-op
		t.Fatal(err)
	}
	if err := e.st.R.QueryRow(`SELECT state FROM import_runs WHERE id = ?`, id2).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("state %s", state)
	}
	if _, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest"); err != nil {
		t.Fatalf("begin after finish: %v", err)
	}
}

func TestLibrary_WritesTwice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	p, err := yue2.ParseRequest([]byte(`{"id":"twice-1","style":"x","lyrics":"[Verse]\na","cot":"full","seed":7}`), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		tx, _ := e.st.W.BeginTx(ctx, nil)
		rid, created, err := e.svc.InsertRecipe(ctx, tx, p, "import")
		if err != nil || created != (round == 1) {
			t.Fatalf("round %d recipe: %v created=%v", round, err, created)
		}
		src := library.SourceRef{Path: "a.jsonl", Line: 1, Raw: p.Canonical, RawKind: "jsonl_line"}
		if c, err := e.svc.InsertRecipeSource(ctx, tx, rid, src, lt.RunID); err != nil || c != (round == 1) {
			t.Fatalf("round %d source: %v %v", round, err, c)
		}
		if err := e.svc.InsertRefs(ctx, tx, rid, "idx.csv", map[string]string{"a": "b"}); err != nil {
			t.Fatalf("round %d refs: %v", round, err)
		}
		did, created, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: rid, GroupKey: "twice-1"})
		if err != nil || created != (round == 1) {
			t.Fatalf("round %d draft: %v %v", round, err, created)
		}
		if err := e.svc.AddToCollection(ctx, tx, did, "c", "c.json"); err != nil {
			t.Fatalf("round %d collection: %v", round, err)
		}
		if _, err := e.svc.RelinkVariantiMothers(ctx, tx); err != nil {
			t.Fatalf("round %d relink: %v", round, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		in := library.DeviceProfileIn{DeviceID: "twice-device-1", Label: "l", Quality: "auto", QualitySource: "default", ProbeResult: "none", ProbeVersion: 1}
		if _, err := e.svc.PutDeviceProfile(ctx, auth.Principal{ID: "x"}, in); err != nil {
			t.Fatalf("round %d device: %v", round, err)
		}
		if _, err := e.st.W.Exec(`UPDATE tracks SET title = ? WHERE id = ?`, "Renamed", lt.TrackLuna); err != nil {
			t.Fatalf("round %d title: %v", round, err)
		}
		if err := e.svc.Heartbeat(ctx, lt.RunID); err != nil { // finished run: no row matches, no trigger fires
			t.Fatalf("round %d heartbeat: %v", round, err)
		}
		rid2, err := e.svc.BeginRun(ctx, "cli", "inbox", "digest")
		if err != nil {
			t.Fatalf("round %d begin: %v", round, err)
		}
		for hb := 0; hb < 2; hb++ { // a running run: the heartbeat row really updates
			if err := e.svc.Heartbeat(ctx, rid2); err != nil {
				t.Fatalf("round %d heartbeat %d: %v", round, hb, err)
			}
		}
		if err := e.svc.FinishRun(ctx, rid2, "completed", nil, nil, nil); err != nil {
			t.Fatalf("round %d finish: %v", round, err)
		}
		if err := e.svc.FinishRun(ctx, lt.RunID, "completed", nil, nil, nil); err != nil {
			t.Fatalf("round %d finish fixture run: %v", round, err)
		}
	}
	// the title change is visible through FTS
	if got := search(t, e, library.SearchParams{Q: "renamed"}); !slices.Equal(got, []string{lt.TrackLuna}) {
		t.Errorf("fts after rename: %v", got)
	}
}

func TestInsertTake_FulfilsOwnAndCoreMatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	own := draftRecipe(t, e, e.fx.Drafts[0])
	other := draftRecipe(t, e, e.fx.Drafts[3]) // fxb_002_v1

	// a take of the same request with a non-default cfg_scale has another recipe hash but the same core hash
	var req string
	if err := e.st.R.QueryRow(`SELECT request_json FROM recipes WHERE id = ?`, other).Scan(&req); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(req), &m)
	m["cfg_scale"] = 5.0
	raw, _ := json.Marshal(m)
	p, err := yue2.ParseRequest(raw, yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}

	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	var f1, f2 library.Fulfilment
	t1 := newTake(lt.TrackIDForTest(1), own, 1)
	t1.Out = &f1
	if err := e.svc.InsertTake(ctx, tx, t1); err != nil {
		t.Fatal(err)
	}
	rid2, created, err := e.svc.InsertRecipe(ctx, tx, p, "import")
	if err != nil || !created {
		t.Fatalf("core recipe: %v %v", err, created)
	}
	t2 := newTake(lt.TrackIDForTest(2), rid2, 2)
	t2.Out = &f2
	if err := e.svc.InsertTake(ctx, tx, t2); err != nil {
		t.Fatal(err)
	}
	// a later take of the same recipe does not re-fulfil
	var f3 library.Fulfilment
	t3 := newTake(lt.TrackIDForTest(3), own, 3)
	t3.Out = &f3
	if err := e.svc.InsertTake(ctx, tx, t3); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if f1.OwnDraftID != e.fx.Drafts[0] || len(f1.CoreDrafts) != 0 {
		t.Errorf("own fulfilment %+v", f1)
	}
	if f2.OwnDraftID != "" || !slices.Equal(f2.CoreDrafts, []string{e.fx.Drafts[3]}) {
		t.Errorf("core fulfilment %+v", f2)
	}
	if f3.OwnDraftID != "" || len(f3.CoreDrafts) != 0 {
		t.Errorf("repeat fulfilment %+v", f3)
	}
	by := func(draft string) string {
		var s sql.NullString
		if err := e.st.R.QueryRow(`SELECT fulfilled_by_track_id FROM recipe_drafts WHERE id = ?`, draft).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	if by(e.fx.Drafts[0]) != t1.ID || by(e.fx.Drafts[3]) != t2.ID || by(e.fx.Drafts[1]) != "" {
		t.Errorf("fulfilled_by: %q %q %q", by(e.fx.Drafts[0]), by(e.fx.Drafts[3]), by(e.fx.Drafts[1]))
	}
}

func TestRelinkVariantiMothers_UniqueRoot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	mk := func(tx *sql.Tx, reqID string, seed int) string {
		p, err := yue2.ParseRequest([]byte(fmt.Sprintf(`{"id":%q,"style":"s","lyrics":"[Verse]\nx","cot":"full","seed":%d}`, reqID, seed)), yue2.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rid, _, err := e.svc.InsertRecipe(ctx, tx, p, "import")
		if err != nil {
			t.Fatal(err)
		}
		return rid
	}
	tx, _ := e.st.W.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	// mother with a sibling: both resolve to the same root
	motherRecipe, sibRecipe := mk(tx, "fxm_001", 1), mk(tx, "fxm_001-s2", 2)
	mother := newTake(lt.TrackIDForTest(10), motherRecipe, 10)
	sib := newTake(lt.TrackIDForTest(11), sibRecipe, 11)
	sib.ParentTrackID, sib.RootTrackID, sib.Generation = mother.ID, mother.ID, 1
	sib.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: true, Rule: "seed_family"}
	// two unrelated roots in one group: ambiguous
	r1, r2 := mk(tx, "fxn_001", 3), mk(tx, "fxn_001_v3", 4)
	n1, n2 := newTake(lt.TrackIDForTest(12), r1, 12), newTake(lt.TrackIDForTest(13), r2, 13)
	for _, tk := range []library.NewTake{mother, sib, n1, n2} {
		if err := e.svc.InsertTake(ctx, tx, tk); err != nil {
			t.Fatal(err)
		}
	}
	draft := func(req, group string) string {
		rid := mk(tx, req, 100+len(req))
		id, _, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: rid, GroupKey: group})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dOK, dAmbiguous, dNone := draft("fxm_001_v2", "fxm_001"), draft("fxn_001_v2", "fxn_001"), draft("fxq_001_v2", "fxq_001")
	for i := 0; i < 2; i++ {
		n, err := e.svc.RelinkVariantiMothers(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[int]int{0: 1, 1: 0}[i]; n != want {
			t.Errorf("round %d relinked %d, want %d", i, n, want)
		}
	}
	parent := func(id string) string {
		var s sql.NullString
		if err := tx.QueryRow(`SELECT parent_track_id FROM recipe_drafts WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s.String
	}
	if parent(dOK) != mother.ID || parent(dAmbiguous) != "" || parent(dNone) != "" {
		t.Errorf("parents: %q %q %q", parent(dOK), parent(dAmbiguous), parent(dNone))
	}
}

func TestMediaAndPeaks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	m, err := e.svc.Media(ctx, lt.TrackLuna)
	if err != nil || len(m.Flac.SHA256) != 64 || m.MP3.BitrateKbps != 320 || m.Flac.ContentType != "audio/flac" || m.MP3.ContentType != "audio/mpeg" || m.Flac.SampleRate != 48000 {
		t.Fatalf("media %+v %v", m, err)
	}
	if _, err := e.svc.Media(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	obj, err := e.svc.Resolve(ctx, m.Flac.SHA256, "flac")
	if err != nil || obj.ContentType != "audio/flac" || obj.ETag != m.Flac.SHA256 || !strings.HasPrefix(obj.RelPath, "audio/masters/") {
		t.Fatalf("resolve flac: %+v %v", obj, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, obj.RelPath)); err != nil {
		t.Errorf("flac file: %v", err)
	}
	obj, err = e.svc.Resolve(ctx, m.MP3.SHA256, "mp3")
	if err != nil || obj.ContentType != "audio/mpeg" || !strings.HasPrefix(obj.RelPath, "derived/") {
		t.Fatalf("resolve mp3: %+v %v", obj, err)
	}
	for _, c := range [][2]string{{m.MP3.SHA256, "flac"}, {m.Flac.SHA256, "mp3"}, {strings.Repeat("0", 64), "flac"}, {m.Flac.SHA256, "ogg"}} {
		if _, err := e.svc.Resolve(ctx, c[0], c[1]); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("resolve %v: %v", c, err)
		}
	}
	pk, err := e.svc.Peaks(ctx, lt.TrackLuna, "")
	if err != nil || pk.Level != "overview" || pk.Peaks.Length != 4 || pk.DurationS != 12 {
		t.Fatalf("peaks %+v %v", pk, err)
	}
	pk, err = e.svc.Peaks(ctx, lt.TrackLuna, "detail")
	if err != nil || pk.Peaks.SamplesPerPixel != 2400 {
		t.Fatalf("detail %+v %v", pk, err)
	}
	if _, err := e.svc.Peaks(ctx, "nope", "overview"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track peaks: %v", err)
	}
	var ve *library.ValidationError
	if _, err := e.svc.Peaks(ctx, lt.TrackLuna, "huge"); !errors.As(err, &ve) {
		t.Errorf("level: %v", err)
	}
	// a track without renditions: media unavailable (409 at the API)
	bare := addTrack(t, e, 1, "bare")
	if _, err := e.svc.Media(ctx, bare); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("bare media: %v", err)
	}
	if _, err := e.svc.Peaks(ctx, bare, "overview"); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("bare peaks: %v", err)
	}
}

func TestFacetsAndSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	f, err := e.svc.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.StyleTerms) == 0 || f.StyleTerms[0].Term != "ambient" || f.StyleTerms[0].Count != 2 ||
		!slices.Equal(f.Statuses, []library.StatusCount{{Status: "draft", Count: 5}}) ||
		*f.DurationS.Min != 6 || *f.DurationS.Max != 12 || f.RecordedAt.Min == nil {
		t.Errorf("facets %+v", f)
	}
	snap, err := e.svc.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Recipes) != 11 || len(snap.Tracks) != 5 || len(snap.AudioIDs) != 5 || len(snap.Drafts) != 6 || len(snap.SourceKeys) != 11 ||
		len(snap.RefKeys) != 1 || len(snap.Collections["pilot-x"]) != 3 || len(snap.MasterPaths) != 5 || len(snap.DerivedPaths) != 8 {
		t.Errorf("snapshot sizes: recipes=%d tracks=%d audio=%d drafts=%d sources=%d refs=%d coll=%d masters=%d derived=%d",
			len(snap.Recipes), len(snap.Tracks), len(snap.AudioIDs), len(snap.Drafts), len(snap.SourceKeys), len(snap.RefKeys), len(snap.Collections["pilot-x"]), len(snap.MasterPaths), len(snap.DerivedPaths))
	}
}

func fullSummary() string {
	m := map[string]any{"errors": 0, "warnings": 0, "by_kind": map[string]int{"a": 1, "b": 2, "c": 3}}
	for i := 0; i < 28; i++ {
		m[fmt.Sprintf("counter_number_%02d", i)] = 1000000 + i
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestImportRuns_PageCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	want := []string{lt.RunID}
	for i := 0; i < 199; i++ {
		id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa000+i)
		want = append(want, id)
		_, err := e.st.W.Exec(`INSERT INTO import_runs(id, started_at, heartbeat_at, finished_at, state, principal, inbox_label, plan_digest, summary_json)
			VALUES (?, ?, '2026-10-06T12:30:00Z', '2026-10-06T12:31:00Z', 'completed', 'cli', 'inbox', 'd', ?)`,
			id, fmt.Sprintf("2026-10-07T12:%02d:%02dZ", i/60, i%60), fullSummary())
		if err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cur, pages := "", 0
	for {
		l, err := e.svc.ImportRuns(ctx, 200, cur)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(l); len(b) >= 90000 {
			t.Fatalf("page %d is %d bytes", pages, len(b))
		}
		for _, r := range l.Items {
			seen = append(seen, r.ID)
		}
		pages++
		if l.NextCursor == nil {
			break
		}
		cur = *l.NextCursor
	}
	if pages < 2 {
		t.Fatalf("expected the cap to split the result, got %d page(s)", pages)
	}
	sameSet(t, "runs", seen, want...)
	if len(seen) != len(want) {
		t.Errorf("duplicates: %d vs %d", len(seen), len(want))
	}
}

func TestDeviceProfiles_PageCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	p := auth.Principal{ID: "tok-1", Name: "t"}
	want := []string{}
	if l, err := e.svc.DeviceProfiles(ctx, 0, ""); err != nil {
		t.Fatal(err)
	} else {
		for _, d := range l.Items {
			want = append(want, d.DeviceID)
		}
	}
	for i := 0; i < 199; i++ {
		id := fmt.Sprintf("dev-%05d", i)
		want = append(want, id)
		_, err := e.svc.PutDeviceProfile(ctx, p, library.DeviceProfileIn{DeviceID: id, Label: strings.Repeat("l", 80), UserAgent: strings.Repeat("u", 512),
			Quality: "auto", QualitySource: "default", ProbeResult: "none"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cur, pages := "", 0
	for {
		l, err := e.svc.DeviceProfiles(ctx, 200, cur)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(l); len(b) >= 90000 {
			t.Fatalf("page %d is %d bytes", pages, len(b))
		}
		for _, d := range l.Items {
			seen = append(seen, d.DeviceID)
		}
		pages++
		if l.NextCursor == nil {
			break
		}
		cur = *l.NextCursor
	}
	if pages < 2 {
		t.Fatalf("expected the cap to split the result, got %d page(s)", pages)
	}
	sameSet(t, "profiles", seen, want...)
	if len(seen) != len(want) {
		t.Errorf("duplicates: %d vs %d", len(seen), len(want))
	}
}

func TestListDrafts_PageCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var want []string
	for i := 0; i < 100; i++ {
		terms := make([]string, 20)
		for j := range terms {
			terms[j] = fmt.Sprintf("t%03d%02d", i, j) + strings.Repeat("x", 40-6)
		}
		raw := fmt.Sprintf(`{"id":"cap-%03d","style":%q,"lyrics":"[Verse]\nline","cot":"full","seed":%d}`, i, strings.Join(terms, ", "), 7000+i)
		p, err := yue2.ParseRequest([]byte(raw), yue2.ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rid, _, err := e.svc.InsertRecipe(ctx, tx, p, "import")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := e.svc.InsertDraft(ctx, tx, library.NewDraft{Origin: "varianti_import", SourceRecipeID: rid, GroupKey: "capgrp"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cur, pages := "", 0
	for {
		pg, err := e.svc.ListDrafts(ctx, library.DraftParams{GroupKey: "capgrp", Limit: 100, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(pg); len(b) >= 90000 {
			t.Fatalf("page %d is %d bytes", pages, len(b))
		}
		for _, d := range pg.Items {
			seen = append(seen, d.ID)
		}
		pages++
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if pages < 2 {
		t.Fatalf("expected the cap to split the result, got %d page(s)", pages)
	}
	sameSet(t, "drafts", seen, want...)
	if len(seen) != len(want) {
		t.Errorf("duplicates: %d vs %d", len(seen), len(want))
	}
}
