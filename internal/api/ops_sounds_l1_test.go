package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const skankTerm = "skank canary term"

// soundsRig is the L1 mirror library with the seed applied, one extra draft Song ("skank"), and every Sound subject
// computed. The Reconciler's Sounds step is wired by L1-B7, so the test computes through the service itself.
func soundsRig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	o.Fixture, o.M2c, o.M3, o.L1 = true, true, true, true
	r := servertest.New(t, o)
	ctx := context.Background()
	if err := r.Services.Sounds.EnsureSeed(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`INSERT INTO recipe_drafts(id,origin,request_id,request_json,status,created_at,updated_at)
		VALUES('01900000-0000-7000-8000-00000000f001','import_backlog','skank-1',?,'backlog','2026-10-06T12:00:00Z','2026-10-06T12:00:00Z')`,
		`{"id":"skank-1","seed":5,"style":"`+skankTerm+`, 90bpm","lyrics":"x"}`); err != nil {
		t.Fatal(err)
	}
	r.Settle(t)
	tx, err := r.Store.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	if _, err := r.Services.Sounds.SweepStaleTx(ctx, tx, store.Env{Now: lt.FixedNow, NewID: func() string { n++; return fmt.Sprintf("x%d", n) }, Actor: "system:test"}, 100000); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestREST_SearchSounds(t *testing.T) {
	t.Parallel()
	r := soundsRig(t, servertest.Options{})
	agent, _ := r.Agent(t, auth.ScopeRead)

	body := m(agent.OK(t, "GET", "/api/sounds/search?limit=0", nil))
	facets := m(body["facets"])
	for _, f := range []string{"genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"} {
		if _, ok := facets[f].([]any); !ok {
			t.Errorf("facet %s missing or null: %v", f, facets[f])
		}
	}
	if body["taxonomy_version"].(float64) != 1 || len(arr(body["songs"])) != 0 || body["total"].(float64) < 1 {
		t.Errorf("counts-only answer: %v", body)
	}
	if eng := arr(facets["engine"]); len(eng) == 0 {
		t.Error("no engine facet values: every known-recipe Song has one")
	}

	// tempo_band values report mismatches, others do not.
	for _, v := range arr(facets["tempo_band"]) {
		if _, ok := m(v)["mismatches"]; !ok {
			t.Errorf("tempo_band value without mismatches: %v", v)
		}
	}
	for _, v := range arr(facets["engine"]) {
		if _, ok := m(v)["mismatches"]; ok {
			t.Errorf("engine value with mismatches: %v", v)
		}
	}
	// NOTE: the sound= / tempo_mismatch query parameters of the frozen openapi.json are missing (see the L1-B6 report:
	// huma ignores the embedded unexported songFilterIn), so selection round trips are tested on soundSearchFilter and
	// on the Facets service in internal/sounds until the contract is fixed.
	// With a page requested the Song list comes from list_songs (L1-B5); until that lane merges it answers 501.
	if resp := agent.Do(t, "GET", "/api/sounds/search?limit=5", nil); resp.Status != 200 && resp.Status != 501 {
		t.Errorf("limit 5: %d %s", resp.Status, resp.Body)
	}
}

func TestREST_TaxonomyAndMap(t *testing.T) {
	t.Parallel()
	r := soundsRig(t, servertest.Options{})
	reader, _ := r.Agent(t, auth.ScopeRead)
	writer, writerID := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	human := r.Human(t)

	tax := m(reader.OK(t, "GET", "/api/sounds/taxonomy?facet=genre_family&limit=5", nil))
	if tax["current_version"].(float64) != 1 || len(arr(tax["items"])) != 5 || tax["next_cursor"] == nil || len(arr(tax["unmapped"])) != 0 {
		t.Fatalf("taxonomy page: %v", tax)
	}
	v0 := m(arr(tax["versions"])[0])
	if v0["seed_id"] != "seed/1" || m(v0["created_by"])["kind"] != "system" {
		t.Errorf("version row: %v", v0)
	}
	un := m(reader.OK(t, "GET", "/api/sounds/taxonomy?unmapped=true", nil))
	if got := fmt.Sprint(un["unmapped"]); !strings.Contains(got, "term:"+skankTerm) {
		t.Errorf("unmapped list lacks the synthetic term: %v", got)
	}
	if resp := reader.Do(t, "GET", "/api/sounds/taxonomy?facet=engine", nil); resp.Status != 422 {
		t.Errorf("facet=engine: %d", resp.Status)
	}

	req := map[string]any{"note": "synthetic", "mappings": []any{map[string]any{"term": skankTerm, "facet": "genre_family", "value": "reggae", "op": "add"}}}
	if resp := reader.Do(t, "POST", "/api/sound-terms", req); resp.Status != 403 {
		t.Errorf("read-only map_sound_term: %d", resp.Status)
	}
	res := m(writer.OK(t, "POST", "/api/sound-terms", req))
	if res["version"].(float64) != 2 || res["mappings_applied"].(float64) != 1 || m(res["recomputed"])["songs"].(float64) < 1 {
		t.Fatalf("map result: %v", res)
	}
	// The Song now shows up under reggae and the unmapped list lost the term.
	body := m(reader.OK(t, "GET", "/api/sounds/search?limit=0", nil))
	reggae := 0.0
	for _, v := range arr(m(body["facets"])["genre_family"]) {
		if m(v)["value"] == "reggae" {
			reggae = m(v)["count"].(float64)
		}
	}
	if reggae < 1 || body["taxonomy_version"].(float64) != 2 {
		t.Errorf("no Song under reggae after the mapping: %v", body["facets"])
	}
	if un := m(reader.OK(t, "GET", "/api/sounds/taxonomy?unmapped=true", nil)); strings.Contains(fmt.Sprint(un["unmapped"]), skankTerm) {
		t.Errorf("the mapped term is still unmapped")
	}
	// An agent's version is attributed to the agent; the operator sees the agent, an agent sees the same.
	tax = m(human.OK(t, "GET", "/api/sounds/taxonomy?limit=1", nil))
	v0 = m(arr(tax["versions"])[0])
	if v0["version"].(float64) != 2 || m(v0["created_by"])["kind"] != "agent" || m(v0["created_by"])["id"] != "agent:"+writerID {
		t.Errorf("version 2: %v", v0)
	}
	// All-unchanged: no new version.
	res = m(writer.OK(t, "POST", "/api/sound-terms", req))
	if res["version"] != nil || res["unchanged"].(float64) != 1 {
		t.Errorf("repeat: %v", res)
	}
	// Errors carry the stable codes.
	bad := map[string]any{"mappings": []any{map[string]any{"term": "x y", "facet": "mood", "value": "chill", "op": "remove"}}}
	if resp := writer.Do(t, "POST", "/api/sound-terms", bad); resp.Status != 422 || m(resp.JSON(t))["code"] != "MAPPING_NOT_EFFECTIVE" {
		t.Errorf("not effective: %d %s", resp.Status, resp.Body)
	}
	bad = map[string]any{"mappings": []any{map[string]any{"term": "x y", "facet": "mood", "value": "very sad", "op": "add"}}}
	if resp := writer.Do(t, "POST", "/api/sound-terms", bad); resp.Status != 422 || m(resp.JSON(t))["code"] != "INVALID_VALUE" {
		t.Errorf("bad value: %d %s", resp.Status, resp.Body)
	}

	// Audit: the calls are recorded by operation id with a hash of the arguments, never the term text.
	rows, err := r.Store.R.Query(`SELECT op, status, args_sha256 FROM audit_log WHERE op = 'map_sound_term' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	statuses := []int{}
	for rows.Next() {
		var op, sha string
		var st int
		if err := rows.Scan(&op, &st, &sha); err != nil {
			t.Fatal(err)
		}
		if len(sha) != 64 || strings.Contains(sha, "skank") {
			t.Errorf("args hash %q", sha)
		}
		statuses = append(statuses, st)
	}
	if fmt.Sprint(statuses) != "[403 200 200 422 422]" {
		t.Errorf("audit statuses = %v", statuses)
	}
	var leaks int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE created_ids_json LIKE '%skank%' OR error_code LIKE '%skank%' OR client LIKE '%skank%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Errorf("term text leaked into %d audit rows (%v)", leaks, err)
	}
}

func TestREST_MapSoundTerm_HeavyRateClass(t *testing.T) {
	t.Parallel()
	r := soundsRig(t, servertest.Options{Limits: ratelimit.Default})
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	// agent_heavy is 6 per minute: six calls pass (a read-only class would allow 60), the seventh is refused.
	var last int
	for i := range 7 {
		req := map[string]any{"mappings": []any{map[string]any{"term": fmt.Sprintf("heavy term %c", 'a'+i), "facet": "mood", "value": "chill", "op": "add"}}}
		last = agent.Do(t, "POST", "/api/sound-terms", req).Status
		if i < 6 && last != 200 {
			t.Fatalf("call %d: %d", i+1, last)
		}
	}
	if last != 429 {
		t.Errorf("7th map_sound_term: %d, want 429 (heavy rate class)", last)
	}
	// Reads stay in the default class.
	if st := agent.Do(t, "GET", "/api/sounds/taxonomy", nil).Status; st != 200 {
		t.Errorf("a read after the heavy refusal: %d", st)
	}
}
