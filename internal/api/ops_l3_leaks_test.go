package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l3Rig is the settled L3 library with the real feature step, an operator client and a read+write agent.
type l3LeakRig struct {
	r      *servertest.Rig
	human  *servertest.Client
	agent  *servertest.Client
	reader *servertest.Client
}

func newL3LeakRig(t *testing.T, o servertest.Options) *l3LeakRig {
	t.Helper()
	o.Fixture, o.M2c, o.M3, o.L1, o.L2, o.L3, o.RealFeatures = true, true, true, true, true, true, true
	r := servertest.New(t, o)
	r.Settle(t)
	ag, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	rd, _ := r.Agent(t, auth.ScopeRead)
	return &l3LeakRig{r: r, human: r.Human(t), agent: ag, reader: rd}
}

func (x *l3LeakRig) id(t *testing.T, table, title string) string {
	t.Helper()
	var id string
	if err := x.r.Store.R.QueryRow(`SELECT id FROM `+table+` WHERE title = ?`, title).Scan(&id); err != nil {
		t.Fatalf("%s %q: %v", table, title, err)
	}
	return id
}

// l3MuseTexts are the synthetic Muse strings of the L3 and L2 fixtures (titles and artists), standing in for real Muse text.
var l3MuseTexts = []string{lt.L3Muse3Title, lt.L3Muse3Artist, lt.L3Muse4Title, lt.L3Muse4Artist, lt.L3Muse5Title, lt.L3Muse5Artist,
	museCanary1, museCanary2, "Synthetic Curation Artist 1", "Synthetic Curation Artist 2"}

// l3GETs are the readable L3 operations and a request each (the sweep calls them as the operator, as a read agent and over MCP).
func (x *l3LeakRig) l3GETs(t *testing.T) map[string]map[string]any {
	t.Helper()
	song := func(l string) string { return x.id(t, "songs", "Synthetic Song "+l) }
	return map[string]map[string]any{
		"suggest_sequence":        {"song_id": []string{song("T1"), song("T2"), song("T3")}, "strategy": "arc"},
		"list_next_actions":       {"limit": 50},
		"list_taxonomy_proposals": {"state": []string{"open", "accepted", "rejected", "expired"}, "include_impact": true},
		"get_song_atlas":          {"x": "tempo", "y": "energy", "color_by": "muse", "limit": 100},
		"get_adherence":           {"min_n": 1},
	}
}

func restGET(t *testing.T, c *servertest.Client, a *api.OpInfo, args map[string]any) string {
	t.Helper()
	q := []string{}
	for k, v := range args {
		switch vv := v.(type) {
		case []string:
			for _, e := range vv {
				q = append(q, k+"="+e)
			}
		default:
			q = append(q, fmt.Sprintf("%s=%v", k, vv))
		}
	}
	resp := c.Do(t, "GET", a.Path+"?"+strings.Join(q, "&"), nil)
	return fmt.Sprintf("%d %s", resp.Status, resp.Body)
}

// TestMuseText_L3_OnlyInAllowlistedOps: every readable L3 operation is called over REST (operator and read-scope agent) and over
// MCP on the L3 fixture with Muses on the Songs and in the Atlas legend; the Muse strings appear in none of them except
// list_next_actions, the one op the allowlist gained (D-L3-16), and a canary Muse assigned to every Song of the sweep never
// reaches the Atlas, the sequence preview, the proposals or the adherence answer.
func TestMuseText_L3_OnlyInAllowlistedOps(t *testing.T) {
	t.Parallel()
	x := newL3LeakRig(t, servertest.Options{})
	tag := randHex(t)
	canary := museCanary{Title: "Canary Title " + tag, Artist: "Canary Artist " + tag}
	mu := obj(t, x.human.OK(t, "POST", "/api/muses", map[string]any{"kind": "reference", "title": canary.Title, "artist": canary.Artist}, "Idempotency-Key", "l3-sweep-muse"))
	var songs []string
	for _, l := range []string{"T1", "T2", "T3"} {
		songs = append(songs, x.id(t, "songs", "Synthetic Song "+l))
	}
	x.human.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": songs}, "Idempotency-Key", "l3-sweep-assign")
	x.r.Settle(t)
	// a proposal (agent text in the rationale is the agent's own and is shown to the operator only)
	x.agent.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-sweep-prop")

	forbidden := append(slices.Clone(l3MuseTexts), canary.Title, canary.Artist)
	sess := x.r.MCP(t, auth.ScopeRead)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(x.r.API) {
		byID[op.ID] = op
	}
	swept := 0
	for id, args := range x.l3GETs(t) {
		op := byID[id]
		bodies := map[string]string{
			"rest human": restGET(t, x.human, &op, args),
			"rest agent": restGET(t, x.reader, &op, args),
			"mcp":        mcpGeneric(t, sess, id, args),
		}
		swept++
		for where, body := range bodies {
			if strings.HasPrefix(body, "4") || strings.HasPrefix(body, "5") {
				t.Errorf("%s over %s: %.200s", id, where, body)
			}
			for _, text := range forbidden {
				if strings.Contains(body, text) && id != "list_next_actions" {
					t.Errorf("%s over %s leaks Muse text %q: %.200s", id, where, text, body)
				}
			}
		}
	}
	if swept != 5 {
		t.Errorf("swept %d operations, want the five readable L3 operations", swept)
	}
	if !slices.Contains(museTextOps, "list_next_actions") {
		t.Error("list_next_actions must be on the Muse-text allowlist of TestMuseText_OnlyInAllowlistedOps")
	}
	// non-vacuous: the canary Muse exists and get_muse does show it
	if body := string(x.reader.Do(t, "GET", "/api/muses/"+mu["id"].(string), nil).Body); !strings.Contains(body, canary.Title) {
		t.Error("get_muse does not show the canary: the sweep is vacuous")
	}
	// the Songs' own titles never contain Muse text (TestSongTitle_NeverMuseText covers the rest of the library)
	for _, s := range songs {
		var title string
		if err := x.r.Store.R.QueryRow(`SELECT title FROM songs WHERE id = ?`, s).Scan(&title); err != nil {
			t.Fatal(err)
		}
		for _, text := range forbidden {
			if strings.Contains(title, text) {
				t.Errorf("Song title %q holds Muse text", title)
			}
		}
	}
}

// TestSongTitle_NeverMuseText: no Song title in the settled L3 library contains a Muse title or artist, so the surfaces that name
// Songs (the sequence preview, the Atlas of a Muse, adherence, next-action text) cannot leak Muse text through a title; the titles
// the L3 surfaces show are the stored Song titles unchanged.
func TestSongTitle_NeverMuseText(t *testing.T) {
	t.Parallel()
	x := newL3LeakRig(t, servertest.Options{})
	type pair struct{ title, artist string }
	var muses []pair
	rows, err := x.r.Store.R.Query(`SELECT title, COALESCE(artist, '') FROM muses`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.title, &p.artist); err != nil {
			t.Fatal(err)
		}
		muses = append(muses, p)
	}
	_ = rows.Close()
	titles := map[string]bool{}
	rows, err = x.r.Store.R.Query(`SELECT title FROM songs WHERE retired_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		titles[s] = true
	}
	_ = rows.Close()
	if len(muses) < 5 || len(titles) < 20 {
		t.Fatalf("%d Muses, %d Songs: the check would be vacuous", len(muses), len(titles))
	}
	for title := range titles {
		for _, p := range muses {
			if p.title != "" && strings.Contains(title, p.title) || p.artist != "" && strings.Contains(title, p.artist) {
				t.Errorf("Song title %q contains Muse text %q / %q", title, p.title, p.artist)
			}
		}
	}
	// every title the sequence preview shows is a stored Song title
	args := x.l3GETs(t)["suggest_sequence"]
	op := api.OpInfo{Path: "/api/sequence"}
	var sug struct {
		Steps []struct{ Title string } `json:"steps"`
	}
	resp := x.reader.Do(t, "GET", op.Path+"?song_id="+strings.Join(args["song_id"].([]string), "&song_id=")+"&strategy=arc", nil)
	if resp.Status != 200 {
		t.Fatalf("suggest_sequence: %d %s", resp.Status, resp.Body)
	}
	if err := json.Unmarshal(resp.Body, &sug); err != nil {
		t.Fatal(err)
	}
	if len(sug.Steps) != 3 {
		t.Fatalf("steps %v", sug.Steps)
	}
	for _, s := range sug.Steps {
		if !titles[s.Title] {
			t.Errorf("preview title %q is not a stored Song title", s.Title)
		}
	}
}

// TestL3Ops_ErrorsCarryIDsOnly: the refusals of the L3 operations, driven with canary text in every free-text field (rationale,
// term, Record title), answer problem+json that names codes, ids and numbers and never that text, a Record or Song title or Muse
// text.
func TestL3Ops_ErrorsCarryIDsOnly(t *testing.T) {
	t.Parallel()
	x := newL3LeakRig(t, servertest.Options{})
	canary := "CanaryL3Text" + randHex(t)
	// selector is a value the caller picks from a closed set (a strategy, an axis, a state): a refusal names the offending value
	// the caller itself sent, as the L2 refusals echo a bad id or filter key. Free text (rationale, term, title, note) is never echoed.
	selector := "no_such_choice"
	missing := "01900000-0000-7000-8000-00000000ffff"
	s1, s2 := x.id(t, "songs", "Synthetic Song T1"), x.id(t, "songs", "Synthetic Song T2")
	rid := x.id(t, "records", r1Title)
	var rev int
	if err := x.r.Store.R.QueryRow(`SELECT revision FROM records WHERE id = ?`, rid).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	var draft string
	if err := x.r.Store.R.QueryRow(`SELECT id FROM records WHERE title = ?`, r2Title).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	// an open proposal to decide twice and a decided one
	prop := obj(t, x.agent.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-err-prop"))
	pid := prop["id"].(string)
	x.human.OK(t, "POST", "/api/taxonomy-proposals/"+pid+"/decision", map[string]any{"decision": "reject"})
	many := make([]map[string]any, 51)
	for i := range many {
		many[i] = map[string]any{"term": fmt.Sprintf("term-%d", i), "facet": "mood", "value": "chill", "op": "add"}
	}
	type call struct {
		name, method, path string
		body               any
		agent              bool
		status             int
	}
	calls := []call{
		{"sequence one song", "GET", "/api/sequence?song_id=" + s1, nil, true, 422},
		{"sequence unknown song", "GET", "/api/sequence?song_id=" + s1 + "&song_id=" + missing, nil, true, 422},
		{"sequence two sources", "GET", "/api/sequence?record_id=" + rid + "&song_id=" + s1 + "&song_id=" + s2, nil, true, 422},
		{"sequence no source", "GET", "/api/sequence", nil, true, 422},
		{"sequence bad strategy", "GET", "/api/sequence?song_id=" + s1 + "&song_id=" + s2 + "&strategy=" + selector, nil, true, 422},
		{"sequence unknown record", "GET", "/api/sequence?record_id=" + missing, nil, true, 404},
		{"sequence unknown crate", "GET", "/api/sequence?crate_id=" + missing, nil, true, 404},
		{"slot op unknown record", "POST", "/api/records/" + missing + "/slots", map[string]any{"revision": 1, "ops": []any{map[string]any{"op": "sequence", "strategy": "arc"}}}, true, 404},
		{"slot op bad strategy", "POST", "/api/records/" + draft + "/slots", map[string]any{"revision": 1, "ops": []any{map[string]any{"op": "sequence", "strategy": selector}}}, true, 422},
		{"propose bad sequence", "POST", "/api/records/" + draft + "/proposal", map[string]any{"revision": 1, "rationale": canary, "sequence": map[string]any{"strategy": selector}}, true, 422},
		{"taxonomy empty", "POST", "/api/taxonomy-proposals", map[string]any{"rationale": canary, "mappings": []any{}}, true, 422},
		{"taxonomy bad facet", "POST", "/api/taxonomy-proposals", map[string]any{"rationale": canary, "mappings": []map[string]any{{"term": canary, "facet": "no_such_facet", "value": "chill", "op": "add"}}}, true, 422},
		{"taxonomy bad value", "POST", "/api/taxonomy-proposals", map[string]any{"rationale": canary, "mappings": []map[string]any{{"term": canary, "facet": "mood", "value": "no_such_choice", "op": "add"}}}, true, 422},
		{"taxonomy too many", "POST", "/api/taxonomy-proposals", map[string]any{"rationale": canary, "mappings": many}, true, 422},
		{"taxonomy no rationale", "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "", "mappings": []map[string]any{{"term": canary, "facet": "mood", "value": "chill", "op": "add"}}}, true, 422},
		{"decide as agent", "POST", "/api/taxonomy-proposals/" + pid + "/decision", map[string]any{"decision": "accept", "note": canary}, true, 403},
		{"decide unknown", "POST", "/api/taxonomy-proposals/" + missing + "/decision", map[string]any{"decision": "accept", "note": canary}, false, 404},
		{"decide twice", "POST", "/api/taxonomy-proposals/" + pid + "/decision", map[string]any{"decision": "accept", "note": canary}, false, 409},
		{"decide bad subset", "POST", "/api/taxonomy-proposals/" + pid + "/decision", map[string]any{"decision": "accept", "accept_mappings": []int{99}, "note": canary}, false, 0},
		{"proposals bad state", "GET", "/api/taxonomy-proposals?state=" + selector, nil, true, 422},
		{"proposals bad cursor", "GET", "/api/taxonomy-proposals?cursor=" + canary, nil, true, 0},
		{"atlas bad axis", "GET", "/api/atlas?x=" + selector + "&y=energy", nil, true, 422},
		{"atlas same axes", "GET", "/api/atlas?x=tempo&y=tempo", nil, true, 422},
		{"atlas bad cursor", "GET", "/api/atlas?x=tempo&y=energy&cursor=" + canary, nil, true, 0},
		{"adherence bad facet", "GET", "/api/atlas/adherence?facet=" + selector, nil, true, 422},
		{"adherence bad cursor", "GET", "/api/atlas/adherence?cursor=" + canary, nil, true, 0},
		{"next bad cursor", "GET", "/api/next-actions?cursor=" + canary, nil, true, 0},
		{"next bad audience", "GET", "/api/next-actions?audience=" + selector, nil, true, 0},
	}
	forbidden := append(slices.Clone(l3MuseTexts), r1Title, r2Title, "Synthetic Song T", "Synthetic Song d", "Synthetic Take")
	// the caller's own free text is never echoed, so the canary is forbidden in every refusal; ids the caller sent may be
	codes := map[string]bool{}
	for i, c := range calls {
		cl := x.human
		if c.agent {
			cl = x.agent
		}
		resp := cl.Do(t, c.method, c.path, c.body, "Idempotency-Key", fmt.Sprintf("l3-err-%d", i))
		if resp.Status/100 == 2 {
			t.Errorf("%s: want a refusal, got %d %.200s", c.name, resp.Status, resp.Body)
			continue
		}
		if c.status != 0 && resp.Status != c.status {
			t.Errorf("%s: status %d, want %d (%.200s)", c.name, resp.Status, c.status, resp.Body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "problem+json") {
			t.Errorf("%s: content type %q", c.name, ct)
		}
		code, _ := problemOf(t, resp)
		if code == "" {
			t.Errorf("%s: no code in %.200s", c.name, resp.Body)
		}
		codes[code] = true
		for _, f := range append(slices.Clone(forbidden), canary) {
			if strings.Contains(string(resp.Body), f) {
				t.Errorf("%s: the problem body carries %q: %.300s", c.name, f, resp.Body)
			}
		}
	}
	if len(codes) < 6 {
		t.Errorf("only %d distinct codes driven: %v", len(codes), codes)
	}
	// the same refusals over MCP: the tool error text is the same problem body
	sess := x.r.MCP(t, auth.ScopeRead, auth.ScopeWrite)
	for op, args := range map[string]map[string]any{
		"suggest_sequence":        {"song_id": []string{s1}, "strategy": selector},
		"propose_taxonomy":        {"rationale": canary, "mappings": []map[string]any{{"term": canary, "facet": "no_such_facet", "value": "chill", "op": "add"}}},
		"list_taxonomy_proposals": {"state": []string{selector}},
		"get_song_atlas":          {"x": selector, "y": "energy"},
		"get_adherence":           {"facet": selector},
	} {
		body := mcpGeneric(t, sess, op, args)
		for _, f := range append(slices.Clone(forbidden), canary) {
			if strings.Contains(body, f) {
				t.Errorf("%s over MCP: the error carries %q: %.300s", op, f, body)
			}
		}
	}
}

// TestNotify_NoNextActionText: next-action text can name a Muse, so it must never reach a notification. Statically, nothing that
// builds or sends a notification imports the next-action engine; dynamically, the next-action texts of the L3 library (as the
// operator sees them) appear in no ntfy request after the agent and the operator have worked through the L3 flows.
func TestNotify_NoNextActionText(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/nicolasacchi/metamusic/internal/notify", "github.com/nicolasacchi/metamusic/internal/jobs").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/nextactions") {
			t.Errorf("the notification path imports %s", dep)
		}
	}
	for _, dir := range []string{"../notify", "../jobs"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			b, _ := os.ReadFile(f) //nolint:gosec // repository sources
			if strings.Contains(string(b), "NextAction") && !strings.HasSuffix(f, "_test.go") {
				t.Errorf("%s mentions next actions", f)
			}
		}
	}

	x := newL3LeakRig(t, servertest.Options{Canary: true})
	var texts []string
	for _, c := range []*servertest.Client{x.human, x.reader} {
		page := obj(t, c.OK(t, "GET", "/api/next-actions?limit=50", nil))
		for _, it := range page["items"].([]any) {
			texts = append(texts, obj(t, it)["text"].(string))
		}
	}
	if len(texts) < 10 {
		t.Fatalf("only %d next-action texts", len(texts))
	}
	x.agent.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "l3-notify-prop")
	x.r.Settle(t)
	for i, nr := range x.r.Ntfy.Requests() {
		for _, text := range texts {
			if strings.Contains(nr.Body, text) || strings.Contains(nr.Path, text) {
				t.Errorf("ntfy request %d carries the next-action text %q", i, text)
			}
		}
	}
}
