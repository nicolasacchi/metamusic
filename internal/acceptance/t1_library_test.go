package acceptance_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAcceptance_T1_JoinsLibrary_Tape is the tape row of the plan's TestAcceptance_T1_JoinsLibrary (B6 owns the library part, in
// t1_b6_test.go): a transform Take pinned as the Pick of a Song in a Record exports as a tape through the real pipeline, with no
// TAPE_* refusal, and the Take shows its transform block in /context.
func TestAcceptance_T1_JoinsLibrary_Tape(t *testing.T) {
	tr := newT1Rig(t)
	a := tr.Info.Tx["A"]
	id := tr.RunDone(glacierBody(a, 10, 3))["id"].(string)
	tr.Settle(t)
	var song string
	if err := tr.Store.R.QueryRow(`SELECT song_id FROM take_songs WHERE track_id = ?`, id).Scan(&song); err != nil {
		t.Fatal(err)
	}
	pin := obj(tr.H.OK(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": id}))
	if obj(pin["pick"])["take_id"] != id {
		t.Fatalf("pin = %v", pin)
	}
	rec := obj(tr.H.OK(t, "POST", "/api/records", map[string]any{"title": "Synthetic Transform Record", "song_ids": []string{song}}, tr.idem()...))
	rid, _ := rec["id"].(string)
	if rid == "" {
		rid, _ = obj(rec["record"])["id"].(string)
	}
	if rid == "" {
		t.Fatalf("create_record = %v", rec)
	}
	rev := obj(obj(tr.H.OK(t, "GET", "/api/records/"+rid, nil))["record"])["revision"].(float64)
	tr.H.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": rev, "published": true})
	tp := l2ExportTape(t, tr.Rig, tr.H, rid, "t1-tape")
	if tp["state"] != "ready" {
		t.Fatalf("tape = %v", tp)
	}
	if b, _ := json.Marshal(tp); strings.Contains(string(b), "TAPE_") {
		t.Errorf("the tape view carries a TAPE_ refusal: %s", b)
	}
	ctx := obj(tr.H.OK(t, "GET", "/api/tracks/"+id+"/context", nil))
	if obj(ctx["transform"])["kind"] != "glacier" {
		t.Errorf("/context transform = %v", ctx["transform"])
	}
}
