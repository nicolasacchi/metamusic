package api_test

import (
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func heatEvent(n int, track, session, kind string, from, to int) map[string]any {
	e := listenEvent(100+n, track, kind)
	e["session_id"] = session
	if from >= 0 {
		e["from_ms"] = from
	}
	if to >= 0 {
		e["to_ms"] = to
	}
	return e
}

func heatRowCount(t *testing.T, r *servertest.Rig, table string) (n int) {
	t.Helper()
	if err := r.Store.R.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestListenHeat_REST: events posted through the listening API show up in the heat of the take, human and device events only;
// the GET is read scoped, answers 404 for an unknown take and writes nothing (TestHeat_GETNeverWrites).
func TestListenHeat_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	luna := lt.TrackLuna // 12 s: 120 bins of 0.1 s
	post := func(c *servertest.Client, evs ...map[string]any) {
		t.Helper()
		anyEvs := make([]any, len(evs))
		for i, e := range evs {
			anyEvs[i] = e
		}
		resp := c.Do(t, "POST", "/api/listen-events", map[string]any{"events": anyEvs})
		if resp.Status != 200 {
			t.Fatalf("log: %d %s", resp.Status, resp.Body)
		}
		if rej := obj(t, resp.JSON(t))["rejected"].([]any); len(rej) != 0 {
			t.Fatalf("rejected: %v", rej)
		}
	}
	getHeat := func() (map[string]any, string) {
		t.Helper()
		resp := human.Do(t, "GET", "/api/tracks/"+luna+"/heat", nil)
		if resp.Status != 200 {
			t.Fatalf("heat: %d %s", resp.Status, resp.Body)
		}
		return obj(t, resp.JSON(t)), string(resp.Body)
	}
	num := func(h map[string]any, k string, i int) float64 { return h[k].([]any)[i].(float64) }
	base, _ := getHeat() // the fixture already holds some listening of Luna
	post(human,
		heatEvent(1, luna, "session-aaa", "start", 0, -1), heatEvent(2, luna, "session-aaa", "segment", 0, 6000),
		heatEvent(3, luna, "session-bbb", "start", 0, -1), heatEvent(4, luna, "session-bbb", "segment", 0, 3000),
		heatEvent(5, luna, "session-bbb", "segment", 0, 3000), heatEvent(6, luna, "session-bbb", "complete", -1, -1),
		heatEvent(7, luna, "session-ccc", "start", 0, -1), heatEvent(8, luna, "session-ccc", "skip", 500, -1))
	h, body1 := getHeat()
	if h["track_id"] != luna || h["bins"] != 120.0 || h["duration_s"] != 12.0 || h["sessions"] != base["sessions"].(float64)+3 || h["plays"] != base["plays"].(float64)+3 {
		t.Fatalf("heat = %s", body1)
	}
	for _, k := range []string{"heard", "replays", "rewinds", "skips", "hotspots", "cold"} {
		if _, ok := h[k].([]any); !ok {
			t.Errorf("%s is %T, want an array (never null)", k, h[k])
		}
	}
	// bin 10 (1.0-1.1 s): sessions aaa and bbb hear it, bbb twice; bin 5 holds the skip; bins past 6 s are not covered by the new events
	if num(h, "heard", 10) != num(base, "heard", 10)+2 || num(h, "replays", 10) != num(base, "replays", 10)+1 || num(h, "skips", 5) != num(base, "skips", 5)+1 || num(h, "heard", 100) != num(base, "heard", 100) {
		t.Errorf("heard[10] %v->%v replays[10] %v->%v skips[5] %v->%v heard[100] %v->%v", num(base, "heard", 10), num(h, "heard", 10),
			num(base, "replays", 10), num(h, "replays", 10), num(base, "skips", 5), num(h, "skips", 5), num(base, "heard", 100), num(h, "heard", 100))
	}
	// an agent's whole-track listen changes nothing
	post(agent, heatEvent(9, luna, "session-agent", "start", 0, -1), heatEvent(10, luna, "session-agent", "segment", 0, 12000))
	before := map[string]int{}
	tables := []string{"listen_events", "audit_log", "comments", "tracks"}
	for _, tb := range tables {
		before[tb] = heatRowCount(t, r, tb)
	}
	if _, body2 := getHeat(); body2 != body1 {
		t.Errorf("an agent's events changed the heat:\n%s\n%s", body1, body2)
	}
	for _, tb := range tables {
		if n := heatRowCount(t, r, tb); n != before[tb] {
			t.Errorf("GET changed %s: %d -> %d rows", tb, before[tb], n)
		}
	}
	// an agent with only the read scope may read it; an unknown take is a 404 problem
	reader, _ := r.Agent(t, auth.ScopeRead)
	if resp := reader.Do(t, "GET", "/api/tracks/"+luna+"/heat", nil); resp.Status != 200 {
		t.Errorf("read-scoped agent: %d %s", resp.Status, resp.Body)
	}
	resp := human.Do(t, "GET", "/api/tracks/"+fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xdead)+"/heat", nil)
	if resp.Status != 404 || obj(t, resp.JSON(t))["code"] != "NOT_FOUND" {
		t.Errorf("unknown take: %d %s", resp.Status, resp.Body)
	}
}

// TestListenHeat_EmptyStrip: a take nobody listened to answers a full strip of zeros, not an error.
func TestListenHeat_EmptyStrip(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	resp := r.Human(t).Do(t, "GET", "/api/tracks/"+lt.TrackShort+"/heat", nil)
	if resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	h := obj(t, resp.JSON(t))
	if h["sessions"] != 0.0 || h["plays"] != 0.0 || h["completion_rate"] != nil || len(h["heard"].([]any)) != 120 || len(h["hotspots"].([]any)) != 0 {
		t.Errorf("empty heat = %s", resp.Body)
	}
}

// TestContext_CloseLoopWired_REST: the served /context carries the close-the-loop block (the wiring in app is live): a take with an
// ancestor reports ancestor_open_fix and marks its comments, a take without ancestors is unchanged.
func TestContext_CloseLoopWired_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	ctxJudg := func(id string) map[string]any {
		t.Helper()
		resp := human.Do(t, "GET", "/api/tracks/"+id+"/context", nil)
		if resp.Status != 200 {
			t.Fatalf("%d %s", resp.Status, resp.Body)
		}
		return obj(t, obj(t, resp.JSON(t))["judgments"])
	}
	if j := ctxJudg(lt.TrackTwinB); j["ancestor_open_fix"] == nil { // TwinB is the child of TwinA
		t.Errorf("a child take lacks ancestor_open_fix: %v", j)
	}
	if j := ctxJudg(lt.TrackLuna); j["ancestor_open_fix"] != nil {
		t.Errorf("a root take reports ancestor_open_fix: %v", j)
	}
}

// TestListenHeat_RewindCounted_REST: the client's backward seek is stored (it used to be dropped as bad_range) and the heat
// counts it as a rewind in the bin it landed in, which makes a hotspot; reading the heat writes nothing.
func TestListenHeat_RewindCounted_REST(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	luna := lt.TrackLuna // 12 s, 0.1 s bins
	heat := func() map[string]any {
		resp := human.Do(t, "GET", "/api/tracks/"+luna+"/heat", nil)
		if resp.Status != 200 {
			t.Fatalf("%d %s", resp.Status, resp.Body)
		}
		return obj(t, resp.JSON(t))
	}
	base := heat()
	resp := human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{
		heatEvent(51, luna, "session-rew", "start", 0, -1),
		heatEvent(52, luna, "session-rew", "seek", 9000, 2000), // rewind 9 s -> 2 s
		heatEvent(53, luna, "session-rew", "seek", 8000, 2500), // another one, same bin region
	}})
	got := obj(t, resp.JSON(t))
	if resp.Status != 200 || got["accepted"] != 3.0 || len(got["rejected"].([]any)) != 0 {
		t.Fatalf("rewinds not accepted: %d %s", resp.Status, resp.Body)
	}
	n := heatRowCount(t, r, "listen_events")
	h := heat()
	rw, brw := h["rewinds"].([]any), base["rewinds"].([]any)
	if rw[20] != brw[20].(float64)+1 || rw[25] != brw[25].(float64)+1 {
		t.Errorf("rewinds[20]=%v (was %v) rewinds[25]=%v (was %v)", rw[20], brw[20], rw[25], brw[25])
	}
	if skips := h["skips"].([]any); skips[90] != base["skips"].([]any)[90] {
		t.Errorf("a rewind origin counted as a skip: %v", skips[90])
	}
	heat()
	if heatRowCount(t, r, "listen_events") != n {
		t.Error("GET heat changed listen_events")
	}
}
