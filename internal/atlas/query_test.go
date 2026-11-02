package atlas_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type world struct {
	st   *store.Store
	svc  *atlas.Service
	sg   *songs.Service
	info librarytest.L3Info
}

func openStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fixedNow() time.Time { return librarytest.FixedNow }

// newWorld is FixtureL3 with the literal song_features rows (the oracle of the fixture, no features service).
func newWorld(t *testing.T) *world {
	t.Helper()
	st := openStore(t)
	info := librarytest.FixtureL3(t, st, librarytest.FixedNow)
	librarytest.InsertL3FeatureRows(t, st, librarytest.FixedNow, info)
	return &world{st: st, svc: atlas.New(st, fixedNow), sg: songs.New(st, fixedNow, func() string { return "x" }), info: info}
}

func (w *world) get(t *testing.T, q atlas.Query) atlas.Atlas {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = atlas.MaxPage
	}
	a, err := w.svc.Get(context.Background(), q)
	if err != nil {
		t.Fatalf("Get(%+v): %v", q, err)
	}
	return a
}

// write runs a statement on the writer pool.
func (w *world) write(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func digestOf(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a problem", err)
	}
	return pe
}

// labels maps a response's Song ids back to the fixture labels ("T1", "d2", ...); other Songs keep their id.
func (w *world) labels(a atlas.Atlas) map[string]atlas.Point {
	back := map[string]string{}
	for l, id := range w.info.Songs {
		back[id] = l
	}
	out := map[string]atlas.Point{}
	for _, p := range a.Points {
		if l, ok := back[p.ID]; ok {
			out[l] = p
		} else {
			out[p.ID] = p
		}
	}
	return out
}

// numericOf is the independent oracle of an axis value: the literal fixture row.
func numericOf(axis string, f librarytest.L3Feature) *int {
	switch axis {
	case "tempo":
		return f.BPMx100
	case "bpm_requested":
		return f.BPMRequestedx100
	case "bpm_measured":
		return f.BPMMeasuredx100
	case "energy":
		return f.Energyx10000
	case "duration":
		if f.DurationS != nil {
			v := int(*f.DurationS * 100)
			return &v
		}
	}
	return nil
}

var categorical = map[string]bool{"family": true, "vocal": true, "era": true, "mood": true, "language": true, "key": true}

func TestAtlas_Query_Table(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	m5, m3 := w.info.Muses["M5"], w.info.Muses["M3"]
	filters := []struct {
		name   string
		f      songs.ListFilter
		labels []string // nil: every Song of the library
	}{
		{"all", songs.ListFilter{}, nil},
		{"M5", songs.ListFilter{MuseID: m5}, []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}},
		{"M3", songs.ListFilter{MuseID: m3}, []string{"d1", "d2", "d3"}},
	}
	pairs := [][2]string{{"tempo", "energy"}, {"bpm_requested", "bpm_measured"}, {"energy", "tempo"}, {"duration", "tempo"},
		{"family", "vocal"}, {"key", "energy"}, {"mood", "key"}, {"tempo", "family"}}
	colors := []string{"none", "muse", "state", "pick", "family", "vocal", "era", "mood", "language", "key", "record"}
	n := 0
	for _, fl := range filters {
		for _, pr := range pairs {
			color := colors[n%len(colors)]
			n++
			t.Run(fmt.Sprintf("%s/%s-%s/%s", fl.name, pr[0], pr[1], color), func(t *testing.T) {
				a := w.get(t, atlas.Query{X: pr[0], Y: pr[1], ColorBy: color, Filter: fl.f})
				wantScope, err := w.sg.Count(context.Background(), fl.f)
				if err != nil {
					t.Fatal(err)
				}
				if a.Totals.Scope != wantScope {
					t.Errorf("scope = %d, want %d", a.Totals.Scope, wantScope)
				}
				// Expected plotted/missing from the literal rows. Songs without a row (the L2 world) have no numeric value.
				inScope := map[string]bool{}
				if fl.labels == nil {
					for _, f := range librarytest.L3Features {
						inScope[f.Label] = true
					}
				}
				for _, l := range fl.labels {
					inScope[l] = true
				}
				featured := 0
				var wantMissX, wantMissY, wantPlotted int
				for _, f := range librarytest.L3Features {
					if !inScope[f.Label] {
						continue
					}
					featured++
					okx, oky := categorical[pr[0]] || numericOf(pr[0], f) != nil, categorical[pr[1]] || numericOf(pr[1], f) != nil
					if !okx {
						wantMissX++
					}
					if !oky {
						wantMissY++
					}
					if okx && oky {
						wantPlotted++
					}
				}
				if rest := wantScope - featured; rest > 0 { // Songs with no feature row
					if !categorical[pr[0]] {
						wantMissX += rest
					}
					if !categorical[pr[1]] {
						wantMissY += rest
					}
					if categorical[pr[0]] && categorical[pr[1]] {
						wantPlotted += rest
					}
				}
				if a.Totals.Plotted != wantPlotted || a.Totals.Missing.X != wantMissX || a.Totals.Missing.Y != wantMissY {
					t.Errorf("plotted %d missing %+v, want %d and {%d %d}", a.Totals.Plotted, a.Totals.Missing, wantPlotted, wantMissX, wantMissY)
				}
				if len(a.Points) != a.Totals.Plotted || a.NextCursor != nil {
					t.Errorf("%d points for %d plotted, cursor %v", len(a.Points), a.Totals.Plotted, a.NextCursor)
				}
				for _, p := range a.Points {
					if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
						t.Errorf("point %s outside the unit square: %v %v", p.ID, p.X, p.Y)
					}
					if a.Legend == nil || p.C < 0 || p.C > 12 {
						t.Errorf("point %s colour %d", p.ID, p.C)
					}
				}
				sum := 0
				for _, e := range a.Legend {
					sum += e.Count
				}
				if color == "none" && len(a.Legend) != 0 || color != "none" && sum != a.Totals.Plotted {
					t.Errorf("legend %v for %d plotted (color_by %s)", a.Legend, a.Totals.Plotted, color)
				}
				if a.Def != atlas.Def || a.LayoutToken == "" || a.Axes.X.Name != pr[0] || a.Axes.Y.Name != pr[1] {
					t.Errorf("header: %q %q %q %q", a.Def, a.LayoutToken, a.Axes.X.Name, a.Axes.Y.Name)
				}
			})
		}
	}
	if n != 24 {
		t.Fatalf("%d rows, want 24", n)
	}
}

// TestAtlas_FilterEqualsListSongs: for fifteen filters totals.scope is list_songs' total.
func TestAtlas_FilterEqualsListSongs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	yes, no := true, false
	four := 4
	filters := map[string]songs.ListFilter{
		"muse":         {MuseID: w.info.Muses["M5"]},
		"no_muse":      {NoMuse: true},
		"state":        {States: []songs.State{"draft"}},
		"query":        {Q: "a"},
		"has_pick":     {HasPick: &no},
		"pick_decided": {PickDecided: &no},
		"unheard":      {Unheard: &yes},
		"in_record":    {InRecord: &yes},
		"not_in_rec":   {InRecord: &no},
		"min_stars":    {MinStars: &four},
		"unrated":      {Unrated: &yes},
		"manual_crate": {CrateID: w.info.Crates["manual_song"]},
		"queued":       {Queued: &no, HasOpenProposals: &no},
		"hidden":       {IncludeHidden: true, IncludeArchived: true},
		"mismatch":     {TempoMismatch: &no, MaxDurationS: ptrF(600), MinDurationS: ptrF(0)},
		"none":         {None: true},
	}
	for name, f := range filters {
		want, err := w.sg.Count(context.Background(), f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a := w.get(t, atlas.Query{X: "family", Y: "vocal", Filter: f})
		if a.Totals.Scope != want || a.Totals.ScopeTotal != want {
			t.Errorf("%s: scope %d/%d, list_songs total %d", name, a.Totals.Scope, a.Totals.ScopeTotal, want)
		}
	}
	if n, _ := w.sg.Count(context.Background(), filters["manual_crate"]); n == 0 {
		t.Error("the manual Crate selects nothing: the test proves nothing")
	}
}

func ptrF(v float64) *float64 { return &v }

// TestAtlas_DimFlagsMatches: with dim the scope stays whole and h marks the filter's Songs.
func TestAtlas_DimFlagsMatches(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	f := songs.ListFilter{MuseID: w.info.Muses["M5"]}
	flat := w.get(t, atlas.Query{X: "family", Y: "vocal", Filter: f})
	dim := w.get(t, atlas.Query{X: "family", Y: "vocal", Filter: f, Dim: true})
	all := w.get(t, atlas.Query{X: "family", Y: "vocal"})
	if dim.Totals.Scope != all.Totals.Scope || dim.Totals.Hit != flat.Totals.Scope || dim.Totals.Hit != 8 {
		t.Errorf("dim scope %d hit %d, flat scope %d, all %d", dim.Totals.Scope, dim.Totals.Hit, flat.Totals.Scope, all.Totals.Scope)
	}
	for l, p := range w.labels(dim) {
		if p.H == nil {
			t.Fatalf("%s has no h under dim", l)
		}
		want := 0
		if strings.HasPrefix(l, "T") {
			want = 1
		}
		if *p.H != want {
			t.Errorf("%s: h = %d, want %d", l, *p.H, want)
		}
	}
	for _, p := range flat.Points {
		if p.H != nil {
			t.Errorf("h present without dim")
		}
	}
	if dim.LayoutToken == flat.LayoutToken {
		t.Error("dim and flat share a token")
	}
}

func TestAtlas_PagingStable(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	full := w.get(t, atlas.Query{})
	if len(full.Points) < 10 {
		t.Fatalf("only %d points", len(full.Points))
	}
	var paged []atlas.Point
	cursor := ""
	pages := 0
	for {
		a := w.get(t, atlas.Query{Limit: 3, Cursor: cursor})
		if a.LayoutToken != full.LayoutToken {
			t.Fatalf("token changed between pages")
		}
		paged = append(paged, a.Points...)
		pages++
		if a.NextCursor == nil {
			break
		}
		cursor = *a.NextCursor
	}
	if pages < 3 || len(paged) != len(full.Points) {
		t.Fatalf("%d pages, %d points, want %d", pages, len(paged), len(full.Points))
	}
	for i := range paged {
		if paged[i] != full.Points[i] {
			t.Errorf("point %d differs between paged and full: %+v vs %+v", i, paged[i], full.Points[i])
		}
		if i > 0 && paged[i-1].ID >= paged[i].ID {
			t.Errorf("points not in id order at %d", i)
		}
	}
}

// TestAtlas_TokenCoversEveryFilter: every ListFilter field and every input that decides a row changes the token.
func TestAtlas_TokenCoversEveryFilter(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	yes := true
	n2 := 2
	variants := map[string]songs.ListFilter{
		"muse": {MuseID: "m"}, "no_muse": {NoMuse: true}, "angle": {Angle: "a"}, "states": {States: []songs.State{"draft"}}, "queued": {Queued: &yes},
		"session": {SessionID: "s"}, "sounds": {Sounds: map[string][]string{"mood": {"chill"}}}, "draft": {DraftID: "d"}, "track": {TrackID: "t"},
		"proposals": {HasOpenProposals: &yes}, "mismatch": {TempoMismatch: &yes}, "q": {Q: "q"}, "has_pick": {HasPick: &yes}, "hidden": {IncludeHidden: true},
		"archived": {IncludeArchived: true}, "pick_decided": {PickDecided: &yes}, "unheard": {Unheard: &yes}, "min_dur": {MinDurationS: ptrF(1)},
		"max_dur": {MaxDurationS: ptrF(1)}, "min_stars": {MinStars: &n2}, "unrated": {Unrated: &yes}, "tags": {Tags: []string{"x"}}, "lossy": {Lossy: &yes},
		"recipe": {RecipeKnown: &yes}, "in_record": {InRecord: &yes}, "crate": {CrateID: "c"}, "none": {None: true},
	}
	seen := map[string]string{w.get(t, atlas.Query{}).LayoutToken: "base"}
	note := func(name, tok string) {
		t.Helper()
		if prev, dup := seen[tok]; dup {
			t.Errorf("%s has the token of %s", name, prev)
		}
		seen[tok] = name
	}
	for name, f := range variants {
		note("filter "+name, w.get(t, atlas.Query{Filter: f}).LayoutToken)
	}
	note("dim", w.get(t, atlas.Query{Dim: true}).LayoutToken)
	note("x", w.get(t, atlas.Query{X: "bpm_measured"}).LayoutToken)
	note("y", w.get(t, atlas.Query{Y: "tempo", X: "energy"}).LayoutToken)
	note("color", w.get(t, atlas.Query{ColorBy: "muse"}).LayoutToken)

	// Data that changes a row changes the token; data outside the scope does not.
	m5 := atlas.Query{Filter: songs.ListFilter{MuseID: w.info.Muses["M5"]}}
	base := w.get(t, m5).LayoutToken
	w.write(t, `UPDATE song_features SET digest = ? WHERE song_id = ?`, digestOf("x"), w.info.Songs["T1"])
	if w.get(t, m5).LayoutToken == base {
		t.Error("a new feature digest left the token alone")
	}
	base = w.get(t, m5).LayoutToken
	w.write(t, `UPDATE song_features SET digest = ? WHERE song_id = ?`, digestOf("y"), w.info.Songs["d4"])
	if w.get(t, m5).LayoutToken != base {
		t.Error("a Song outside the scope changed the token")
	}
	w.write(t, `UPDATE songs SET title = 'Renamed' WHERE id = ?`, w.info.Songs["T2"])
	if w.get(t, m5).LayoutToken == base {
		t.Error("a rename left the token alone")
	}
	base = w.get(t, m5).LayoutToken
	w.write(t, `UPDATE songs SET muse_id = ?, muse_source = 'manual' WHERE id = ?`, w.info.Muses["M3"], w.info.Songs["T4"])
	if w.get(t, m5).LayoutToken == base {
		t.Error("leaving the scope left the token alone")
	}
}

func TestAtlas_PageCap(t *testing.T) {
	st := openStore(t)
	librarytest.FixtureL3Scale(t, st, librarytest.FixedNow, 700, 10)
	if _, err := st.W.Exec(`UPDATE songs SET title = ? || id`, strings.Repeat("日", 40)); err != nil {
		t.Fatal(err)
	}
	svc := atlas.New(st, fixedNow)
	total, pages := 0, 0
	cursor := ""
	plotted := 0
	for {
		a, err := svc.Get(context.Background(), atlas.Query{Limit: atlas.MaxPage, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		plotted = a.Totals.Plotted
		b, _ := json.Marshal(a)
		if len(b) > 90_000 {
			t.Fatalf("page %d is %d bytes, cap 90 000", pages, len(b))
		}
		if pages == 0 && (len(a.Points) == 0 || len(a.Points) >= atlas.MaxPage || a.NextCursor == nil) {
			t.Errorf("first page has %d points, cursor %v: the cap did not cut a whole point", len(a.Points), a.NextCursor)
		}
		for _, p := range a.Points {
			if len([]rune(p.T)) > atlas.TitleRunes {
				t.Fatalf("title of %d runes", len([]rune(p.T)))
			}
		}
		total += len(a.Points)
		pages++
		if a.NextCursor == nil {
			break
		}
		cursor = *a.NextCursor
	}
	if total != plotted || plotted < 700 {
		t.Errorf("paged %d points, plotted %d", total, plotted)
	}
}

func TestAtlas_Truncated(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	want, _ := w.sg.Count(context.Background(), songs.ListFilter{})
	w.svc.SetMaxScope(5)
	a := w.get(t, atlas.Query{X: "family", Y: "vocal"})
	if !a.Totals.Truncated || a.Totals.Scope != 5 || a.Totals.ScopeTotal != want || len(a.Points) != 5 {
		t.Errorf("totals %+v, %d points, want scope 5 of %d", a.Totals, len(a.Points), want)
	}
	// Newest first: the five Songs are the five newest by created_at.
	rows, err := w.st.R.Query(`SELECT id FROM songs WHERE retired_at IS NULL AND hidden_at IS NULL ORDER BY created_at DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var newest []string
	for rows.Next() && len(newest) < 5 {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		newest = append(newest, id)
	}
	for _, p := range a.Points {
		if !slices.Contains(newest, p.ID) {
			t.Errorf("%s is not among the five newest", p.ID)
		}
	}
}

func TestAtlas_BasisTotals(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a := w.get(t, atlas.Query{Filter: songs.ListFilter{MuseID: w.info.Muses["M5"]}})
	if a.Totals.Basis.X != (atlas.BasisCounts{M: 8}) || a.Totals.Basis.Y != (atlas.BasisCounts{M: 8}) {
		t.Errorf("the T set: basis %+v", a.Totals.Basis)
	}
	for l, p := range w.labels(a) {
		if p.XB != "m" || p.YB != "m" {
			t.Errorf("%s: xb %q yb %q", l, p.XB, p.YB)
		}
	}
	d := w.get(t, atlas.Query{Filter: songs.ListFilter{MuseID: w.info.Muses["M3"]}})
	if d.Totals.Basis.X != (atlas.BasisCounts{D: 3}) || d.Totals.Basis.Y != (atlas.BasisCounts{P: 3}) {
		t.Errorf("drafts: basis %+v, want x d:3 and y p:3", d.Totals.Basis)
	}
	k := w.get(t, atlas.Query{X: "key", Y: "energy", Filter: songs.ListFilter{MuseID: w.info.Muses["M5"]}})
	if k.Totals.Basis.X != (atlas.BasisCounts{M: 8}) {
		t.Errorf("key basis %+v", k.Totals.Basis)
	}
}

func TestAtlas_MissingNotPlotted(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	// A Song with no song_features row (S1 of the L2 world) is absent from tempo x energy and counted in missing.
	a := w.get(t, atlas.Query{})
	all, _ := w.sg.Count(context.Background(), songs.ListFilter{})
	if _, ok := w.labels(a)["S1"]; ok {
		t.Error("S1 has no features and must not be plotted")
	}
	if a.Totals.Missing.X != all-a.Totals.Plotted || a.Totals.Missing.Y != all-a.Totals.Plotted {
		t.Errorf("missing %+v, plotted %d of %d", a.Totals.Missing, a.Totals.Plotted, all)
	}
	// Reference Songs without measurements (u1, u2): no bpm_measured, so absent from requested x measured.
	g := w.get(t, atlas.Query{X: "bpm_requested", Y: "bpm_measured", Filter: songs.ListFilter{MuseID: w.info.Muses["M4"]}})
	if g.Totals.Plotted != 0 || g.Totals.Missing.Y != 2 || g.Totals.Missing.X != 0 {
		t.Errorf("requested x measured for M4: %+v", g.Totals)
	}
	// On categorical axes the same Song sits in (none).
	c := w.get(t, atlas.Query{X: "family", Y: "vocal"})
	if _, ok := w.labels(c)["S1"]; !ok {
		t.Error("S1 belongs in the (none) band of a categorical map")
	}
	if last := c.Axes.X.Categories[len(c.Axes.X.Categories)-1]; last.Value != atlas.NoneBand {
		t.Errorf("trailing band %q", last.Value)
	}
}

func TestAtlas_ColorByMuse_IdsOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a := w.get(t, atlas.Query{ColorBy: "muse"})
	if len(a.Muses) == 0 || len(a.Muses) > 12 {
		t.Fatalf("dictionary %v", a.Muses)
	}
	for _, p := range a.Points {
		if p.MI != nil && (*p.MI < 0 || *p.MI >= len(a.Muses)) {
			t.Errorf("mi %d outside the dictionary", *p.MI)
		}
		if p.MI != nil && a.Legend[p.C].Value != a.Muses[*p.MI] {
			t.Errorf("point colour %d is %q, dictionary says %q", p.C, a.Legend[p.C].Value, a.Muses[*p.MI])
		}
	}
	for _, e := range a.Legend {
		if e.Value != "none" && e.Value != "other" && !slices.Contains(a.Muses, e.Value) {
			t.Errorf("legend value %q is not a Muse id of the dictionary", e.Value)
		}
	}
	// Many Muses: at most 12 plus other.
	st := openStore(t)
	librarytest.FixtureL3Scale(t, st, librarytest.FixedNow, 60, 30)
	big, err := atlas.New(st, fixedNow).Get(context.Background(), atlas.Query{ColorBy: "muse", Limit: 600})
	if err != nil {
		t.Fatal(err)
	}
	if len(big.Muses) != 12 || len(big.Legend) != 13 || big.Legend[12].Value != "other" || big.Legend[12].C != 12 {
		t.Errorf("30 Muses: %d dictionary entries, legend %d, last %+v", len(big.Muses), len(big.Legend), big.Legend[len(big.Legend)-1])
	}
}

func TestAtlas_ColorByRecord(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a := w.get(t, atlas.Query{X: "family", Y: "vocal", ColorBy: "record"})
	want := map[string]string{}
	rows, err := w.st.R.Query(`SELECT rs.song_id, MAX(CASE rc.status WHEN 'published' THEN 2 ELSE 1 END) FROM record_slots rs JOIN records rc ON rc.id = rs.record_id
		WHERE rs.removed_at IS NULL AND rc.hidden_at IS NULL GROUP BY rs.song_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		want[id] = []string{"none", "draft_record", "published_record"}[n]
	}
	rows.Close()
	seen := map[string]bool{}
	for _, p := range a.Points {
		exp, ok := want[p.ID]
		if !ok {
			exp = "none"
		}
		got := a.Legend[slices.IndexFunc(a.Legend, func(e atlas.LegendEntry) bool { return e.C == p.C })].Value
		if got != exp {
			t.Errorf("%s: colour %q, want %q", p.ID, got, exp)
		}
		seen[got] = true
	}
	if !seen["published_record"] || !seen["none"] {
		t.Errorf("legend %v: the L2 world has a published Record and Songs outside any Record", a.Legend)
	}
}

func TestAtlas_Neighbours(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	t3 := w.info.Songs["T3"]
	a := w.get(t, atlas.Query{NearSongID: t3, K: 2, Filter: songs.ListFilter{MuseID: w.info.Muses["M5"]}})
	if len(a.Neighbors) != 2 {
		t.Fatalf("neighbours %+v", a.Neighbors)
	}
	got := []string{a.Neighbors[0].ID, a.Neighbors[1].ID}
	slices.Sort(got)
	want := []string{w.info.Songs["T2"], w.info.Songs["T4"]}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("nearest of T3 = %v, want T2 and T4 (%v)", got, want)
	}
	if a.Neighbors[0].Distance > a.Neighbors[1].Distance || a.Neighbors[0].Distance <= 0 {
		t.Errorf("not ordered by distance: %+v", a.Neighbors)
	}
	if a.Neighbors[0].Parts.Tempo+a.Neighbors[0].Parts.Key+a.Neighbors[0].Parts.Energy == 0 {
		t.Errorf("parts are empty: %+v", a.Neighbors[0])
	}
	// First page only, default k, never more than MaxNeighbours, never the Song itself.
	b := w.get(t, atlas.Query{NearSongID: t3})
	if len(b.Neighbors) != 8 {
		t.Errorf("default k gave %d", len(b.Neighbors))
	}
	for _, n := range b.Neighbors {
		if n.ID == t3 {
			t.Error("the Song is its own neighbour")
		}
	}
	if c := w.get(t, atlas.Query{NearSongID: t3, K: 500}); len(c.Neighbors) > atlas.MaxNeighbours {
		t.Errorf("%d neighbours", len(c.Neighbors))
	}
	pg := w.get(t, atlas.Query{NearSongID: t3, Limit: 2})
	next := w.get(t, atlas.Query{NearSongID: t3, Limit: 2, Cursor: *pg.NextCursor})
	if len(pg.Neighbors) == 0 || len(next.Neighbors) != 0 {
		t.Errorf("neighbours on pages: %d then %d", len(pg.Neighbors), len(next.Neighbors))
	}
	_, err := w.svc.Get(context.Background(), atlas.Query{NearSongID: "nope", Limit: 5})
	if pe := problemOf(t, err); pe.Status != http.StatusNotFound || pe.Code != "NOT_FOUND" {
		t.Errorf("unknown Song: %+v", pe)
	}
}

func TestAtlas_Pending(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.write(t, `DELETE FROM provenance_dirty`)
	if w.get(t, atlas.Query{}).Pending {
		t.Error("pending with an empty queue")
	}
	w.write(t, `INSERT INTO provenance_dirty(kind, id, at) VALUES('track', ?, '2026-01-01T00:00:00Z')`, w.info.Takes["T1"])
	if !w.get(t, atlas.Query{}).Pending {
		t.Error("a dirty take of a scope Song must set pending")
	}
	if w.get(t, atlas.Query{Filter: songs.ListFilter{MuseID: w.info.Muses["M3"]}}).Pending {
		t.Error("a dirty take outside the scope must not set pending")
	}
	w.write(t, `DELETE FROM provenance_dirty`)
	w.write(t, `INSERT INTO provenance_dirty(kind, id, at) VALUES('draft', ?, '2026-01-01T00:00:00Z')`, w.info.Drafts3["d1"])
	if !w.get(t, atlas.Query{Filter: songs.ListFilter{MuseID: w.info.Muses["M3"]}}).Pending {
		t.Error("a dirty draft of a scope Song must set pending")
	}
}

func TestAtlas_Stale(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	f := songs.ListFilter{MuseID: w.info.Muses["M5"]}
	first := w.get(t, atlas.Query{Limit: 3, Filter: f})
	if first.NextCursor == nil {
		t.Fatal("no next page")
	}
	// A change outside the scope leaves the open page valid.
	w.write(t, `UPDATE song_features SET digest = ? WHERE song_id = ?`, digestOf("o"), w.info.Songs["d4"])
	if _, err := w.svc.Get(context.Background(), atlas.Query{Limit: 3, Filter: f, Cursor: *first.NextCursor}); err != nil {
		t.Fatalf("outside change: %v", err)
	}
	// A cursor with other axes is stale too (the token covers the axes).
	_, err := w.svc.Get(context.Background(), atlas.Query{X: "bpm_measured", Limit: 3, Filter: f, Cursor: *first.NextCursor})
	if pe := problemOf(t, err); pe.Code != "ATLAS_STALE" {
		t.Errorf("other axes: %+v", pe)
	}
	// A change inside the scope is a 409.
	w.write(t, `UPDATE song_features SET energy_x10000 = 1234, digest = ? WHERE song_id = ?`, digestOf("i"), w.info.Songs["T1"])
	_, err = w.svc.Get(context.Background(), atlas.Query{Limit: 3, Filter: f, Cursor: *first.NextCursor})
	if pe := problemOf(t, err); pe.Status != http.StatusConflict || pe.Code != "ATLAS_STALE" {
		t.Errorf("stale page: %+v", pe)
	}
}

func TestAtlas_RequestedGotPreset(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a := w.get(t, atlas.Query{X: "bpm_requested", Y: "bpm_measured", Filter: songs.ListFilter{MuseID: w.info.Muses["M5"]}})
	var kinds []string
	for _, g := range a.Guides {
		kinds = append(kinds, g.Kind)
	}
	if !slices.Equal(kinds, []string{"identity", "octave_up", "octave_down"}) {
		t.Errorf("guides %v", kinds)
	}
	if a.Totals.Plotted != 8 {
		t.Fatalf("plotted %d", a.Totals.Plotted)
	}
	for l, p := range w.labels(a) {
		if d := p.X - p.Y; d > 0.02 || d < -0.02 {
			t.Errorf("%s asked what it got but sits off the identity line: %v, %v", l, p.X, p.Y)
		}
		if p.XB != "d" || p.YB != "m" {
			t.Errorf("%s: bases %q %q", l, p.XB, p.YB)
		}
	}
	if g := w.get(t, atlas.Query{}).Guides; g == nil || len(g) != 0 {
		t.Errorf("tempo x energy guides %v, want an empty list", g)
	}
	// The axis info carries the domain.
	if a.Axes.X.Kind != atlas.KindNumeric || a.Axes.X.Min == nil || *a.Axes.X.Min != 6000 || *a.Axes.X.Max != 20000 || a.Axes.X.Scale != "log2" {
		t.Errorf("axis info %+v", a.Axes.X)
	}
}

func TestAtlas_AxesErrors(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	cases := []struct {
		name   string
		q      atlas.Query
		status int
		code   string
	}{
		{"unknown x", atlas.Query{X: "loudness"}, 422, "UNKNOWN_AXIS"},
		{"unknown y", atlas.Query{Y: "nope"}, 422, "UNKNOWN_AXIS"},
		{"same axis", atlas.Query{X: "tempo", Y: "tempo"}, 422, "AXIS_PAIR_INVALID"},
		{"color", atlas.Query{ColorBy: "rainbow"}, 422, "UNKNOWN_COLOR_BY"},
		{"cursor garbage", atlas.Query{Cursor: "!!"}, 422, "INVALID_CURSOR"},
		{"cursor short", atlas.Query{Cursor: "YWJj"}, 422, "INVALID_CURSOR"},
	}
	for _, c := range cases {
		_, err := w.svc.Get(context.Background(), c.q)
		pe := problemOf(t, err)
		if pe.Status != c.status || pe.Code != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, pe.Status, pe.Code, c.status, c.code)
		}
	}
}

// TestAtlas_NoMuseText: the document carries Muse ids at most, never a title or an artist.
func TestAtlas_NoMuseText(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rows, err := w.st.R.Query(`SELECT title, artist FROM muses`)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for rows.Next() {
		var a, b sql.NullString
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, a.String, b.String)
	}
	rows.Close()
	if len(texts) < 6 {
		t.Fatalf("only %d Muse texts", len(texts))
	}
	for _, by := range []string{"muse", "none", "key"} {
		a := w.get(t, atlas.Query{ColorBy: by, NearSongID: w.info.Songs["T3"]})
		b, _ := json.Marshal(a)
		for _, tx := range texts {
			if tx != "" && strings.Contains(string(b), tx) {
				t.Errorf("color_by %s leaks Muse text %q", by, tx)
			}
		}
	}
}

// dump hashes every row of every table.
func dump(t *testing.T, st *store.Store) string {
	t.Helper()
	tables, err := st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	h := sha256.New()
	for _, n := range names {
		rows, err := st.R.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(h, n, vals)
		}
		rows.Close()
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestGETNeverWritesDB_L3(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	before := dump(t, w.st)
	yes := true
	for _, q := range []atlas.Query{{}, {X: "family", Y: "key", ColorBy: "muse", Dim: true, Filter: songs.ListFilter{Unheard: &yes}},
		{NearSongID: w.info.Songs["T3"], ColorBy: "state"}, {X: "bpm_requested", Y: "bpm_measured", Limit: 3}} {
		a := w.get(t, q)
		if a.NextCursor != nil {
			w.get(t, atlas.Query{X: q.X, Y: q.Y, Limit: 3, Cursor: *a.NextCursor})
		}
	}
	_, _ = w.svc.Get(context.Background(), atlas.Query{X: "bad"})
	if after := dump(t, w.st); after != before {
		t.Error("Get changed the database")
	}
	tx, err := w.st.R.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE songs SET title = 'x'`); err == nil {
		t.Error("the reader pool accepted a write")
	}
}

// TestAtlas_StateEqualsSongState: the state the Atlas colours by is the shared C90.4 state (store.SongStateSQL) for every
// Song of the L3 fixture and of a scale library with Picks.
func TestAtlas_StateEqualsSongState(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, st *store.Store) {
		want := map[string]int{}
		rows, err := st.R.Query(`SELECT st.song_id, CASE WHEN st.picked = 1 THEN 3 WHEN st.judged = 1 THEN 2 WHEN st.takes > 0 THEN 1 ELSE 0 END FROM (` + store.SongStateSQL + `) st`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatal(err)
			}
			want[id] = n
		}
		rows.Close()
		a, err := atlas.New(st, fixedNow).Get(context.Background(), atlas.Query{X: "family", Y: "vocal", Limit: atlas.MaxPage, Filter: songs.ListFilter{IncludeHidden: true, IncludeArchived: true}})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int]int{}
		for _, p := range a.Points {
			if want[p.ID] != p.ST {
				t.Errorf("%s: state %d, SongStateSQL says %d", p.ID, p.ST, want[p.ID])
			}
			seen[p.ST]++
		}
		t.Logf("states seen: %v", seen)
	}
	t.Run("l3", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		check(t, w.st)
	})
	t.Run("scale", func(t *testing.T) {
		t.Parallel()
		st := openStore(t)
		librarytest.FixtureL3Scale(t, st, librarytest.FixedNow, 300, 10)
		check(t, st)
	})
}
