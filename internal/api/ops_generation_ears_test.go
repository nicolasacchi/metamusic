package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestEstimate_TierBREST: options.tier_b reaches the planner (take plans show tier_b and a bigger ceiling), an explicit true and
// false are honoured, a non-boolean is a validation error, and an Editor sees tier_b but none of the Owner-side money fields (EARS-G1).
func TestEstimate_TierBREST(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	item := map[string]any{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 2}
	est := func(c interface {
		OK(testing.TB, string, string, any, ...string) any
	}, opts any) map[string]any {
		body := map[string]any{"items": []any{item}}
		if opts != nil {
			body["options"] = opts
		}
		return m(c.OK(t, "POST", "/api/generations/estimate", body))
	}
	tierB := func(e map[string]any) []bool {
		var out []bool
		for _, tk := range arr(m(arr(e["items"])[0])["takes"]) {
			v, _ := m(tk)["tier_b"].(bool)
			out = append(out, v)
		}
		return out
	}
	ceiling := func(e map[string]any) float64 { return m(e["cost"])["ceiling_micro_usd"].(float64) }

	auto := est(owner, nil)
	on := est(owner, map[string]any{"tier_b": true})
	off := est(owner, map[string]any{"tier_b": false})
	if got := tierB(auto); len(got) != 2 || !got[0] || !got[1] {
		t.Errorf("auto: tier_b = %v, want both on (the fake backend has every capability)", got)
	}
	if got := tierB(on); !got[0] || !got[1] {
		t.Errorf("true: tier_b = %v", got)
	}
	if got := tierB(off); got[0] || got[1] {
		t.Errorf("false: tier_b = %v", got)
	}
	if ceiling(off) >= ceiling(auto) || ceiling(auto) != ceiling(on) {
		t.Errorf("ceilings: off %v auto %v true %v", ceiling(off), ceiling(auto), ceiling(on))
	}
	if resp := owner.Do(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{item}, "options": map[string]any{"tier_b": "yes"}}); resp.Status != 422 {
		t.Errorf("a non-boolean tier_b: %d %s", resp.Status, resp.Body)
	}

	// an Editor sees the flag and the take's max, but none of the Owner-side money (price, month, day, caps)
	e := est(editor, nil)
	if got := tierB(e); !got[0] {
		t.Errorf("editor: tier_b = %v", got)
	}
	if e["price"] != nil || m(e["budget"])["month"] != nil || m(e["budget"])["day"] != nil || m(e["budget"])["batch_cap_micro_usd"] != nil {
		t.Errorf("an editor sees owner-side money: price %v budget %v", e["price"], e["budget"])
	}
	if got := tierB(est(agent, nil)); !got[0] {
		t.Errorf("agent: tier_b = %v", got)
	}
}

// TestEstimate_AnalyzeREST: the item mode analyze reaches the planner over REST (EARS-G2, C340): it prices a chosen take for Owner,
// Editor and agent alike (an Editor sees none of the Owner-side money), the schema bounds track_ids, and the rules answer their
// stable codes.
func TestEstimate_AnalyzeREST(t *testing.T) {
	t.Parallel()
	r := genRig(t)
	owner := r.Human(t)
	editor := r.User(t, "bob", config.RoleEditor)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	body := func(items ...any) map[string]any { return map[string]any{"items": items} }
	analyze := func(ids ...string) any { return map[string]any{"mode": "analyze", "track_ids": ids} }

	e := m(owner.OK(t, "POST", "/api/generations/estimate", body(analyze(lt.TrackLuna))))
	take := m(arr(m(arr(e["items"])[0])["takes"])[0])
	if take["operator"] != "analyze" || take["parent_track_id"] != lt.TrackLuna || take["tier_b"] == true {
		t.Errorf("take = %v", take)
	}
	if c, _ := m(e["cost"])["ceiling_micro_usd"].(float64); c <= 0 {
		t.Errorf("cost = %v", e["cost"])
	}
	ed := m(editor.OK(t, "POST", "/api/generations/estimate", body(analyze(lt.TrackCitta))))
	if ed["price"] != nil || m(ed["budget"])["month"] != nil || ed["requires_human_approval"] != true {
		t.Errorf("an Editor's analyze estimate: price %v budget %v requires %v", ed["price"], ed["budget"], ed["requires_human_approval"])
	}
	m(agent.OK(t, "POST", "/api/generations/estimate", body(analyze(lt.TrackTwinA))))

	for name, c := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"no tracks":       {body(map[string]any{"mode": "analyze"}), 422, ""},
		"too many tracks": {body(map[string]any{"mode": "analyze", "track_ids": make([]string, 51)}), 422, ""},
		"with tier_b":     {map[string]any{"items": []any{analyze(lt.TrackLuna)}, "options": map[string]any{"tier_b": false}}, 422, "VALIDATION_FAILED"},
		"mixed":           {body(analyze(lt.TrackLuna), map[string]any{"mode": "new_seed", "parent_track_id": lt.TrackLuna}), 422, "VALIDATION_FAILED"},
		"extra field":     {body(map[string]any{"mode": "analyze", "track_ids": []string{lt.TrackLuna}, "n_seeds": 2}), 422, "INVALID_ITEM"},
		"unknown track":   {body(analyze("01900000-0000-7000-8000-00000000ffff")), 422, "UNKNOWN_TRACK"},
	} {
		resp := owner.Do(t, "POST", "/api/generations/estimate", c.body)
		if resp.Status != c.status {
			t.Errorf("%s: %d %s, want %d", name, resp.Status, resp.Body, c.status)
			continue
		}
		if c.code != "" && m(resp.JSON(t))["code"] != c.code {
			t.Errorf("%s: code %v, want %s", name, m(resp.JSON(t))["code"], c.code)
		}
	}
}
