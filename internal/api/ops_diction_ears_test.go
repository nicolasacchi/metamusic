package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestGetTrackDiction_REST: a take with neither a listening check nor timed sections is 409 with analyzer tier_b; Luna's plan
// timing gives the proportional basis; her listening check gives asr; Owner, Editor and a read agent see the same body, a Listener
// is refused by scope; unknown take is 404; a bad cursor is 422; the GET writes nothing.
func TestGetTrackDiction_REST(t *testing.T) {
	t.Parallel()
	r := earsBRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	agent, _ := r.Agent(t, auth.ScopeRead)
	path := "/api/tracks/" + lt.TrackLuna + "/diction"

	resp := owner.Do(t, "GET", "/api/tracks/"+lt.TrackCitta+"/diction", nil)
	if resp.Status != 409 {
		t.Fatalf("untimed take: %d %s", resp.Status, resp.Body)
	}
	if p := obj(t, resp.JSON(t)); p["code"] != "ANALYSIS_UNAVAILABLE" || obj(t, p["details"])["analyzer"] != "tier_b" {
		t.Errorf("problem = %s", resp.Body)
	}
	resp = owner.Do(t, "GET", path, nil)
	if resp.Status != 200 {
		t.Fatalf("proportional: %d %s", resp.Status, resp.Body)
	}
	b := obj(t, resp.JSON(t))
	if b["basis"] != "proportional" || b["truncated"] != false || b["next_cursor"] != nil {
		t.Errorf("body = %s", resp.Body)
	}
	if lines, _ := b["lines"].([]any); len(lines) != 3 || obj(t, lines[0])["timing"] != "estimated" {
		t.Errorf("lines = %v", b["lines"])
	}

	storeEarsDoc(t, r, lt.TrackLuna, tierbtest.FromLyrics(earsBLyrics, tierbtest.Opts{Subs: map[string]string{"luna": "lena"}}))
	count := func() int {
		var n int
		if err := r.Store.R.QueryRow(`SELECT (SELECT count(*) FROM analyses) + (SELECT count(*) FROM sections) + (SELECT count(*) FROM diction_marks) + (SELECT count(*) FROM lexicon_overrides)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	resp = owner.Do(t, "GET", path, nil)
	if resp.Status != 200 {
		t.Fatalf("asr: %d %s", resp.Status, resp.Body)
	}
	b = obj(t, resp.JSON(t))
	lines, _ := b["lines"].([]any)
	if b["basis"] != "asr" || b["lang"] != "it" || len(lines) != 3 {
		t.Fatalf("body = %s", resp.Body)
	}
	l0 := obj(t, lines[0])
	if l0["text"] != "la la luna" || l0["timing"] != "heard" || l0["class_source"] != "asr" {
		t.Errorf("line 0 = %v", l0)
	}
	if ws, _ := l0["words"].([]any); len(ws) != 3 || obj(t, ws[2])["match"] == "exact" || obj(t, ws[2])["heard"] != "lena" {
		t.Errorf("words = %v", l0["words"])
	}
	for _, c := range []struct {
		name string
		c    *servertest.Client
	}{{"editor", editor}, {"agent", agent}} {
		if got := c.c.Do(t, "GET", path, nil); got.Status != 200 || string(got.Body) != string(resp.Body) {
			t.Errorf("%s: %d, body differs from the Owner's", c.name, got.Status)
		}
	}
	if got := listener.Do(t, "GET", path, nil); got.Status != 403 {
		t.Errorf("listener: %d %s", got.Status, got.Body)
	}
	if got := owner.Do(t, "GET", "/api/tracks/nope/diction", nil); got.Status != 404 {
		t.Errorf("unknown take: %d", got.Status)
	}
	if got := owner.Do(t, "GET", path+"?cursor=%25%25", nil); got.Status != 422 {
		t.Errorf("bad cursor: %d %s", got.Status, got.Body)
	}
	if after := count(); after != before {
		t.Errorf("GET wrote rows: %d -> %d", before, after)
	}
}

// TestSetMarks_EditorOwnerOnly: the Owner marks, the marks show on the lines and override the class; an Editor, a Listener, a read
// agent and a device of an Editor are refused; an agent with write scope marks as critic and changes no class.
func TestSetMarks_EditorOwnerOnly(t *testing.T) {
	t.Parallel()
	r := earsBRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	readAgent, _ := r.Agent(t, auth.ScopeRead)
	writeAgent, _ := r.Agent(t, auth.ScopeWrite, auth.ScopeRead)
	storeEarsDoc(t, r, lt.TrackLuna, tierbtest.FromLyrics(earsBLyrics, tierbtest.Opts{}))
	path := "/api/tracks/" + lt.TrackLuna + "/diction/marks"
	body := map[string]any{"marks": []map[string]any{{"section_idx": 0, "line_idx": 0, "mark": "unclear"}}}

	for name, c := range map[string]*servertest.Client{"editor": editor, "listener": listener, "read agent": readAgent} {
		if got := c.Do(t, "PUT", path, body); got.Status != 403 {
			t.Errorf("%s: %d %s, want 403", name, got.Status, got.Body)
		}
	}
	resp := owner.Do(t, "PUT", path, body)
	if resp.Status != 200 {
		t.Fatalf("owner: %d %s", resp.Status, resp.Body)
	}
	if ms, _ := obj(t, resp.JSON(t))["marks"].([]any); len(ms) != 1 || obj(t, ms[0])["source"] != "human" || obj(t, ms[0])["mark"] != "unclear" {
		t.Errorf("marks = %s", resp.Body)
	}
	lines := obj(t, owner.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/diction", nil).JSON(t))["lines"].([]any)
	if l := obj(t, lines[0]); l["class"] != "unclear" || l["class_source"] != "human" || obj(t, l["mark"])["human"] != "unclear" {
		t.Errorf("line 0 = %v", l)
	}
	crit := map[string]any{"marks": []map[string]any{{"section_idx": 1, "line_idx": 0, "mark": "clear"}}}
	resp = writeAgent.Do(t, "PUT", path, crit)
	if resp.Status != 200 {
		t.Fatalf("write agent: %d %s", resp.Status, resp.Body)
	}
	if ms, _ := obj(t, resp.JSON(t))["marks"].([]any); len(ms) != 2 || obj(t, ms[1])["source"] != "critic" {
		t.Errorf("marks = %s", resp.Body)
	}
	for name, bad := range map[string]map[string]any{
		"unknown line": {"marks": []map[string]any{{"section_idx": 0, "line_idx": 5, "mark": "clear"}}},
		"bad mark":     {"marks": []map[string]any{{"section_idx": 0, "line_idx": 0, "mark": "loud"}}},
		"empty":        {"marks": []map[string]any{}},
	} {
		if got := owner.Do(t, "PUT", path, bad); got.Status != 422 {
			t.Errorf("%s: %d %s, want 422", name, got.Status, got.Body)
		}
	}
	if got := owner.Do(t, "PUT", "/api/tracks/nope/diction/marks", body); got.Status != 404 {
		t.Errorf("unknown take: %d", got.Status)
	}
}

// TestDictionLexicon_REST: the lexicon lists what the stored rows say; Owner, Editor and a read agent read it, a Listener does not;
// the Owner can ignore a word, an Editor cannot; unknown short words are 404 and bad input 422; source isolation: an agent's
// critic marks do not move the calibration.
func TestDictionLexicon_REST(t *testing.T) {
	t.Parallel()
	r := earsBRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	listener := r.User(t, "carol", config.RoleListener)
	agent, _ := r.Agent(t, auth.ScopeRead)
	writeAgent, _ := r.Agent(t, auth.ScopeWrite)
	storeEarsDoc(t, r, lt.TrackLuna, tierbtest.FromLyrics(earsBLyrics, tierbtest.Opts{Subs: map[string]string{"luna": "lena"}}))

	resp := owner.Do(t, "GET", "/api/diction/lexicon?lang=it&q=lun", nil)
	if resp.Status != 200 {
		t.Fatalf("lexicon: %d %s", resp.Status, resp.Body)
	}
	b := obj(t, resp.JSON(t))
	es, _ := b["entries"].([]any)
	if len(es) != 1 || obj(t, es[0])["word"] != "luna" || obj(t, es[0])["mangled"] != float64(1) || obj(t, b["calibration"])["lines_marked"] != float64(0) {
		t.Fatalf("lexicon = %s", resp.Body)
	}
	for _, c := range []*servertest.Client{editor, agent} {
		if got := c.Do(t, "GET", "/api/diction/lexicon?lang=it&q=lun", nil); got.Status != 200 || string(got.Body) != string(resp.Body) {
			t.Errorf("body differs: %d", got.Status)
		}
	}
	if got := listener.Do(t, "GET", "/api/diction/lexicon", nil); got.Status != 403 {
		t.Errorf("listener: %d", got.Status)
	}
	if got := owner.Do(t, "GET", "/api/diction/lexicon?risk=true", nil); got.Status != 200 || len(obj(t, got.JSON(t))["entries"].([]any)) != 0 {
		t.Errorf("risk=true: %d %s", got.Status, got.Body)
	}
	if got := owner.Do(t, "GET", "/api/diction/lexicon?cursor=%25", nil); got.Status != 422 {
		t.Errorf("bad cursor: %d", got.Status)
	}

	// a critic mark does not move the calibration
	if got := writeAgent.Do(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/diction/marks", map[string]any{"marks": []map[string]any{{"section_idx": 0, "line_idx": 0, "mark": "clear"}}}); got.Status != 200 {
		t.Fatalf("critic mark: %d %s", got.Status, got.Body)
	}
	if cal := obj(t, obj(t, owner.Do(t, "GET", "/api/diction/lexicon", nil).JSON(t))["calibration"]); cal["lines_marked"] != float64(0) {
		t.Errorf("calibration = %v", cal)
	}
	if got := owner.Do(t, "PUT", "/api/tracks/"+lt.TrackLuna+"/diction/marks", map[string]any{"marks": []map[string]any{{"section_idx": 0, "line_idx": 0, "mark": "clear"}}}); got.Status != 200 {
		t.Fatalf("human mark: %d", got.Status)
	}
	if cal := obj(t, obj(t, owner.Do(t, "GET", "/api/diction/lexicon", nil).JSON(t))["calibration"]); cal["lines_marked"] != float64(1) || cal["clear"] != float64(1) || cal["tracks_marked"] != float64(1) {
		t.Errorf("calibration = %v", cal)
	}

	patch := map[string]any{"status": "ignored", "note": "a place"}
	for name, c := range map[string]*servertest.Client{"editor": editor, "listener": listener, "read agent": agent} {
		if got := c.Do(t, "PATCH", "/api/diction/lexicon/it/luna", patch); got.Status != 403 {
			t.Errorf("%s: %d, want 403", name, got.Status)
		}
	}
	resp = owner.Do(t, "PATCH", "/api/diction/lexicon/it/Luna", patch)
	if resp.Status != 200 {
		t.Fatalf("patch: %d %s", resp.Status, resp.Body)
	}
	if e := obj(t, resp.JSON(t)); e["status"] != "ignored" || e["note"] != "a place" || e["word"] != "luna" {
		t.Errorf("entry = %s", resp.Body)
	}
	if got := owner.Do(t, "PATCH", "/api/diction/lexicon/it/ab", patch); got.Status != 404 {
		t.Errorf("unknown short word: %d %s", got.Status, got.Body)
	}
	for name, p := range map[string]string{"/api/diction/lexicon/ITALIAN/luna": "bad lang", "/api/diction/lexicon/it/la%20luna": "two words"} {
		if got := owner.Do(t, "PATCH", name, patch); got.Status != 422 {
			t.Errorf("%s: %d %s", p, got.Status, got.Body)
		}
	}
	if got := owner.Do(t, "PATCH", "/api/diction/lexicon/it/luna", map[string]any{}); got.Status != 422 {
		t.Errorf("empty patch: %d", got.Status)
	}
}
