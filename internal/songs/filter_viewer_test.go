package songs_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

// TestFilterWithoutViewerFailsClosed pins l2Clauses: a ListFilter that skipped WithViewer (empty Rater) binds auth.NoRater, so
// it matches no rating row. A rated Song is not treated as rated and a star range returns nothing, instead of reading every
// rater's rows.
func TestFilterWithoutViewerFailsClosed(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	raw := func(f songs.ListFilter) []string {
		t.Helper()
		where, args := songs.FilterSQL(f, "") // no WithViewer: Rater and OwnerEvents stay empty
		rows, err := w.st.R.Query(`SELECT s.id FROM songs s WHERE `+where, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		byID := map[string]string{}
		for l, id := range w.fx.Songs {
			byID[id] = l
		}
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			if l, ok := byID[id]; ok {
				out = append(out, l)
			}
		}
		slices.Sort(out)
		return out
	}
	// control: with the Owner's viewer the same fixture has S4 and S6 rated
	eq(t, "owner unrated=false", w.labels(songs.ListFilter{Unrated: &no}), "S4", "S6")
	eq(t, "viewer-less min_stars=1", raw(songs.ListFilter{MinStars: ip(1)}))
	eq(t, "viewer-less min_stars=3", raw(songs.ListFilter{MinStars: ip(3)}))
	eq(t, "viewer-less unrated=false (rated)", raw(songs.ListFilter{Unrated: &no}))
	eq(t, "viewer-less unrated=true", raw(songs.ListFilter{Unrated: &yes}), "S1", "S2", "S3", "S4", "S5", "S6")
}
