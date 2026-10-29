package acceptance_test

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestProperty_RecordInvariants_FullRig: seeded random walks over the whole server, with Picks (ratings and Song pins), Sounds
// (the Settle seeds the taxonomy and derives every Song's facets), Records (edits, publishing) and tapes (export, the fake pipeline,
// the sweep) all enabled. After a final Settle records.Verify and the L1 provenance.Verify have no error, nothing answered 5xx
// along the way, and every Record's changelog replays to the slots' resolved Takes. The unit-level walk is records'
// TestProperty_RecordInvariants; this one adds the surfaces the Records package cannot see. MM_PROPERTY_SEQS overrides the count.
func TestProperty_RecordInvariants_FullRig(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	seqs := 12
	if v, err := strconv.Atoi(os.Getenv("MM_PROPERTY_SEQS")); err == nil && v > 0 {
		seqs = min(v, 60)
	}
	for seed := 1; seed <= seqs; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			r := l2Rig(t, servertest.Options{})
			rnd := rand.New(rand.NewSource(int64(seed)))
			h := r.Human(t)
			agent, _ := r.Agent(t, "read", "write")
			songTakes := map[string][]string{}
			var songs []string
			rows, err := r.Store.R.Query(`SELECT ts.song_id, ts.track_id FROM take_songs ts JOIN songs s ON s.id = ts.song_id WHERE s.retired_at IS NULL ORDER BY ts.song_id, ts.track_id`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var s, tk string
				if err := rows.Scan(&s, &tk); err != nil {
					t.Fatal(err)
				}
				if len(songTakes[s]) == 0 {
					songs = append(songs, s)
				}
				songTakes[s] = append(songTakes[s], tk)
			}
			_ = rows.Close()
			recs := []string{l2ID(t, r, "records", l2R1), l2ID(t, r, "records", l2R2)}
			ok := func(label string, status int, body []byte) {
				t.Helper()
				if status >= 500 {
					t.Fatalf("seed %d: %s answered %d: %s", seed, label, status, body)
				}
			}
			for i := 0; i < 40; i++ {
				switch op := rnd.Intn(10); op {
				case 0, 1: // a human rating moves Picks
					s := songs[rnd.Intn(len(songs))]
					tk := songTakes[s][rnd.Intn(len(songTakes[s]))]
					resp := h.Do(t, "PUT", "/api/tracks/"+tk+"/rating", map[string]any{"stars": 1 + rnd.Intn(5), "verdict": "synthetic"})
					ok("rate", resp.Status, resp.Body)
				case 2: // a Song-level pin
					s := songs[rnd.Intn(len(songs))]
					tk := songTakes[s][rnd.Intn(len(songTakes[s]))]
					resp := h.Do(t, "PUT", "/api/songs/"+s+"/pin", map[string]any{"take_id": tk})
					ok("pin", resp.Status, resp.Body)
				case 3: // a Record from random Songs
					a, b := songs[rnd.Intn(len(songs))], songs[rnd.Intn(len(songs))]
					if a == b {
						continue
					}
					resp := agent.Do(t, "POST", "/api/records", map[string]any{"title": "Synthetic Property Record", "song_ids": []string{a, b}}, "Idempotency-Key", "prop-"+strconv.Itoa(seed)+"-"+strconv.Itoa(i))
					ok("create", resp.Status, resp.Body)
					if resp.Status == 201 {
						recs = append(recs, obj(obj(resp.JSON(t))["record"])["id"].(string))
					}
				case 4, 5: // slot edits by the operator
					rid := recs[rnd.Intn(len(recs))]
					rev := recordRevision(t, r, rid)
					var sop map[string]any
					switch rnd.Intn(4) {
					case 0:
						sop = map[string]any{"op": "pin_all"}
					case 1:
						sop = map[string]any{"op": "unpin_all"}
					case 2:
						sop = map[string]any{"op": "add", "song_id": songs[rnd.Intn(len(songs))]}
					default:
						slots := l2Slots(t, h, rid)
						if len(slots) == 0 {
							continue
						}
						sop = map[string]any{"op": "set_side", "slot_id": slots[rnd.Intn(len(slots))]["id"], "side": []any{"A", "B", nil}[rnd.Intn(3)]}
					}
					resp := h.Do(t, "POST", "/api/records/"+rid+"/slots", map[string]any{"revision": rev, "ops": []any{sop}})
					ok("edit", resp.Status, resp.Body)
				case 6: // publish or unpublish
					rid := recs[rnd.Intn(len(recs))]
					resp := h.Do(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": recordRevision(t, r, rid), "published": rnd.Intn(2) == 0})
					ok("publish", resp.Status, resp.Body)
				case 7: // a tape through the fake pipeline
					rid := recs[rnd.Intn(len(recs))]
					resp := h.Do(t, "POST", "/api/records/"+rid+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", "prop-tape-"+strconv.Itoa(seed)+"-"+strconv.Itoa(i))
					ok("export", resp.Status, resp.Body)
					for r.Tapes.RunOne(t) {
					}
				case 8:
					r.Settle(t)
				default:
					if _, err := r.Services.Tapes.Sweep(t.Context(), -time.Hour); err != nil {
						t.Fatal(err)
					}
				}
			}
			r.Settle(t)
			l2Verify(t, r)
			if an, err := r.Provenance.Verify(t.Context()); err != nil || len(an) != 0 {
				t.Errorf("provenance.Verify: %v %v", an, err)
			}
			for _, rid := range recs {
				last := map[string]any{}
				for _, row := range l2Changes(t, h, rid) {
					sid, _ := row["slot_id"].(string)
					switch row["kind"] {
					case "slot_added", "slot_filled", "slot_upgraded", "slot_emptied", "slot_pinned", "slot_unpinned":
						if to := obj(row["to"]); to != nil {
							last[sid] = to["id"]
						} else {
							last[sid] = nil
						}
					}
				}
				for _, s := range l2Slots(t, h, rid) {
					var got any
					if tk := obj(s["take"]); tk != nil {
						got = tk["id"]
					}
					if s["state"] == "pin_ineligible" {
						continue // the slot still names its pin; the changelog says what the slot resolved to before
					}
					if last[s["id"].(string)] != got {
						t.Errorf("record %s slot %v: the changelog replays to %v, the slot resolves to %v", rid, s["position"], last[s["id"].(string)], got)
					}
				}
			}
		})
	}
}
