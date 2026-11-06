package acceptance_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// sectionDuelRig is the Blindtest fixture of M2b with both takes timed (plan sections: verse then chorus; the chorus is 20-45 s on
// Alpha and 22-50 s on Beta) and a fixed slot coin, so two rigs can differ in the coin alone.
func sectionDuelRig(t *testing.T, coin bool) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2b: servertest.M2bOptions{Fixture: true, FakeClips: true, SlotCoin: func() bool { return coin }}})
	for _, s := range []struct {
		track, kind, label string
		idx                int
		from, to           float64
	}{{duelstest.TrackAlpha, "verse", "verse 1", 0, 0, 20}, {duelstest.TrackAlpha, "chorus", "chorus 1", 1, 20, 45},
		{duelstest.TrackBeta, "verse", "verse 1", 0, 0, 22}, {duelstest.TrackBeta, "chorus", "chorus 1", 1, 22, 50}} {
		if _, err := r.Store.W.Exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?,'plan',?,?,?,?,?,?,0.9)`,
			s.track, s.idx, s.kind, s.label, "["+s.label+"]", s.from, s.to); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// TestAcceptance_M5_SectionDuelBlindness: a section-locked duel (roadmap M5: compare one section of two takes, blind) end to end over
// REST and MCP. Until the operator has judged, nothing a caller can read says which take is in which slot or where the compared
// windows sit: two rigs that differ only in the slot coin answer byte-identical views from create, get, list and the agent's MCP
// reads. A critic verdict does not reveal it to the operator. The operator's verdict reveals the windows, and slot A's window is the
// section of whichever take the coin put there.
func TestAcceptance_M5_SectionDuelBlindness(t *testing.T) {
	views := map[bool]map[string]any{}
	reveal := map[bool]map[string]any{}
	for _, coin := range []bool{false, true} {
		r := sectionDuelRig(t, coin)
		h := r.Human(t)
		agent := r.MCP(t, auth.ScopeRead, auth.ScopeWrite)
		agentRest, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)

		// the agent creates the section duel over MCP (kind + ordinal), the operator reads it over REST
		created := obj(callTool(t, agent, "create_duel", map[string]any{"a_track_id": duelstest.TrackAlpha, "b_track_id": duelstest.TrackBeta,
			"section": map[string]any{"kind": "chorus", "ordinal": 1}}, true))
		id := created["id"].(string)
		dur, _ := obj(created["window"])["duration_s"].(float64)
		if dur <= 0 || obj(created["section"])["label"] != "chorus 1" {
			t.Fatalf("coin %v: created = %v", coin, created)
		}
		read := map[string]any{
			"create":      created,
			"human get":   obj(h.OK(t, "GET", "/api/duels/"+id, nil)),
			"agent get":   obj(callTool(t, agent, "get_duel", map[string]any{"id": id}, true)),
			"agent rest":  obj(agentRest.OK(t, "GET", "/api/duels/"+id, nil)),
			"human lists": nil,
		}
		for _, it := range arr(obj(h.OK(t, "GET", "/api/duels?state=open&limit=50", nil))["items"]) {
			if obj(it)["id"] == id {
				read["human lists"] = it
			}
		}
		if read["human lists"] == nil {
			t.Fatalf("coin %v: the duel is not in the open list", coin)
		}
		for what, v := range read {
			m := obj(v)
			w := obj(m["window"])
			if w["from_s"] != nil || w["to_s"] != nil || w["duration_s"] != dur {
				t.Errorf("coin %v %s: window = %v, want only a duration", coin, what, w)
			}
			for _, side := range []string{"a", "b"} {
				slot := obj(obj(m["slots"])[side])
				if _, has := slot["window"]; has || slot["track"] != nil {
					t.Errorf("coin %v %s: slot %s = %v leaks before the verdict", coin, what, side, slot)
				}
			}
			raw, _ := json.Marshal(v)
			for _, secret := range []string{duelstest.TrackAlpha, duelstest.TrackBeta, "Blindtest"} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("coin %v %s leaks %q", coin, what, secret)
				}
			}
		}
		views[coin] = read

		// the critic's verdict is stored as an opinion and reveals nothing to the operator
		if res := obj(callTool(t, agent, "submit_duel", map[string]any{"id": id, "verdict": "a"}, false)); obj(res["verdict"])["source"] != "critic" {
			t.Fatalf("coin %v: agent verdict = %v", coin, res)
		}
		after := obj(h.OK(t, "GET", "/api/duels/"+id, nil))
		if !reflect.DeepEqual(after, read["human get"]) {
			t.Errorf("coin %v: the human view changed after a critic verdict:\n%v\n%v", coin, after, read["human get"])
		}

		// the operator's verdict reveals: the window bounds, and per slot the window of the take the coin put there
		resp := h.Do(t, "POST", "/api/duels/"+id+"/verdicts", map[string]any{"verdict": "a"}, "Idempotency-Key", "sd-1")
		if resp.Status != 201 {
			t.Fatalf("coin %v: human verdict %d %s", coin, resp.Status, resp.Body)
		}
		d := obj(obj(resp.JSON(t))["duel"])
		if d["revealed"] != true || obj(d["window"])["from_s"] == nil {
			t.Fatalf("coin %v: not revealed: %v", coin, d)
		}
		// the window leads into the section by 2 s and lasts as long as the shorter of the two sections plus the lead-in (25 + 2 s): slot A
		// holds Alpha without the swap (the coin false), Beta with it
		wantA, wantB := 18.0, 20.0
		trackA := duelstest.TrackAlpha
		if coin {
			wantA, wantB, trackA = 20.0, 18.0, duelstest.TrackBeta
		}
		a, b := obj(obj(d["slots"])["a"]), obj(obj(d["slots"])["b"])
		if obj(a["track"])["id"] != trackA || obj(a["window"])["from_s"] != wantA || obj(b["window"])["from_s"] != wantB ||
			obj(a["window"])["to_s"].(float64)-wantA != dur || obj(b["window"])["to_s"].(float64)-wantB != dur {
			t.Errorf("coin %v: revealed slots = %v / %v", coin, a, b)
		}
		reveal[coin] = d
	}
	// the unrevealed views of the two rigs are identical, byte for byte
	for what, v := range views[false] {
		// the clip URLs are content addresses of the slot's audio, which is what differs between the rigs by construction
		if !reflect.DeepEqual(withoutClipURLs(v), withoutClipURLs(views[true][what])) {
			t.Errorf("%s differs with the slot coin:\n%v\n%v", what, v, views[true][what])
		}
	}
	// and the reveals differ (the test can tell the coins apart)
	if reflect.DeepEqual(reveal[false], reveal[true]) {
		t.Error("the revealed duel is the same under both coins: the blindness check is vacuous")
	}
}

// withoutClipURLs drops the content-addressed clip URLs of both slots from a duel view (a copy).
func withoutClipURLs(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, side := range []string{"a", "b"} {
		if clip := obj(obj(obj(m["slots"])[side])["clip"]); clip != nil {
			delete(clip, "url")
		}
	}
	return m
}
