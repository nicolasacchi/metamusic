package board_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/board"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func get(t *testing.T, b *board.Service, q board.Query) board.Board {
	t.Helper()
	got, err := b.Get(context.Background(), q)
	if err != nil {
		t.Fatalf("Get(%+v): %v", q, err)
	}
	return got
}

func rowOf(b board.Board, muse string) (board.Row, bool) {
	for _, r := range b.Rows {
		if (r.Muse == nil && muse == "") || (r.Muse != nil && r.Muse.ID == muse) {
			return r, true
		}
	}
	return board.Row{}, false
}

func size(t *testing.T, b board.Board) int {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestBoard_Equivalence(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs := board.New(l.St, func() time.Time { return l.Now })
	b := get(t, bs, board.Query{Detail: "counts"})
	ctx := context.Background()
	for _, muse := range []string{l.MuseA, l.MuseB} {
		row, ok := rowOf(b, muse)
		if !ok {
			t.Fatalf("no row for muse %s", muse)
		}
		cov, err := l.View.MuseCoverage(ctx, muse)
		if err != nil {
			t.Fatal(err)
		}
		if row.SongsTotal != cov.Songs || row.Counts.Draft != cov.Draft || row.Counts.Rendered != cov.Rendered || row.Counts.Judged != cov.Judged || row.Counts.Picked != cov.Picked || row.Counts.Queued != cov.QueuedJobs {
			t.Errorf("muse %s: board %+v total %d, coverage %+v", muse, row.Counts, row.SongsTotal, cov)
		}
		page, err := l.Songs.List(ctx, songs.ListFilter{MuseID: muse, Limit: 100})
		if err != nil || page.Total != row.SongsTotal {
			t.Errorf("list_songs total %d (%v), board %d", page.Total, err, row.SongsTotal)
		}
		for _, st := range []songs.State{songs.StateDraft, songs.StateRendered, songs.StateJudged, songs.StatePicked} {
			p, _ := l.Songs.List(ctx, songs.ListFilter{MuseID: muse, States: []songs.State{st}, Limit: 100})
			want := map[songs.State]int{songs.StateDraft: row.Counts.Draft, songs.StateRendered: row.Counts.Rendered, songs.StateJudged: row.Counts.Judged, songs.StatePicked: row.Counts.Picked}[st]
			if p.Total != want {
				t.Errorf("muse %s state %s: list_songs %d, board %d", muse, st, p.Total, want)
			}
		}
	}
	// totals equal the list_songs total over the same (empty) filter
	all, _ := l.Songs.List(ctx, songs.ListFilter{Limit: 100})
	if b.Totals.Songs != all.Total || b.Totals.Draft+b.Totals.Rendered+b.Totals.Judged+b.Totals.Picked != all.Total {
		t.Errorf("totals %+v vs list_songs %d", b.Totals, all.Total)
	}
}

func TestBoard_CountsVsChips(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs := board.New(l.St, func() time.Time { return l.Now })
	c := get(t, bs, board.Query{Detail: "counts"})
	k := get(t, bs, board.Query{Detail: "chips"})
	if len(c.Rows) != len(k.Rows) || c.Totals != k.Totals || !reflect.DeepEqual(c.Columns, k.Columns) {
		t.Fatalf("counts and chips disagree: %d/%d rows", len(c.Rows), len(k.Rows))
	}
	for i := range c.Rows {
		if c.Rows[i].Counts != k.Rows[i].Counts || c.Rows[i].SongsTotal != k.Rows[i].SongsTotal {
			t.Errorf("row %d counts differ", i)
		}
		n := 0
		for _, cell := range c.Rows[i].Cells {
			if len(cell.Chips) != 0 {
				t.Errorf("detail=counts carries chips")
			}
		}
		for _, cell := range k.Rows[i].Cells {
			n += len(cell.Chips)
		}
		if n != k.Rows[i].SongsTotal {
			t.Errorf("row %d: %d chips, %d songs", i, n, k.Rows[i].SongsTotal)
		}
		if len(c.Rows[i].Cells) != len(k.Rows[i].Cells) {
			t.Errorf("row %d: cells %d vs %d", i, len(c.Rows[i].Cells), len(k.Rows[i].Cells))
		}
	}
}

func TestBoard_DraftIDOnlyForDrafts(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs := board.New(l.St, func() time.Time { return l.Now })
	b := get(t, bs, board.Query{Detail: "chips", IncludeHidden: true})
	seen := 0
	for _, r := range b.Rows {
		for _, cell := range r.Cells {
			for _, ch := range cell.Chips {
				seen++
				if (ch.State == "draft") != (ch.DraftID != nil) {
					t.Errorf("chip %s state %s draft_id %v", ch.ID, ch.State, ch.DraftID)
				}
				if ch.State == "draft" && *ch.DraftID != l.DraftID {
					t.Errorf("draft chip names %s", *ch.DraftID)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no chips")
	}
	// the chip facts of the scene
	for _, r := range b.Rows {
		for _, cell := range r.Cells {
			for _, ch := range cell.Chips {
				switch ch.ID {
				case l.Picked:
					if ch.PickSource == nil || *ch.PickSource != "pinned" || ch.PickTakeID == nil || ch.Takes != 2 {
						t.Errorf("picked chip: %+v", ch)
					}
				case l.Rendered:
					if !ch.TempoMismatch || ch.PickSource != nil || ch.Queued {
						t.Errorf("rendered chip: %+v", ch)
					}
				case l.Queued:
					if !ch.Queued {
						t.Errorf("queued chip: %+v", ch)
					}
				}
			}
		}
	}
}

func TestBoard_UnassignedRow(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs := board.New(l.St, func() time.Time { return l.Now })
	b := get(t, bs, board.Query{Detail: "counts"})
	last := b.Rows[len(b.Rows)-1]
	if last.Muse != nil {
		t.Fatalf("the final row is the Unassigned row, got %+v", last.Muse)
	}
	// Muse A, Muse B, the draft's reference Muse and Unassigned: Unassigned never counts as a Muse
	if b.Totals.Muses != 3 || len(b.Rows) != 4 {
		t.Errorf("rows %d, totals.muses %d", len(b.Rows), b.Totals.Muses)
	}
	// stars-pick, queued and proposed Songs have no Muse
	if last.SongsTotal != 3 {
		t.Errorf("unassigned songs: %d", last.SongsTotal)
	}
	// one Muse filter: no Unassigned row
	one := get(t, bs, board.Query{MuseID: l.MuseA})
	if len(one.Rows) != 1 || one.Rows[0].Muse == nil || one.Rows[0].Muse.ID != l.MuseA || one.Totals.Songs != 2 {
		t.Errorf("muse filter: %+v", one.Rows)
	}
	// an unassigned-free library has no Unassigned row
	w := songstest.New(t)
	if _, err := w.Muses.Create(context.Background(), songstest.HumanP, museNew("Synthetic Lonely")); err != nil {
		t.Fatal(err)
	}
	e := get(t, board.New(w.St, func() time.Time { return w.Now }), board.Query{})
	if len(e.Rows) != 1 || e.Rows[0].Muse == nil || e.Rows[0].SongsTotal != 0 || e.Totals.Muses != 1 || e.Rows[0].Cells == nil {
		t.Errorf("an empty Muse is still a row: %+v", e.Rows)
	}
}

func TestBoard_FiltersTable(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs := board.New(l.St, func() time.Time { return l.Now })
	muse := func(b board.Board) []string {
		var out []string
		for _, r := range b.Rows {
			if r.Muse != nil {
				out = append(out, r.Muse.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	cases := []struct {
		name  string
		q     board.Query
		muses []string
		songs int
		unas  bool
	}{
		{"unfiltered", board.Query{}, nil, 7, true}, // muse ids are checked below
		{"angle", board.Query{Angle: "variant-a"}, []string{l.MuseA, l.RefMuse}, 2, false},
		{"state picked", board.Query{States: []string{"picked"}}, []string{l.MuseB}, 1, false},
		{"state judged and rendered", board.Query{States: []string{"judged", "rendered"}}, []string{l.MuseA}, 5, true},
		{"session", board.Query{SessionID: l.Session}, []string{l.MuseA, l.MuseB}, 6, true},
		{"sound or", board.Query{Sounds: map[string][]string{"genre_family": {"pop", "reggae"}}}, []string{l.MuseA, l.MuseB}, 3, false},
		{"sound and", board.Query{Sounds: map[string][]string{"genre_family": {"reggae"}, "vocal": {"male"}}}, []string{l.MuseA}, 1, false},
		{"q", board.Query{Q: "Synthetic Judged"}, []string{l.MuseA}, 1, false},
		{"q unassigned only", board.Query{Q: "Queued"}, nil, 1, true},
		{"no match", board.Query{Q: "zzz"}, nil, 0, false},
	}
	for _, c := range cases {
		b := get(t, bs, c.q)
		if c.name != "unfiltered" && !slices.Equal(muse(b), sorted(c.muses)) {
			t.Errorf("%s: muses %v, want %v", c.name, muse(b), c.muses)
		}
		if b.Totals.Songs != c.songs {
			t.Errorf("%s: songs %d, want %d", c.name, b.Totals.Songs, c.songs)
		}
		if _, has := rowOf(b, ""); has != c.unas {
			t.Errorf("%s: unassigned row %v, want %v", c.name, has, c.unas)
		}
		sum := 0
		for _, r := range b.Rows {
			sum += r.SongsTotal
		}
		if sum != b.Totals.Songs {
			t.Errorf("%s: rows sum %d, totals %d", c.name, sum, b.Totals.Songs)
		}
	}
	// the unfiltered Board keeps the draft's reference Muse, which has the draft Song
	b := get(t, bs, board.Query{})
	if len(b.Rows) != 4 || b.Totals.Muses != 3 {
		t.Errorf("unfiltered rows %d muses %d", len(b.Rows), b.Totals.Muses)
	}
	// include_hidden brings the hidden Song
	if h := get(t, bs, board.Query{IncludeHidden: true}); h.Totals.Songs != 8 {
		t.Errorf("include_hidden songs: %d", h.Totals.Songs)
	}
	// columns: variant-a, variant-b and "(none)"; by count desc, none last among equals
	if len(b.Columns) < 3 || b.Columns[0].Angle != nil {
		t.Errorf("columns: %+v", b.Columns)
	}
	for _, bad := range []board.Query{{Detail: "everything"}, {States: []string{"nope"}}, {Cursor: "%%"}, {UnassignedAfter: "%%", Detail: "chips"}} {
		if _, err := bs.Get(context.Background(), bad); err == nil {
			t.Errorf("%+v accepted", bad)
		} else if pe := (*problem.Error)(nil); !errors.As(err, &pe) || pe.Status != 422 {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func sorted(s []string) []string { slices.Sort(s); return s }

func museNew(title string) muses.NewMuse {
	return muses.NewMuse{Kind: muses.KindReference, Title: title}
}

func TestBoard_PageCap(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	ctx := context.Background()
	for i := 0; i < 400; i++ {
		if _, err := w.Muses.Create(ctx, songstest.HumanP, museNew(fmt.Sprintf("Synthetic Muse %03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	bs := board.New(w.St, func() time.Time { return w.Now })
	seen := map[string]bool{}
	cur, pages := "", 0
	for {
		b := get(t, bs, board.Query{Limit: 500, Cursor: cur})
		pages++
		if n := size(t, b); n > board.MaxPageBytes {
			t.Errorf("page %d is %d bytes", pages, n)
		}
		for _, r := range b.Rows {
			if seen[r.Muse.ID] {
				t.Errorf("row %s repeated", r.Muse.ID)
			}
			seen[r.Muse.ID] = true
		}
		if b.Totals.Muses != 400 {
			t.Errorf("totals.muses %d", b.Totals.Muses)
		}
		if b.NextCursor == nil {
			break
		}
		cur = *b.NextCursor
		if pages > 10 {
			t.Fatal("no end")
		}
	}
	if len(seen) != 400 || pages < 2 {
		t.Errorf("%d rows over %d pages: 400 rows must split at the cap", len(seen), pages)
	}
}

// unassignedWorld has n Song rows of no Muse, built straight in SQL (draft Songs).
func unassignedWorld(t *testing.T, n int) *songstest.World {
	t.Helper()
	w := songstest.New(t)
	for i := 0; i < n; i++ {
		d := w.Sc.Draft(fmt.Sprintf("syn_u%04d_v1", i), librarytest.DraftOpts{})
		if _, err := w.St.W.Exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,'draft','system:test',?,'system:test',?)`,
			w.NewID(), d, fmt.Sprintf("Synthetic Unassigned %04d", i), store.FormatTime(w.Now), store.FormatTime(w.Now)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func TestBoard_UnassignedCapped(t *testing.T) {
	t.Parallel()
	w := unassignedWorld(t, 1000)
	bs := board.New(w.St, func() time.Time { return w.Now })
	seen := map[string]bool{}
	cur, pages := "", 0
	for {
		b := get(t, bs, board.Query{Detail: "chips", UnassignedAfter: cur})
		pages++
		if n := size(t, b); n > board.MaxPageBytes {
			t.Errorf("page %d is %d bytes", pages, n)
		}
		if len(b.Rows) != 1 || b.Rows[0].Muse != nil {
			t.Fatalf("page %d rows: %+v", pages, b.Rows)
		}
		r := b.Rows[0]
		if r.SongsTotal != 1000 || r.Counts.Draft != 1000 || b.Totals.Songs != 1000 || b.Totals.Muses != 0 {
			t.Errorf("page %d: counts must stay complete: %+v total %d totals %+v", pages, r.Counts, r.SongsTotal, b.Totals)
		}
		n := 0
		for _, cell := range r.Cells {
			for _, ch := range cell.Chips {
				n++
				if seen[ch.ID] {
					t.Errorf("chip %s repeated", ch.ID)
				}
				seen[ch.ID] = true
			}
		}
		if n > board.UnassignedChips {
			t.Errorf("page %d has %d chips", pages, n)
		}
		if pages == 1 && (!r.ChipsMore || r.NextUnassignedCursor == nil || n != board.UnassignedChips) {
			t.Errorf("first page: %d chips, more %v", n, r.ChipsMore)
		}
		if !r.ChipsMore {
			if r.NextUnassignedCursor != nil {
				t.Error("cursor without more")
			}
			break
		}
		cur = *r.NextUnassignedCursor
		if pages > 10 {
			t.Fatal("no end")
		}
	}
	if len(seen) != 1000 || pages != 5 {
		t.Errorf("%d chips over %d pages, want 1000 over 5", len(seen), pages)
	}
	// the very first response (no cursor) is the same first page
	first := get(t, bs, board.Query{Detail: "chips"})
	if len(first.Rows) != 1 || !first.Rows[0].ChipsMore {
		t.Errorf("first response: %+v", first.Rows)
	}
}

func TestBoard_FiveStatements(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	bs, n := countedBoard(t, l.World)
	for _, q := range []board.Query{{}, {Detail: "chips"}, {Detail: "chips", Angle: "variant-a"}, {Detail: "chips", SessionID: l.Session, States: []string{"judged"}}, {UnassignedAfter: "e30"}} {
		q := q
		if q.UnassignedAfter != "" {
			q.UnassignedAfter = ""
		}
		before := n.Load()
		get(t, bs, q)
		if got := n.Load() - before; got > 5 || got < 3 {
			t.Errorf("%+v: %d statements, want <= 5", q, got)
		}
	}
}

func TestPerf_MuseBoard(t *testing.T) {
	budget := perftest.Budget(t, time.Second)
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir()+"/p.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	librarytest.FixtureL1Scale(t, st, librarytest.FixedNow, 200, 3)
	w := songstest.Over(t, st)
	w.Settle()
	bs := board.New(st, func() time.Time { return w.Now })
	var durs []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		cur := ""
		for {
			b := get(t, bs, board.Query{Detail: "chips", Cursor: cur})
			if b.NextCursor == nil {
				break
			}
			cur = *b.NextCursor
		}
		durs = append(durs, time.Since(start))
	}
	slices.Sort(durs)
	if p95 := durs[18]; p95 > budget {
		t.Errorf("p95 %v over budget %v", p95, budget)
	}
}
