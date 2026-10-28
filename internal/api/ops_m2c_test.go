package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// newRigM2c is newRig over lt.FixtureM2c (a reference FLAC and a lossy reference are added).
func newRigM2c(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureM2c(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return lt.FixedNow }
	r := &rig{mux: http.NewServeMux(), signer: serve.NewSigner(testKey, now), svc: library.New(st, root, now), st: st}
	api.New(r.mux, api.Deps{Store: st, Library: r.svc, Signer: r.signer, Now: now})
	return r
}

func TestOpGetMediaLinks_Lossy(t *testing.T) {
	t.Parallel()
	r := newRigM2c(t)
	m := r.get(t, human, "/api/tracks/"+lt.TrackLossy+"/media")
	if v, ok := m["flac"]; !ok || v != nil {
		t.Errorf("flac must be present and null: %v (present %v)", v, ok)
	}
	if m["lossy"] != true {
		t.Errorf("lossy: %v", m["lossy"])
	}
	mp3 := m["mp3"].(map[string]any)
	if !strings.HasPrefix(mp3["url"].(string), "/media/") || !strings.Contains(mp3["url"].(string), ".mp3?") || mp3["content_type"] != "audio/mpeg" || mp3["bitrate_kbps"] != float64(128) {
		t.Errorf("mp3: %v", mp3)
	}
	// a normal track still has both links and lossy=false
	n := r.get(t, human, "/api/tracks/"+lt.TrackLuna+"/media")
	if n["flac"] == nil || n["lossy"] != false {
		t.Errorf("normal track: flac=%v lossy=%v", n["flac"], n["lossy"])
	}
	// the reference FLAC keeps its lossless link
	f := r.get(t, human, "/api/tracks/"+lt.TrackRefFlac+"/media")
	if f["flac"] == nil || f["lossy"] != false {
		t.Errorf("reference flac: %v", f)
	}
}

func TestOpGetMediaLinks_FlacSchemaNullable(t *testing.T) {
	t.Parallel()
	all := schemas(openapiDoc(t))
	var comp map[string]any
	for name, s := range all {
		if props, ok := s.(map[string]any)["properties"].(map[string]any); ok && props["lossy"] != nil && props["flac"] != nil && props["mp3"] != nil && props["ttl_s"] != nil {
			comp = s.(map[string]any)
			_ = name
		}
	}
	if comp == nil {
		t.Fatal("media links component not found")
	}
	fl := comp["properties"].(map[string]any)["flac"].(map[string]any)
	types := typeSet(fl)
	if !has(types, "object") || !has(types, "null") {
		t.Errorf("flac must be a nullable object: %v", fl)
	}
}

func TestOpSearchTracks_Filters(t *testing.T) {
	t.Parallel()
	r := newRigM2c(t)
	got := func(q string) []string {
		ids := itemIDs(r.get(t, human, "/api/tracks?"+q))
		slices.Sort(ids)
		return ids
	}
	if want := []string{lt.TrackRefFlac, lt.TrackLossy}; !slices.Equal(got("recipe_known=false"), sortedCopy(want)) {
		t.Errorf("recipe_known=false: %v", got("recipe_known=false"))
	}
	if want := []string{lt.TrackLossy}; !slices.Equal(got("lossy=true"), want) {
		t.Errorf("lossy=true: %v", got("lossy=true"))
	}
	if g := got("lossy=false&recipe_known=true"); len(g) != 5 || slices.Contains(g, lt.TrackLossy) || slices.Contains(g, lt.TrackRefFlac) {
		t.Errorf("known lossless: %v", g)
	}
	page := r.get(t, human, "/api/tracks?lossy=true")
	it := page["items"].([]any)[0].(map[string]any)
	if it["lossy"] != true || it["recipe_known"] != false {
		t.Errorf("item flags: %v", it)
	}
	d := r.get(t, human, "/api/tracks/"+lt.TrackLossy)
	if d["lossy"] != true || d["recipe_known"] != false || d["audio"].(map[string]any)["mp3"] != nil {
		t.Errorf("detail: lossy=%v known=%v mp3=%v", d["lossy"], d["recipe_known"], d["audio"].(map[string]any)["mp3"])
	}
	if d["recipe"].(map[string]any)["engine"] != "unknown" {
		t.Errorf("recipe: %v", d["recipe"])
	}
	r.problem(t, human, "GET", "/api/tracks?lossy=maybe", "", 422, "VALIDATION_FAILED")
}

func sortedCopy(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

// TestRegistry_OpCount pins the registry size: 58 operations after M2c plus the 23 of M3 (C81) plus the 18 of L1 (C96), the 2 songwriting-target operations and the 8 of M4a (C113).
func TestRegistry_OpCount(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"operationId"`); n != 126 {
		t.Fatalf("operation count = %d, want 126 (81 + 18 of L1 + 2 songwriting settings + 8 of M4a + 17 of L2)", n)
	}
}
