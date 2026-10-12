package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human    = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}, Via: "proxy"}
	agent    = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "agent-1", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
	readOnly = auth.Principal{Kind: auth.KindAgent, ID: "tok-2", Name: "ro", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

type rig struct {
	mux    *http.ServeMux
	signer *serve.Signer
	svc    *library.Service
	st     *store.Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.Fixture(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return lt.FixedNow }
	r := &rig{mux: http.NewServeMux(), signer: serve.NewSigner(testKey, now), svc: library.New(st, root, now), st: st}
	api.New(r.mux, api.Deps{Store: st, Library: r.svc, Signer: r.signer, Now: now,
		Probe: &serve.Object{RelPath: "derived/probe/probe-v1.flac", ContentType: "audio/flac", ETag: strings.Repeat("ab", 32)}})
	return r
}

func (r *rig) call(p auth.Principal, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), p))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *rig) get(t *testing.T, p auth.Principal, path string) map[string]any {
	t.Helper()
	w := r.call(p, "GET", path, "")
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (r *rig) problem(t *testing.T, p auth.Principal, method, path, body string, status int, code string) map[string]any {
	t.Helper()
	w := r.call(p, method, path, body)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != status || m["code"] != code || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%s %s: %d %q %s (want %d %s)", method, path, w.Code, w.Header().Get("Content-Type"), w.Body, status, code)
	}
	return m
}

func itemIDs(m map[string]any) []string {
	var out []string
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

type opWant struct {
	id, method, path, mcp, ui string
	scope                     auth.Scope
	explode                   []string
}

var m1Ops = []opWant{
	{"search_tracks", "GET", "/api/tracks", "tool", "/", auth.ScopeRead, []string{"status", "style"}},
	{"get_library_facets", "GET", "/api/library/facets", "generic", "/", auth.ScopeRead, nil},
	{"get_track", "GET", "/api/tracks/{id}", "tool", "/tracks/:id", auth.ScopeRead, nil},
	{"get_track_peaks", "GET", "/api/tracks/{id}/peaks", "generic", "/tracks/:id", auth.ScopeRead, nil},
	{"get_media_links", "GET", "/api/tracks/{id}/media", "tool", "/tracks/:id", auth.ScopeRead, nil},
	{"list_drafts", "GET", "/api/drafts", "tool", "/backlog", auth.ScopeRead, nil},
	{"get_draft", "GET", "/api/drafts/{id}", "tool", "/backlog/:id", auth.ScopeRead, nil},
	{"get_stats", "GET", "/api/stats", "tool", "/", auth.ScopeRead, nil},
	{"list_import_runs", "GET", "/api/imports", "generic", "/import", auth.ScopeRead, nil},
	{"get_import_run", "GET", "/api/imports/{id}", "generic", "/import", auth.ScopeRead, nil},
	{"get_device_probe", "GET", "/api/device-probe", "generic", "/", auth.ScopeRead, nil},
	{"put_device_profile", "PUT", "/api/device-profiles/{device_id}", "generic", "/settings", auth.ScopeWrite, nil},
	{"list_device_profiles", "GET", "/api/device-profiles", "generic", "/settings", auth.ScopeRead, nil},
}

func TestOps_RegisteredWithMeta(t *testing.T) {
	mux := http.NewServeMux()
	a := api.New(mux, api.Deps{})
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Parameters  []struct {
				Name    string `json:"name"`
				In      string `json:"in"`
				Explode *bool  `json:"explode"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	for _, w := range m1Ops {
		op, ok := byID[w.id]
		if !ok {
			t.Errorf("%s not registered", w.id)
			continue
		}
		m := op.Meta
		if op.Method != w.method || op.Path != w.path || m.MCP != w.mcp || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" || m.HumanOnly {
			t.Errorf("%s = %+v", w.id, op)
		}
		var exploded []string
		for _, p := range spec.Paths[w.path][strings.ToLower(w.method)].Parameters {
			if p.In == "query" && p.Explode != nil && *p.Explode {
				exploded = append(exploded, p.Name)
			}
		}
		slices.Sort(exploded)
		if !slices.Equal(exploded, w.explode) {
			t.Errorf("%s explode = %v, want %v", w.id, exploded, w.explode)
		}
	}
	if len(m1Ops) != 13 {
		t.Fatalf("expected 13 M1 ops, table has %d", len(m1Ops))
	}
}

func TestOps_NilDepsUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	api.New(mux, api.Deps{}) // zero Deps must register every op without panicking
	r := &rig{mux: mux}
	put := `{"label":"x","user_agent":"u","quality":"auto","quality_source":"default","probe_result":"none","flac_can_play":"","probe_version":1}`
	for _, tc := range []struct {
		method, path, body, component string
	}{
		{"GET", "/api/tracks", "", "library"},
		{"GET", "/api/library/facets", "", "library"},
		{"GET", "/api/tracks/x", "", "library"},
		{"GET", "/api/tracks/x/peaks", "", "library"},
		{"GET", "/api/tracks/x/media", "", "library"},
		{"GET", "/api/drafts", "", "library"},
		{"GET", "/api/drafts/x", "", "library"},
		{"GET", "/api/stats", "", "library"},
		{"GET", "/api/imports", "", "library"},
		{"GET", "/api/imports/x", "", "library"},
		{"GET", "/api/device-probe", "", "signer"},
		{"PUT", "/api/device-profiles/device-0001", put, "library"},
		{"GET", "/api/device-profiles", "", "library"},
	} {
		m := r.problem(t, human, tc.method, tc.path, tc.body, 503, "UNAVAILABLE")
		if d := m["details"].(map[string]any); d["component"] != tc.component {
			t.Errorf("%s %s: component = %v, want %s", tc.method, tc.path, d["component"], tc.component)
		}
	}
	// partial wiring names the missing part
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mux2 := http.NewServeMux()
	api.New(mux2, api.Deps{Library: library.New(st, nil, nil)})
	r2 := &rig{mux: mux2}
	m := r2.problem(t, human, "GET", "/api/tracks/x/media", "", 503, "UNAVAILABLE")
	if m["details"].(map[string]any)["component"] != "signer" {
		t.Errorf("media without signer: %v", m)
	}
	mux3 := http.NewServeMux()
	api.New(mux3, api.Deps{Signer: serve.NewSigner(testKey, nil)})
	m = (&rig{mux: mux3}).problem(t, human, "GET", "/api/device-probe", "", 503, "UNAVAILABLE")
	if m["details"].(map[string]any)["component"] != "probe" {
		t.Errorf("probe without file: %v", m)
	}
}

func TestSearchTracks_REST(t *testing.T) {
	r := newRig(t)
	m := r.get(t, agent, "/api/tracks")
	if m["total"] != float64(5) || m["next_cursor"] != nil {
		t.Fatalf("list: %v", m)
	}
	if got := itemIDs(m); got[0] != lt.TrackShort {
		t.Errorf("order %v", got)
	}
	// repeated keys (explode) are AND for style and OR for status
	m = r.get(t, agent, "/api/tracks?style=ambient&style=drone")
	if got := itemIDs(m); !slices.Equal(got, []string{lt.TrackShort}) {
		t.Errorf("style AND: %v", got)
	}
	m = r.get(t, agent, "/api/tracks?status=final&status=draft&limit=2")
	if len(itemIDs(m)) != 2 || m["next_cursor"] == nil || m["total"] != float64(5) {
		t.Errorf("status OR + limit: %v", m)
	}
	m = r.get(t, agent, "/api/tracks?has_score=true&truncated=false&min_duration_s=10")
	if got := itemIDs(m); !slices.Equal(got, []string{lt.TrackLuna}) {
		t.Errorf("typed filters: %v", got)
	}
	m = r.get(t, agent, "/api/tracks?q="+url.QueryEscape("Città"))
	if got := itemIDs(m); !slices.Equal(got, []string{lt.TrackCitta}) {
		t.Errorf("q: %v", got)
	}
	it := m["items"].([]any)[0].(map[string]any)
	if it["has_score"] != false || it["truncated"] != nil || it["integrated_lufs"] != -15.5 || it["bandwidth_hz"] != float64(12000) || it["parent_track_id"] != nil {
		t.Errorf("summary fields: %v", it)
	}
	r.problem(t, agent, "GET", "/api/tracks?recorded_from=yesterday", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/tracks?cursor=!!", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/tracks?status=bogus", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/tracks?limit=1000", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/tracks?style=a&style=b&style=c&style=d&style=e&style=f", "", 422, "VALIDATION_FAILED")
	r.get(t, readOnly, "/api/tracks")
}

func TestGetTrackAndFacetsAndPeaks_REST(t *testing.T) {
	r := newRig(t)
	m := r.get(t, agent, "/api/tracks/"+lt.TrackLuna)
	if m["title"] != "Luna Park" || m["timed"] != true || m["audio"].(map[string]any)["mp3"] == nil {
		t.Errorf("track: %v", m)
	}
	r.problem(t, agent, "GET", "/api/tracks/nope", "", 404, "NOT_FOUND")
	f := r.get(t, agent, "/api/library/facets")
	if len(f["style_terms"].([]any)) == 0 || f["duration_s"].(map[string]any)["max"] != float64(12) {
		t.Errorf("facets: %v", f)
	}
	p := r.get(t, agent, "/api/tracks/"+lt.TrackLuna+"/peaks")
	if p["level"] != "overview" || p["peaks"].(map[string]any)["length"] != float64(4) {
		t.Errorf("peaks: %v", p)
	}
	if p := r.get(t, agent, "/api/tracks/"+lt.TrackLuna+"/peaks?level=detail"); p["peaks"].(map[string]any)["samples_per_pixel"] != float64(2400) {
		t.Errorf("detail: %v", p)
	}
	r.problem(t, agent, "GET", "/api/tracks/"+lt.TrackLuna+"/peaks?level=huge", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/tracks/nope/peaks", "", 404, "NOT_FOUND")
}

func TestGetMediaLinks_TTLByPrincipal(t *testing.T) {
	r := newRig(t)
	path := "/api/tracks/" + lt.TrackLuna + "/media"
	// fixture clock: 2026-10-06T12:00:00Z
	h := r.get(t, human, path)
	a := r.get(t, agent, path)
	if h["expires_at"] != "2026-10-07T00:00:00Z" || h["ttl_s"] != float64(43200) {
		t.Errorf("human: %v %v", h["expires_at"], h["ttl_s"])
	}
	if a["expires_at"] != "2026-10-06T12:15:00Z" || a["ttl_s"] != float64(900) {
		t.Errorf("agent: %v %v", a["expires_at"], a["ttl_s"])
	}
	for _, c := range []struct {
		m    map[string]any
		kind string
	}{{h, "flac"}, {h, "mp3"}, {a, "flac"}, {a, "mp3"}} {
		l := c.m[c.kind].(map[string]any)
		u, err := url.Parse(l["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		sha := l["sha256"].(string)
		if u.Path != "/media/"+sha+"."+c.kind {
			t.Errorf("path %s", u.Path)
		}
		if err := r.signer.Verify(sha, c.kind, u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
			t.Errorf("%s link does not verify: %v", c.kind, err)
		}
	}
	fl, ml := h["flac"].(map[string]any), h["mp3"].(map[string]any)
	if fl["content_type"] != "audio/flac" || fl["sample_rate"] != float64(48000) || fl["bits_per_sample"] != float64(24) ||
		ml["content_type"] != "audio/mpeg" || ml["bitrate_kbps"] != float64(320) {
		t.Errorf("media metadata %v %v", fl, ml)
	}
	r.problem(t, human, "GET", "/api/tracks/nope/media", "", 404, "NOT_FOUND")
}

func TestGetMediaLinks_MediaUnavailable(t *testing.T) {
	r := newRig(t)
	// a track without renditions answers 409 MEDIA_UNAVAILABLE
	ctx := context.Background()
	tx, _ := r.st.W.BeginTx(ctx, nil)
	var rid string
	if err := tx.QueryRow(`SELECT recipe_id FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	tk := library.NewTake{
		ID: lt.TrackIDForTest(1), Title: "Bare", Slug: "bare", RecipeID: rid, DurationS: 1, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 48000,
		MasterPath: "audio/masters/bare.flac", MasterSHA256: lt.Sha("bare-m"), MasterBytes: 1, SourceSHA256: lt.Sha("bare-s"), FramesSHA256: lt.Sha("bare-f"),
		AudioMD5: strings.Repeat("0", 32), ImportRule: "slug", PrincipalID: "cli", RecordedAt: lt.FixedNow, ToolVersion: "ffmpeg n9.0.1",
	}
	if err := r.svc.InsertTake(ctx, tx, tk); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r.problem(t, human, "GET", "/api/tracks/"+tk.ID+"/media", "", 409, "MEDIA_UNAVAILABLE")
	r.problem(t, human, "GET", "/api/tracks/"+tk.ID+"/peaks", "", 409, "MEDIA_UNAVAILABLE")
}

func TestGetDraft_ReferenceHumanOnly(t *testing.T) {
	r := newRig(t)
	id := r.get(t, agent, "/api/drafts")["items"].([]any)[0].(map[string]any)["id"].(string)
	h := r.get(t, human, "/api/drafts/"+id)
	a := r.get(t, agent, "/api/drafts/"+id)
	if h["has_reference"] != true || h["reference"].(map[string]any)["variant_type"] != "slower" {
		t.Errorf("human: %v", h["reference"])
	}
	if a["has_reference"] != true || a["reference"] != nil {
		t.Errorf("agent must not see the reference: %v", a)
	}
	if b := r.call(agent, "GET", "/api/drafts/"+id, "").Body.String(); strings.Contains(b, "Synthetic Reference Title") || strings.Contains(b, "slower") {
		t.Errorf("agent response leaks reference: %s", b)
	}
	if _, ok := h["request"].(map[string]any)["reference"]; ok {
		t.Errorf("reference inside request")
	}
	r.problem(t, human, "GET", "/api/drafts/nope", "", 404, "NOT_FOUND")
}

func TestListDrafts_REST(t *testing.T) {
	r := newRig(t)
	m := r.get(t, agent, "/api/drafts")
	if m["total"] != float64(6) || m["groups_total"] != float64(2) {
		t.Errorf("drafts: %v", m)
	}
	if m := r.get(t, agent, "/api/drafts?collection=pilot-x"); m["total"] != float64(3) {
		t.Errorf("collection: %v", m)
	}
	if m := r.get(t, agent, "/api/drafts?status=fulfilled"); m["total"] != float64(0) {
		t.Errorf("fulfilled: %v", m)
	}
	r.problem(t, agent, "GET", "/api/drafts?status=bogus", "", 422, "VALIDATION_FAILED")
}

func TestGetStats_REST(t *testing.T) {
	r := newRig(t)
	m := r.get(t, agent, "/api/stats")
	tr := m["tracks"].(map[string]any)
	l := m["loudness"].(map[string]any)
	b := m["backlog"].(map[string]any)
	if tr["total"] != float64(5) || tr["by_status"].(map[string]any)["draft"] != float64(5) || b["drafts"] != float64(6) || b["groups"] != float64(2) ||
		l["spread_lu"] != float64(4) || l["listen_flac_recommended"] != true {
		t.Errorf("stats: %v", m)
	}
}

func TestDeviceProfile_REST(t *testing.T) {
	r := newRig(t)
	body := `{"label":"Phone","user_agent":"UA","quality":"auto","quality_source":"default","probe_result":"none","flac_can_play":"","probe_version":1}`
	w := r.call(agent, "PUT", "/api/device-profiles/phone-abcdef01", body)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	body2 := `{"label":"Phone","user_agent":"UA","quality":"high","quality_source":"probe","probe_result":"failed","flac_can_play":"maybe","flac_decode_ok":false,"probe_version":1,"probed_at":"2026-10-06T12:00:00Z"}`
	w = r.call(agent, "PUT", "/api/device-profiles/phone-abcdef01", body2)
	var d map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != 200 || d["effective_format"] != "mp3" || d["flac_decode_ok"] != false || d["flac_seek_ok"] != nil {
		t.Fatalf("second put: %d %s", w.Code, w.Body)
	}
	list := r.get(t, agent, "/api/device-profiles")
	if n := len(list["items"].([]any)); n != 2 { // fixture device + this one
		t.Errorf("profiles: %d", n)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/device-profiles/short", body},
		{"/api/device-profiles/bad%20device%20id", body},
		{"/api/device-profiles/phone-abcdef01", strings.Replace(body, `"auto"`, `"ultra"`, 1)},
		{"/api/device-profiles/phone-abcdef01", strings.Replace(body, `"label":"Phone",`, "", 1)},
		{"/api/device-profiles/phone-abcdef01", strings.Replace(body2, "2026-10-06T12:00:00Z", "soon", 1)},
	} {
		r.problem(t, agent, "PUT", c.path, c.body, 422, "VALIDATION_FAILED")
	}
	r.problem(t, readOnly, "PUT", "/api/device-profiles/phone-abcdef01", body, 403, "FORBIDDEN_SCOPE")
}

func TestImportRuns_REST(t *testing.T) {
	r := newRig(t)
	runs := r.get(t, agent, "/api/imports")
	if len(runs["items"].([]any)) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	m := r.get(t, agent, "/api/imports/"+lt.RunID)
	if m["section"] != "errors" || m["total"] != float64(1) || m["run"].(map[string]any)["state"] != "completed" {
		t.Errorf("default section: %v", m)
	}
	if m := r.get(t, agent, "/api/imports/"+lt.RunID+"?section=unmatched"); m["total"] != float64(1) {
		t.Errorf("unmatched: %v", m)
	}
	r.problem(t, agent, "GET", "/api/imports/"+lt.RunID+"?section=inventory", "", 422, "VALIDATION_FAILED")
	r.problem(t, agent, "GET", "/api/imports/nope", "", 404, "NOT_FOUND")
}

func TestDeviceProbe_REST(t *testing.T) {
	r := newRig(t)
	m := r.get(t, agent, "/api/device-probe")
	u, _ := url.Parse(m["url"].(string))
	if m["version"] != float64(1) || m["content_type"] != "audio/flac" || m["sample_rate"] != float64(48000) || m["bits_per_sample"] != float64(24) ||
		m["channels"] != float64(2) || m["duration_s"] != 3.0 || m["seek_to_s"] != 1.5 || u.Path != "/media/"+strings.Repeat("ab", 32)+".flac" {
		t.Errorf("probe: %v", m)
	}
	if err := r.signer.Verify(strings.Repeat("ab", 32), "flac", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
		t.Errorf("probe link: %v", err)
	}
}

func TestSystemInfoUsesDepsNow(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Store: st, Started: lt.FixedNow.Add(-90 * time.Second), Now: func() time.Time { return lt.FixedNow }})
	m := (&rig{mux: mux}).get(t, agent, "/api/system")
	if m["uptime_s"] != float64(90) || m["started_at"] != "2026-10-06T11:58:30Z" {
		t.Errorf("system: %v", m)
	}
}
