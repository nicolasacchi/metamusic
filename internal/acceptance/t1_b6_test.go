package acceptance_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func tracksByOrigin(t *testing.T, h *servertest.Client) map[string]float64 {
	t.Helper()
	s := obj(h.OK(t, "GET", "/api/stats", nil))
	out := map[string]float64{}
	for k, v := range obj(obj(s["tracks"])["by_origin"]) {
		out[k] = v.(float64)
	}
	return out
}

// TestStats_T1: a transform Take counts under its own origin in get_stats; the job and import counts and every generation
// counter are unchanged by it (the stored origin stays 'job').
func TestStats_T1(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"})
	h := r.Human(t)
	before := tracksByOrigin(t, h)
	genBefore := fmt.Sprint(obj(h.OK(t, "GET", "/api/stats", nil))["generation"])
	insertDSPTake(t, r, "glacier")
	r.Settle(t)
	after := tracksByOrigin(t, h)
	if after["transform"] != before["transform"]+1 || after["job"] != before["job"] || after["import"] != before["import"] {
		t.Errorf("by_origin %v -> %v, want transform +1 and the others unchanged", before, after)
	}
	if got := fmt.Sprint(obj(h.OK(t, "GET", "/api/stats", nil))["generation"]); got != genBefore {
		t.Errorf("generation counters moved: %s -> %s", genBefore, got)
	}
}

// TestExport_SkipsDsp: an export that names a transform Take explicitly still writes no row for it, and the same selection
// without it is byte-identical.
func TestExport_SkipsDsp(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"})
	h := r.Human(t)
	var imported string
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes c ON c.id = t.recipe_id WHERE c.engine = 'yue2' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	export := func(key string, ids ...string) (string, float64) {
		t.Helper()
		resp := h.Do(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": ids}}, "Idempotency-Key", key)
		if resp.Status/100 != 2 {
			t.Fatalf("export answered %d: %s", resp.Status, resp.Body)
		}
		m := obj(resp.JSON(t))
		return fmt.Sprint(m["sha256"]), obj(m["jsonl"])["rows"].(float64)
	}
	shaBefore, rowsBefore := export("skips-before", imported)
	dsp, _ := insertDSPTake(t, r, "polytempo")
	r.Settle(t)
	sha, rows := export("skips-after", imported, dsp)
	if sha != shaBefore || rows != rowsBefore || rows == 0 {
		t.Errorf("export with a transform Take: %s (%v rows), want %s (%v rows)", sha, rows, shaBefore, rowsBefore)
	}
}

// TestSearch_OriginTransform_REST: the REST filter lists the transform Take, with origin and the transform block on the
// row, and origin=job does not.
func TestSearch_OriginTransform_REST(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"})
	h := r.Human(t)
	id, _ := insertDSPTake(t, r, "glacier")
	r.Settle(t)
	page := obj(h.OK(t, "GET", "/api/tracks?origin=transform", nil))
	items, _ := page["items"].([]any)
	if len(items) != 1 || obj(items[0])["id"] != id || obj(items[0])["origin"] != "transform" || obj(obj(items[0])["transform"])["kind"] != "glacier" {
		t.Errorf("origin=transform: %v", page)
	}
	for _, it := range obj(h.OK(t, "GET", "/api/tracks?origin=job&limit=100", nil))["items"].([]any) {
		if obj(it)["id"] == id {
			t.Error("origin=job lists the transform Take")
		}
	}
	d := obj(h.OK(t, "GET", "/api/tracks/"+id, nil))
	if d["origin"] != "transform" || obj(d["transform"])["transform_id"] != id {
		t.Errorf("detail origin=%v transform=%v", d["origin"], d["transform"])
	}
	ctxDoc := obj(h.OK(t, "GET", "/api/tracks/"+id+"/context", nil))
	if obj(ctxDoc["transform"])["kind"] != "glacier" {
		t.Errorf("context transform = %v", ctxDoc["transform"])
	}
}

// TestGuards_TransformsNeverSpend: writing a transform Take adds no row to any money or backend table (the import-graph
// half, "internal/transform reaches no spend, provider, jobs, generation or upload", is TestImportGraph_T1).
func TestGuards_TransformsNeverSpend(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"})
	tables := []string{"ledger", "batches", "jobs", "estimates", "provider_state"}
	count := func() map[string]int {
		out := map[string]int{}
		for _, tb := range tables {
			var n int
			if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
				t.Fatalf("%s: %v", tb, err)
			}
			out[tb] = n
		}
		return out
	}
	before := count()
	insertDSPTake(t, r, "master")
	r.Settle(t)
	if after := count(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("row counts %v -> %v", before, after)
	}
}

// TestAcceptance_T1_JoinsLibrary (library part): a transform Take joins its parent's Song, gets Sound facets from the nearest
// non-dsp ancestor and measured-only features, and leaves the Song's Pick alone. The tape row of the plan's test is B5's.
func TestAcceptance_T1_JoinsLibrary(t *testing.T) {
	r := l3Rig(t, servertest.Options{Backend: "fake"})
	h := r.Human(t)
	id, parent := insertDSPTake(t, r, "glacier")
	var song, pickBefore string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, parent).Scan(&song); err != nil {
		t.Fatal(err)
	}
	_ = r.Store.R.QueryRow(`SELECT COALESCE(take_id, '') FROM song_picks WHERE song_id = ?`, song).Scan(&pickBefore)
	r.Settle(t)
	var inSong string
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, id).Scan(&inSong); err != nil || inSong != song {
		t.Fatalf("the transform Take is in Song %q (%v), want %q", inSong, err, song)
	}
	page := obj(h.OK(t, "GET", "/api/tracks?song_id="+song, nil))
	found := false
	for _, it := range page["items"].([]any) {
		found = found || obj(it)["id"] == id
	}
	if !found {
		t.Error("search_tracks song_id does not list the transform Take")
	}
	var engine string
	if err := r.Store.R.QueryRow(`SELECT value FROM take_sounds WHERE track_id = ? AND facet = 'engine'`, id).Scan(&engine); err != nil || engine != "dsp" {
		t.Errorf("engine facet = %q (%v)", engine, err)
	}
	var basis sql.NullString
	if err := r.Store.R.QueryRow(`SELECT energy_basis FROM take_features WHERE track_id = ?`, id).Scan(&basis); err == nil && basis.Valid && basis.String != "measured" {
		t.Errorf("transform Take energy basis = %q, want measured only", basis.String)
	}
	var pickAfter string
	_ = r.Store.R.QueryRow(`SELECT COALESCE(take_id, '') FROM song_picks WHERE song_id = ?`, song).Scan(&pickAfter)
	if pickAfter == id || pickAfter != pickBefore && pickBefore != "" {
		t.Errorf("Pick moved from %q to %q", pickBefore, pickAfter)
	}
}
