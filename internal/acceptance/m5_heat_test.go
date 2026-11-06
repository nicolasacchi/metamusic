package acceptance_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func m5Event(n int, track, session, kind string, from, to int) map[string]any {
	e := map[string]any{"id": fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xc5000+n), "track_id": track, "session_id": session, "kind": kind, "client_at": "2026-10-06T11:59:00Z"}
	if from >= 0 {
		e["from_ms"] = from
	}
	if to >= 0 {
		e["to_ms"] = to
	}
	return e
}

// TestAcceptance_M5_ListeningHeat: the operator's listening becomes a heat strip and hotspots, rewinds included (the client's backward
// seeks used to be dropped as bad_range). A phone counts like the operator, an agent's listen counts for nothing, a backward
// segment is still refused, reading writes nothing, REST and MCP answer the same strip, and the take's /context names the hotspots.
func TestAcceptance_M5_ListeningHeat(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
	h, dev := r.Human(t), r.Device(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	sess := r.MCP(t, auth.ScopeRead)
	track := knownMother(t, r) // 6 s: 120 bins of 0.05 s

	post := func(c *servertest.Client, wantRejected int, evs ...map[string]any) {
		t.Helper()
		anyEvs := make([]any, len(evs))
		for i, e := range evs {
			anyEvs[i] = e
		}
		resp := c.Do(t, "POST", "/api/listen-events", map[string]any{"events": anyEvs})
		if resp.Status != 200 {
			t.Fatalf("log: %d %s", resp.Status, resp.Body)
		}
		if rej := arr(obj(resp.JSON(t))["rejected"]); len(rej) != wantRejected {
			t.Fatalf("rejected %d events (%v), want %d", len(rej), rej, wantRejected)
		}
	}
	heat := func() map[string]any { return obj(h.OK(t, "GET", "/api/tracks/"+track+"/heat", nil)) }
	cell := func(h map[string]any, k string, i int) float64 { return arr(h[k])[i].(float64) }

	empty := heat()
	if empty["plays"] != 0.0 || len(arr(empty["heard"])) != 120 || len(arr(empty["hotspots"])) != 0 {
		t.Fatalf("an unheard take: %v", empty)
	}

	// session a listens through; sessions b and c listen to the first seconds, jump forward, then rewind to 1.0 s twice and replay it
	post(h, 0, m5Event(1, track, "session-aaa", "start", 0, -1), m5Event(2, track, "session-aaa", "segment", 0, 6000), m5Event(3, track, "session-aaa", "complete", -1, -1))
	post(h, 0, m5Event(4, track, "session-bbb", "start", 0, -1), m5Event(5, track, "session-bbb", "segment", 0, 4000),
		m5Event(6, track, "session-bbb", "seek", 4000, 1000), // a rewind: the client seeks from 4.0 s back to 1.0 s
		m5Event(7, track, "session-bbb", "segment", 1000, 3000), m5Event(8, track, "session-bbb", "seek", 3000, 1000), m5Event(9, track, "session-bbb", "segment", 1000, 2500))
	post(dev, 0, m5Event(10, track, "session-ccc", "start", 0, -1), m5Event(11, track, "session-ccc", "segment", 0, 3000), m5Event(12, track, "session-ccc", "seek", 3000, 1000),
		m5Event(13, track, "session-ccc", "segment", 1000, 3000), m5Event(14, track, "session-ddd", "skip", 500, -1))
	stored := count(t, r, `SELECT COUNT(*) FROM listen_events WHERE kind = 'seek'`)
	if stored != 3 {
		t.Fatalf("%d seek events stored, want the 3 rewinds", stored)
	}
	// a backward segment is still refused (bad_range), and an agent's listening is accepted but counts for nothing
	post(h, 1, m5Event(15, track, "session-eee", "segment", 5000, 1000))
	post(agent, 0, m5Event(16, track, "session-agent", "start", 0, -1), m5Event(17, track, "session-agent", "segment", 0, 6000))

	got := heat()
	if got["sessions"] != 4.0 || got["plays"] != 3.0 {
		t.Errorf("sessions/plays = %v/%v, want 4/3 (sessions a-d, three of which started a play; the agent's session does not count)", got["sessions"], got["plays"])
	}
	// bin 20 is 1.0-1.05 s: three rewinds landed there, four sessions-worth of segments covered it
	if cell(got, "rewinds", 20) != 3 || cell(got, "heard", 20) != 3 || cell(got, "replays", 20) < 3 {
		t.Errorf("bin 20: rewinds %v heard %v replays %v", cell(got, "rewinds", 20), cell(got, "heard", 20), cell(got, "replays", 20))
	}
	if cell(got, "heard", 110) != 1 { // only session a heard the last stretch
		t.Errorf("heard[110] = %v, want 1", cell(got, "heard", 110))
	}
	if cell(got, "skips", 10) != 1 { // the skip at 0.5 s
		t.Errorf("skips[10] = %v", cell(got, "skips", 10))
	}
	hot := arr(got["hotspots"])
	if len(hot) == 0 || len(hot) > 3 {
		t.Fatalf("hotspots = %v", hot)
	}
	first := obj(hot[0])
	if from, to := first["from_s"].(float64), first["to_s"].(float64); from > 1.05 || to < 1.0 || first["score"].(float64) < 6 {
		t.Errorf("the top hotspot %v does not cover the rewound-to second", first)
	}

	// REST and MCP answer the same strip; reading writes nothing; the take's context names the hotspots
	if mcpHeat := obj(callTool(t, sess, "get_listen_heat", map[string]any{"id": track}, true)); !reflect.DeepEqual(mcpHeat, got) {
		t.Errorf("MCP heat differs from REST:\n%v\n%v", mcpHeat, got)
	}
	before := count(t, r, `SELECT COUNT(*) FROM listen_events`) + count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'get_listen_heat' AND status >= 300`)
	heat()
	if after := count(t, r, `SELECT COUNT(*) FROM listen_events`) + count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'get_listen_heat' AND status >= 300`); after != before {
		t.Error("reading the heat wrote a row")
	}
	listening := obj(obj(callTool(t, sess, "get_track_context", map[string]any{"id": track}, false))["listening"])
	if hs := arr(listening["hotspots"]); len(hs) == 0 || len(hs) > 3 || obj(hs[0])["score"] != first["score"] {
		t.Errorf("context hotspots = %v, want the strip's top hotspot first", listening["hotspots"])
	}
}
