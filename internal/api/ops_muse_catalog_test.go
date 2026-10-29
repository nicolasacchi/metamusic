package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestGetMuse_Catalog: get_muse.catalog is the Muse's automatic album {id, neutral_name}: present while the Muse has Songs
// outside published Records, null otherwise or when hidden, and the name stays neutral with subsonic_muse_names on.
func TestGetMuse_Catalog(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	c := r.Human(t)
	museID := func(title string) string {
		var id string
		if err := r.Store.R.QueryRow(`SELECT id FROM muses WHERE title = ?`, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	m1, m2 := museID(librarytest.L2Muse1Title), museID(librarytest.L2Muse2Title)
	catalog := func(id string) any {
		t.Helper()
		body, _ := c.OK(t, "GET", "/api/muses/"+id, nil).(map[string]any)
		cat, present := body["catalog"]
		if !present {
			t.Fatalf("get_muse has no catalog field: %v", body)
		}
		return cat
	}
	want := map[string]any{"id": "mus-" + m1, "neutral_name": "Inspiration 001"}
	if got := catalog(m1); !equalJSON(got, want) {
		t.Errorf("M1 catalog = %v, want %v", got, want)
	}
	if got := catalog(m2); got != nil {
		t.Errorf("M2 holds only published Songs, catalog = %v", got)
	}
	// the opt-in names the Subsonic album, never get_muse.catalog
	c.OK(t, "PUT", "/human/api/curation/settings", map[string]any{"subsonic_muse_names": true})
	if got := catalog(m1); !equalJSON(got, want) {
		t.Errorf("flag on: catalog = %v, want the neutral %v", got, want)
	}
	// a hidden Muse has no album
	var rev int
	if err := r.Store.R.QueryRow(`SELECT revision FROM muses WHERE id = ?`, m1).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	c.OK(t, "PATCH", "/api/muses/"+m1, map[string]any{"revision": rev, "hidden": true})
	if got := catalog(m1); got != nil {
		t.Errorf("hidden Muse catalog = %v", got)
	}
}

func equalJSON(a, b any) bool {
	x, ok1 := a.(map[string]any)
	y, ok2 := b.(map[string]any)
	if !ok1 || !ok2 || len(x) != len(y) {
		return false
	}
	for k, v := range y {
		if x[k] != v {
			return false
		}
	}
	return true
}
