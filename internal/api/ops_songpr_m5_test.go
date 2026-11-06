package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func songPRRig(t *testing.T) (*rig, lt.M5Info) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fx := lt.FixtureM5(t, st, lt.FixedNow)
	now := func() time.Time { return lt.FixedNow }
	r := &rig{mux: http.NewServeMux(), st: st}
	api.New(r.mux, api.Deps{Store: st, Now: now, SongPR: songpr.New(songpr.Options{Store: st, Now: now})})
	return r, fx
}

func spBody(base string, comments []string, ops string) string {
	b, _ := json.Marshal(comments)
	return `{"base_track_id":"` + base + `","addresses_comment_ids":` + string(b) + `,"rationale":"r","ops":` + ops + `}`
}

// TestSongPR_REST: propose, get and list over REST on a real library (FixtureM5): shapes, the problem codes of C253.2 (one per
// code), scopes, validation.
func TestSongPR_REST(t *testing.T) {
	t.Parallel()
	r, fx := songPRRig(t)
	writer := m5Agent(auth.ScopeRead, auth.ScopeWrite)
	reader := m5Agent(auth.ScopeRead)
	g0, g2, cm := fx.Lineage[0], fx.Lineage[2], fx.Comments
	chorus := `[{"op":"replace_line","section_idx":1,"line_idx":0,"text":"a brand new hook line"}]`

	rec := r.callWith(writer, "POST", "/api/patches", spBody(g2, []string{cm["fix_chorus"]}, chorus), "Idempotency-Key", "sp-1")
	if rec.Code != 201 {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	id, _ := p["id"].(string)
	base, _ := p["base"].(map[string]any)
	diff, _ := p["diff"].(map[string]any)
	if id == "" || p["operator"] != "lyric_edit" || base["track_id"] != g2 || len(p["ops"].([]any)) != 1 || len(diff["lyrics"].([]any)) != 1 ||
		len(p["addresses"].([]any)) != 1 || len(p["renders"].([]any)) != 0 || p["title"] != nil {
		t.Errorf("patch = %v", p)
	}
	if c, _ := p["compiled"].(map[string]any); c["lyrics"] == nil || c["style"] != nil {
		t.Errorf("compiled = %v", p["compiled"])
	}
	got := r.get(t, reader, "/api/patches/"+id)
	if got["id"] != id || got["rationale"] != "r" {
		t.Errorf("get = %v", got)
	}
	list := r.get(t, reader, "/api/patches?track_id="+g2)
	if items := list["items"].([]any); len(items) != 1 || list["total"] != float64(1) || items[0].(map[string]any)["ops"] != nil || items[0].(map[string]any)["diff"] != nil {
		t.Errorf("list = %v", list)
	}
	if items := r.get(t, reader, "/api/patches?comment_id="+cm["fix_verse"])["items"].([]any); len(items) != 0 {
		t.Errorf("list by another comment = %v", items)
	}

	// one request per error code
	for i, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"hidden comment", spBody(g0, []string{cm["hidden_fix"]}, chorus), 422, "COMMENT_NOT_ADDRESSABLE"},
		{"out of bounds", spBody(g0, []string{cm["fix_chorus"]}, `[{"op":"replace_line","section_idx":2,"line_idx":0,"text":"x"}]`), 422, "PATCH_OUT_OF_BOUNDS"},
		{"invalid line", spBody(g0, []string{cm["fix_chorus"]}, `[{"op":"replace_line","section_idx":1,"line_idx":9,"text":"x"}]`), 422, "PATCH_INVALID"},
		{"no change", spBody(g0, []string{cm["fix_chorus"]}, `[{"op":"replace_line","section_idx":1,"line_idx":0,"text":"fixture hook b1"}]`), 422, "PATCH_NO_CHANGE"},
		{"reference base", spBody(lt.TrackRefFlac, []string{cm["fix_chorus"]}, chorus), 422, "RECIPE_UNKNOWN"},
		{"unknown base", spBody("nope", []string{cm["fix_chorus"]}, chorus), 422, "UNKNOWN_TRACK"},
		{"too many comments", spBody(g0, make([]string, 11), chorus), 422, "VALIDATION_FAILED"},
		{"text over 200 characters", spBody(g0, []string{cm["fix_chorus"]}, `[{"op":"replace_line","section_idx":1,"line_idx":0,"text":"`+strings.Repeat("x", 201)+`"}]`), 422, "VALIDATION_FAILED"},
	} {
		rec := r.callWith(writer, "POST", "/api/patches", c.body, "Idempotency-Key", "sp-err-"+strconv.Itoa(i))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if rec.Code != c.status || m["code"] != c.code || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: %d %q %s, want %d %s", c.name, rec.Code, rec.Header().Get("Content-Type"), rec.Body, c.status, c.code)
		}
		if c.code != "VALIDATION_FAILED" && m["details"] == nil {
			t.Errorf("%s: no details", c.name)
		}
	}
	r.problem(t, reader, "GET", "/api/patches/no-such-patch", "", 404, "NOT_FOUND")
	r.problem(t, reader, "GET", "/api/patches?cursor=garbage", "", 422, "VALIDATION_FAILED")
	if rec := r.callWith(reader, "POST", "/api/patches", spBody(g2, []string{cm["fix_chorus"]}, chorus), "Idempotency-Key", "sp-2"); rec.Code != 403 {
		t.Errorf("a read-only token proposed a patch: %d %s", rec.Code, rec.Body)
	}
	if n := countRows(t, r, `SELECT COUNT(*) FROM patches`); n != 1 {
		t.Errorf("refused proposals left %d patches", n)
	}
}

func countRows(t *testing.T, r *rig, q string) int {
	t.Helper()
	var n int
	if err := r.st.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
