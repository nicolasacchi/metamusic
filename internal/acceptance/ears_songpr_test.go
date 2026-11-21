package acceptance_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_Ears_SongPRAfterListeningCheck: a take that got its listening check (measured sections, which start at the first
// sung word and here run on the document's clock, not the plan's) still takes a Song PR that addresses fix comments the way the
// take had before the check: the comments land in the plan's sections, not in the gaps of the measured ones.
func TestAcceptance_Ears_SongPRAfterListeningCheck(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{M2b: servertest.M2bOptions{Backfill: true}})
	h := r.Human(t)
	_, track, _ := earsRender(t, r, h, "ears-pr", "italian, synth pop, 100 bpm", nil)
	if n := count(t, r, `SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'measured'`, track); n == 0 {
		t.Fatal("no measured sections: the check did not run")
	}
	var ids []string
	for i, part := range []float64{0.4, 0.7} {
		var dur float64
		if err := r.Store.R.QueryRow(`SELECT duration_s FROM tracks WHERE id = ?`, track).Scan(&dur); err != nil {
			t.Fatal(err)
		}
		c := obj(h.OK(t, "POST", "/api/tracks/"+track+"/comments", map[string]any{"body": "fix this", "kind": "fix", "at_second": float64(int(dur*part*10)) / 10},
			"Idempotency-Key", "ears-pr-comment-"+string(rune('a'+i))))
		ids = append(ids, c["id"].(string))
	}
	resp := h.Do(t, "POST", "/api/patches", map[string]any{"base_track_id": track, "addresses_comment_ids": ids, "rationale": "tighten",
		"ops": []any{map[string]any{"op": "replace_line", "section_idx": 0, "line_idx": 0, "text": "la la"}}}, "Idempotency-Key", "ears-pr-patch")
	if resp.Status != 201 {
		t.Fatalf("propose after the listening check: %d %s", resp.Status, resp.Body)
	}
}
