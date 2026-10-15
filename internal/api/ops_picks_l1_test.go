package api_test

import (
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// picksRig is the L1 library with its structure settled, and one Song with at least two takes.
func picksRig(t *testing.T) (r *servertest.Rig, song string, takes []string) {
	t.Helper()
	r = servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true})
	r.Settle(t)
	if err := r.Store.R.QueryRow(`SELECT song_id FROM take_songs GROUP BY song_id HAVING COUNT(*) >= 2 ORDER BY song_id LIMIT 1`).Scan(&song); err != nil {
		t.Fatal(err)
	}
	rows, err := r.Store.R.Query(`SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? AND t.status <> 'discarded' ORDER BY t.id`, song)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		takes = append(takes, id)
	}
	if len(takes) < 2 {
		t.Fatalf("fixture has no Song with two takes")
	}
	return r, song, takes
}

func auditStatus(t *testing.T, r *servertest.Rig, op string) []int {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT status FROM audit_log WHERE op = ? ORDER BY id`, op)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var s int
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestPickOps_ProposePinDismissFeed(t *testing.T) {
	t.Parallel()
	r, song, tk := picksRig(t)
	human := r.Human(t)
	agent, agentID := r.Agent(t, rw...)

	// propose: 201, agent-authored text, idempotent replay
	body := map[string]any{"take_id": tk[0], "rationale": "synthetic rationale"}
	key := "Idempotency-Key"
	resp := agent.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", body, key, "k-1")
	if resp.Status != 201 {
		t.Fatalf("propose: %d %s", resp.Status, resp.Body)
	}
	p := obj(t, resp.JSON(t))
	if p["state"] != "open" || p["song_id"] != song || p["take_id"] != tk[0] || p["decided_at"] != nil || obj(t, p["proposed_by"])["id"] != "agent:"+agentID {
		t.Fatalf("proposal %v", p)
	}
	again := agent.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", body, key, "k-1")
	if again.Status != 201 || obj(t, again.JSON(t))["id"] != p["id"] {
		t.Fatalf("replay: %d %s", again.Status, again.Body)
	}
	var n int
	_ = r.Store.R.QueryRow(`SELECT COUNT(*) FROM pick_proposals WHERE song_id = ?`, song).Scan(&n)
	if n != 1 {
		t.Fatalf("%d proposals after a replay", n)
	}
	// a human cannot propose; an agent cannot pin or dismiss
	if x := human.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", body, key, "k-2"); x.Status != 403 || obj(t, x.JSON(t))["code"] != "HUMAN_ONLY" {
		t.Errorf("human propose: %d %s", x.Status, x.Body)
	}
	before := mustCount(t, r, `SELECT COUNT(*) FROM pick_events`)
	if x := agent.Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": tk[0]}); x.Status != 403 || obj(t, x.JSON(t))["code"] != "HUMAN_ONLY" {
		t.Errorf("agent pin: %d %s", x.Status, x.Body)
	}
	if x := agent.Do(t, "POST", "/api/pick-proposals/"+p["id"].(string)+"/dismiss", nil); x.Status != 403 {
		t.Errorf("agent dismiss: %d", x.Status)
	}
	if mustCount(t, r, `SELECT COUNT(*) FROM pick_events`) != before {
		t.Errorf("a refused call changed state")
	}
	if st := auditStatus(t, r, "pin_pick"); len(st) != 1 || st[0] != 403 {
		t.Errorf("audit rows of pin_pick: %v", st)
	}

	// the human pins, accepting the proposal
	resp = human.Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": tk[0], "proposal_id": p["id"]})
	if resp.Status != 200 {
		t.Fatalf("pin: %d %s", resp.Status, resp.Body)
	}
	res := obj(t, resp.JSON(t))
	pick, prop := obj(t, res["pick"]), obj(t, res["proposal"])
	if res["changed"] != true || pick["source"] != "pinned" || pick["take_id"] != tk[0] || prop["state"] != "accepted" || obj(t, pick["pinned_by"])["kind"] != "human" {
		t.Fatalf("pin result %v", res)
	}
	if obj(t, pick["evidence"])["proposal_id"] != p["id"] {
		t.Errorf("evidence %v", pick["evidence"])
	}
	// an agent sees the operator anonymised
	feed := agent.OK(t, "GET", "/api/pick-events?song_id="+song, nil)
	items := obj(t, feed)["items"].([]any)
	last := obj(t, items[len(items)-1])
	if last["reason"] != "pin" || obj(t, last["actor"])["id"] != "human" || last["to_take_id"] != tk[0] {
		t.Errorf("event %v", last)
	}
	hfeed := obj(t, human.OK(t, "GET", "/api/pick-events?song_id="+song, nil))["items"].([]any)
	if a := obj(t, obj(t, hfeed[len(hfeed)-1])["actor"]); a["id"] != "human:alice" {
		t.Errorf("human view actor %v", a)
	}
	// dismiss a second proposal as the human
	r2 := agent.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", map[string]any{"take_id": tk[1], "rationale": "other"}, key, "k-3")
	if r2.Status != 409 && r2.Status != 201 {
		t.Fatalf("second propose: %d %s", r2.Status, r2.Body)
	}
	if r2.Status == 201 {
		id := obj(t, r2.JSON(t))["id"].(string)
		d := human.Do(t, "POST", "/api/pick-proposals/"+id+"/dismiss", nil)
		if d.Status != 200 || obj(t, d.JSON(t))["state"] != "dismissed" {
			t.Errorf("dismiss: %d %s", d.Status, d.Body)
		}
		if d2 := human.Do(t, "POST", "/api/pick-proposals/"+id+"/dismiss", nil); d2.Status != 409 || obj(t, d2.JSON(t))["code"] != "PROPOSAL_CLOSED" {
			t.Errorf("second dismiss: %d %s", d2.Status, d2.Body)
		}
	}
	// unpin
	un := obj(t, human.OK(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": nil}))
	if un["changed"] != true || obj(t, un["pick"])["pinned_take_id"] != nil || un["proposal"] != nil {
		t.Errorf("unpin %v", un)
	}
	// paging by cursor
	first := obj(t, human.OK(t, "GET", "/api/pick-events?limit=1", nil))
	if len(first["items"].([]any)) != 1 || first["next_cursor"] == nil {
		t.Fatalf("page %v", first)
	}
	next := obj(t, human.OK(t, "GET", fmt.Sprintf("/api/pick-events?limit=1&after=%v", first["next_cursor"]), nil))
	if c0, c1 := obj(t, first["items"].([]any)[0])["cursor"].(float64), obj(t, next["items"].([]any)[0])["cursor"].(float64); c1 <= c0 {
		t.Errorf("cursors %v %v", c0, c1)
	}
	// error shapes carry ids only
	if x := human.Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": "no-such-take"}); x.Status != 422 || obj(t, x.JSON(t))["code"] != "TAKE_NOT_IN_SONG" {
		t.Errorf("foreign take: %d %s", x.Status, x.Body)
	}
	if x := human.Do(t, "PUT", "/api/songs/no-such-song/pin", map[string]any{"take_id": nil}); x.Status != 404 {
		t.Errorf("unknown song: %d %s", x.Status, x.Body)
	}
}

func mustCount(t *testing.T, r *servertest.Rig, q string) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPickOps_ReadScopeAndMissingKey(t *testing.T) {
	t.Parallel()
	r, song, tk := picksRig(t)
	ro, _ := r.Agent(t, auth.ScopeRead)
	if x := ro.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", map[string]any{"take_id": tk[0], "rationale": "x"}, "Idempotency-Key", "k"); x.Status != 403 {
		t.Errorf("read scope propose: %d", x.Status)
	}
	if x := ro.Do(t, "GET", "/api/pick-events", nil); x.Status != 200 {
		t.Errorf("read scope feed: %d", x.Status)
	}
	rwc, _ := r.Agent(t, rw...)
	if x := rwc.Do(t, "POST", "/api/songs/"+song+"/pick-proposals", map[string]any{"take_id": tk[0], "rationale": "x"}); x.Status != 428 {
		t.Errorf("missing Idempotency-Key: %d %s", x.Status, x.Body)
	}
}
