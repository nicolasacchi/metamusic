package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func adhRig(t *testing.T, realFeatures bool) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: realFeatures})
	r.Settle(t)
	return r
}

func getAdh(t *testing.T, c *servertest.Client, query string) (int, atlas.Adherence, map[string]any, []byte) {
	t.Helper()
	resp := c.Do(t, "GET", "/api/atlas/adherence"+query, nil)
	var a atlas.Adherence
	var problem map[string]any
	if resp.Status == 200 {
		if err := json.Unmarshal(resp.Body, &a); err != nil {
			t.Fatalf("%v: %s", err, resp.Body)
		}
	} else {
		_ = json.Unmarshal(resp.Body, &problem)
	}
	return resp.Status, a, problem, resp.Body
}

// TestGetAdherence_REST: an agent with read scope gets the derived answer over the settled L3 fixture; the takes' styles declare
// their measured tempo, so every pair is exact; no Muse text is in the body.
func TestGetAdherence_REST(t *testing.T) {
	t.Parallel()
	r := adhRig(t, true)
	agent, _ := r.Agent(t, auth.ScopeRead)
	status, a, _, body := getAdh(t, agent, "?limit=100")
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	if a.Coverage.WithPair < 8 || a.Summary.N != a.Coverage.WithPair || len(a.Items) != a.Summary.N || a.NextCursor != nil {
		t.Errorf("answer: coverage %+v summary %+v items %d", a.Coverage, a.Summary, len(a.Items))
	}
	if a.Summary.Within5PctShare != 1 || a.Summary.MedianErrorPct != 0 || a.Summary.OctaveShare != 0 {
		t.Errorf("fixture tempos are exact: %+v", a.Summary)
	}
	if len(a.DimsAvailable) == 0 || a.DimsAvailable[0] != "bpm" {
		t.Errorf("dims = %v", a.DimsAvailable)
	}
	for _, s := range []string{lt.L3Muse3Title, lt.L3Muse4Title, lt.L3Muse5Title, lt.L3Muse5Artist} {
		if strings.Contains(string(body), s) {
			t.Errorf("body carries Muse text %q", s)
		}
	}
	// filters
	if _, f, _, _ := getAdh(t, agent, "?octave=true"); len(f.Items) != 0 {
		t.Errorf("octave=true: %d items", len(f.Items))
	}
	if _, f, _, _ := getAdh(t, agent, "?min_error_pct=1"); len(f.Items) != 0 || f.Summary.N != a.Summary.N {
		t.Errorf("min_error_pct=1: %d items, summary %d", len(f.Items), f.Summary.N)
	}
	_, f, _, _ := getAdh(t, agent, "?facet=family&value=pop&min_n=1")
	if f.Coverage.Songs == 0 || len(f.ByFacet) != 1 || f.ByFacet[0].Value != "pop" {
		t.Errorf("facet=family&value=pop: %+v", f)
	}
	// paging
	_, p1, _, _ := getAdh(t, agent, "?limit=3")
	if len(p1.Items) != 3 || p1.NextCursor == nil {
		t.Fatalf("page 1: %d items, cursor %v", len(p1.Items), p1.NextCursor)
	}
	_, p2, _, _ := getAdh(t, agent, "?limit=3&cursor="+url.QueryEscape(*p1.NextCursor))
	if len(p2.Items) == 0 || p2.Items[0].SongID == p1.Items[0].SongID {
		t.Errorf("page 2 repeats page 1: %+v", p2.Items)
	}
	// a human reads the same
	if status, _, _, _ := getAdh(t, r.Human(t), ""); status != 200 {
		t.Errorf("human: %d", status)
	}
	// anonymous callers are refused
	resp, err := http.Get(r.URL + "/api/atlas/adherence")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
}

// TestGetAdherence_Validation: bad input is 422 with a stable code.
func TestGetAdherence_Validation(t *testing.T) {
	t.Parallel()
	r := adhRig(t, true)
	agent, _ := r.Agent(t, auth.ScopeRead)
	for q, code := range map[string]string{
		"?limit=0": "", "?limit=101": "", "?min_n=0": "", "?min_n=51": "", "?min_error_pct=-1": "",
		"?facet=bogus": "VALIDATION_FAILED", "?value=pop": "VALIDATION_FAILED", "?cursor=!!": "INVALID_CURSOR",
	} {
		status, _, p, _ := getAdh(t, agent, q)
		if status != 422 || (code != "" && p["code"] != code) {
			t.Errorf("%s: %d %v, want 422 %s", q, status, p, code)
		}
	}
}

// TestGetAdherence_NoFeatures: before any feature row exists the answer is a 200 with empty lists (never null).
func TestGetAdherence_NoFeatures(t *testing.T) {
	t.Parallel()
	r := adhRig(t, false)
	agent, _ := r.Agent(t, auth.ScopeRead)
	status, a, _, body := getAdh(t, agent, "")
	if status != 200 || a.Summary.N != 0 || len(a.Items) != 0 || !strings.Contains(string(body), `"dims_available":[]`) {
		t.Errorf("%d %s", status, body)
	}
}

// TestGetSong_FeaturesREST: get_song answers features over the settled L3 fixture: a rendered Song with measured tempo, energy and
// key; a draft Song with a declared tempo and no key; and null before any feature row exists.
func TestGetSong_FeaturesREST(t *testing.T) {
	t.Parallel()
	type feat struct {
		Tempo struct {
			BPM          *float64 `json:"bpm"`
			Basis        *string  `json:"basis"`
			RequestedBPM *float64 `json:"requested_bpm"`
			MeasuredBPM  *float64 `json:"measured_bpm"`
			Octave       bool     `json:"octave"`
		} `json:"tempo"`
		Energy struct {
			Value *float64 `json:"value"`
			Basis *string  `json:"basis"`
		} `json:"energy"`
		Key struct {
			Camelot *string `json:"camelot"`
			Name    *string `json:"name"`
			Basis   *string `json:"basis"`
		} `json:"key"`
		DurationRatio *float64 `json:"duration_ratio"`
		Subject       struct {
			Kind string  `json:"kind"`
			ID   string  `json:"id"`
			Note *string `json:"note"`
		} `json:"subject"`
		Pending bool `json:"pending"`
	}
	get := func(c *servertest.Client, id string) (*feat, string) {
		resp := c.Do(t, "GET", "/api/songs/"+id, nil)
		if resp.Status != 200 {
			t.Fatalf("get_song %s: %d %s", id, resp.Status, resp.Body)
		}
		var v struct {
			Features *feat `json:"features"`
		}
		if err := json.Unmarshal(resp.Body, &v); err != nil {
			t.Fatal(err)
		}
		return v.Features, string(resp.Body)
	}
	r := adhRig(t, true)
	agent, _ := r.Agent(t, auth.ScopeRead)
	song := func(r *servertest.Rig, label string) string {
		return dbStr(t, r, `SELECT id FROM songs WHERE title = 'Synthetic Song `+label+`'`)
	}
	take := dbStr(t, r, `SELECT subject_id FROM song_features WHERE song_id = '`+song(r, "T1")+`'`)
	f, _ := get(agent, song(r, "T1"))
	if f == nil || f.Tempo.BPM == nil || *f.Tempo.BPM != 96 || *f.Tempo.Basis != "measured" || f.Tempo.Octave || f.Energy.Value == nil || *f.Energy.Value != 0.3 ||
		f.Key.Camelot == nil || *f.Key.Camelot != "8A" || *f.Key.Name != "A minor" || f.Subject.Kind != "take" || f.Subject.ID != take || f.Pending {
		t.Errorf("T1 features = %+v", f)
	}
	f, _ = get(agent, song(r, "d1"))
	if f == nil || f.Tempo.BPM == nil || *f.Tempo.BPM != 76 || *f.Tempo.Basis != "declared" || f.Key.Camelot != nil || f.DurationRatio != nil || f.Subject.Kind != "draft" {
		t.Errorf("d1 features = %+v", f)
	}
	r2 := adhRig(t, false)
	agent2, _ := r2.Agent(t, auth.ScopeRead)
	if f, body := get(agent2, song(r2, "T1")); f != nil || !strings.Contains(body, `"features":null`) {
		t.Errorf("no row: %+v", f)
	}
}
