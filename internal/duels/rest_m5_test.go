package duels_test

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// timeAlphaBeta gives both Blindtest takes plan sections (verse 0-20, chorus 20-45 / 22-50).
func timeAlphaBeta(t *testing.T, r *servertest.Rig) {
	t.Helper()
	for _, s := range []struct {
		track, kind, label string
		from, to           float64
	}{{duelstest.TrackAlpha, "verse", "verse 1", 0, 20}, {duelstest.TrackAlpha, "chorus", "chorus 1", 20, 45},
		{duelstest.TrackBeta, "verse", "verse 1", 0, 22}, {duelstest.TrackBeta, "chorus", "chorus 1", 22, 50}} {
		if _, err := r.Store.W.Exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?,'plan',?,?,?,?,?,?,0.9)`,
			s.track, map[string]int{"verse 1": 0, "chorus 1": 1}[s.label], s.kind, s.label, "["+s.label+"]", s.from, s.to); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreateDuel_Section_REST(t *testing.T) {
	t.Parallel()
	r := rig(t, nil)
	c, _ := r.Agent(t, rw...)
	// untimed (the fixture takes are lyrics-only) and malformed requests answer stable codes on the wire
	res := c.Do(t, "POST", "/api/duels", createBody(map[string]any{"section": map[string]any{"label": "chorus 1"}}), "Idempotency-Key", idemKey())
	if res.Status != 422 || asMap(res.JSON(t))["code"] != "SECTION_UNTIMED" {
		t.Fatalf("untimed: %d %s", res.Status, res.Body)
	}
	timeAlphaBeta(t, r)
	res = c.Do(t, "POST", "/api/duels", createBody(map[string]any{"section": map[string]any{"label": "chorus 1"}, "from_s": 10}), "Idempotency-Key", idemKey())
	if res.Status != 422 || asMap(res.JSON(t))["code"] != "VALIDATION_FAILED" {
		t.Fatalf("from_s with section: %d %s", res.Status, res.Body)
	}
	res = c.Do(t, "POST", "/api/duels", createBody(map[string]any{"section": map[string]any{"label": "bridge"}}), "Idempotency-Key", idemKey())
	if res.Status != 422 || asMap(res.JSON(t))["code"] != "SECTION_NOT_FOUND" {
		t.Fatalf("missing: %d %s", res.Status, res.Body)
	}
	res = c.Do(t, "POST", "/api/duels", createBody(map[string]any{"section": map[string]any{"kind": "chorus", "ordinal": 1}}), "Idempotency-Key", idemKey())
	if res.Status != 201 {
		t.Fatalf("create: %d %s", res.Status, res.Body)
	}
	d := asMap(res.JSON(t))
	w := asMap(d["window"])
	if w["from_s"] != nil || w["to_s"] != nil || w["duration_s"] != 27.0 || asMap(d["section"])["label"] != "chorus 1" || asMap(d["section"])["source"] != "plan" {
		t.Errorf("blind section duel = %s", res.Body)
	}
	for _, s := range []string{"a", "b"} {
		if _, ok := asMap(asMap(d["slots"])[s])["window"]; ok {
			t.Errorf("slot %s window present before the reveal", s)
		}
	}
	// the same duel read back by id is just as blind
	g := c.Do(t, "GET", "/api/duels/"+d["id"].(string), nil)
	if g.Status != 200 {
		t.Fatalf("get: %d %s", g.Status, g.Body)
	}
	var back map[string]any
	_ = json.Unmarshal(g.Body, &back)
	if asMap(back["window"])["from_s"] != nil {
		t.Errorf("get leaks the window: %s", g.Body)
	}
}

func TestSuggestDuelPairs_REST(t *testing.T) {
	t.Parallel()
	for _, via := range []string{"rest", "mcp"} {
		t.Run(via, func(t *testing.T) {
			t.Parallel()
			r := rig(t, nil)
			timeAlphaBeta(t, r)
			args := map[string]any{"scope": "lineage", "root_track_id": duelstest.TrackAlpha, "k": 3, "section_kind": "chorus", "section_ordinal": 1}
			var out map[string]any
			if via == "rest" {
				c, _ := r.Agent(t, rw...)
				res := c.Do(t, "GET", "/api/duels/suggestions?scope=lineage&root_track_id="+duelstest.TrackAlpha+"&k=3&section_kind=chorus&section_ordinal=1", nil)
				if res.Status != 200 {
					t.Fatalf("%d %s", res.Status, res.Body)
				}
				out = asMap(res.JSON(t))
			} else {
				out, _ = callOp(t, r.MCP(t, rw...), "suggest_duel_pairs", args, true)
			}
			// the fixture's open duel holds the only pair of the lineage: nothing to suggest, and the shape is complete
			if out["scope"] != "lineage" || out["section"] != "chorus 1" || asMap(out["model"]) == nil {
				t.Errorf("answer = %v", out)
			}
			if pairs, ok := out["pairs"].([]any); !ok || len(pairs) != 0 {
				t.Errorf("pairs = %v", out["pairs"])
			}
			// without the open duel in the way (global scope has Alpha/Beta too) a bad request is a clean 422
			if via == "rest" {
				c, _ := r.Agent(t, rw...)
				for _, q := range []string{"scope=lineage", "scope=global&section_kind=chorus", "scope=global&section_label=a&section_kind=chorus&section_ordinal=1"} {
					res := c.Do(t, "GET", "/api/duels/suggestions?"+q, nil)
					if res.Status != 422 || asMap(res.JSON(t))["code"] != "VALIDATION_FAILED" {
						t.Errorf("%s: %d %s", q, res.Status, res.Body)
					}
				}
			}
		})
	}
}
