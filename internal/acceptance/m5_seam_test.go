package acceptance_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M5_PatchLineAddressing is the S1/W1 seam on real imported recipes: the Track page sends replace_line ops as
// SectionOut.idx (track.sections) plus the index of the line among the section's lines, and the server reads them against
// yue2.LyricsSections of the base lyrics. For every known-recipe take of the synthetic archive (HTML-ish text, accents, two and three
// sections) the two numberings agree, section by section and line by line, and replacing (idx, line) through propose_patch changes
// exactly that line of exactly that section.
func TestAcceptance_M5_PatchLineAddressing(t *testing.T) {
	r := archiveRig(t, servertest.Options{})
	h := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	rows, err := r.Store.R.Query(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE rc.engine <> 'unknown' AND t.lossy = 0 ORDER BY t.id`)
	if err != nil {
		t.Fatal(err)
	}
	var tracks []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, id)
	}
	_ = rows.Close()
	if len(tracks) < 5 {
		t.Fatalf("only %d known-recipe takes", len(tracks))
	}
	proposed, shapes := 0, map[string]bool{}
	for _, id := range tracks {
		tr := obj(h.OK(t, "GET", "/api/tracks/"+id, nil))
		lyrics := obj(tr["recipe"])["lyrics"].(string)
		want := yue2.LyricsSections(lyrics)
		shapes[fmt.Sprint(len(want))] = true
		got := arr(tr["sections"])
		if len(got) != len(want) {
			t.Fatalf("%s: track.sections has %d entries, yue2.LyricsSections %d", id, len(got), len(want))
		}
		note := obj(agent.OK(t, "POST", "/api/tracks/"+id+"/comments", map[string]any{"body": "line check", "kind": "note"}, "Idempotency-Key", "seam-c-"+id))["id"].(string)
		for i, w := range want {
			s := obj(got[i])
			var lines []string
			for _, l := range arr(s["lines"]) {
				lines = append(lines, l.(string))
			}
			if int(s["idx"].(float64)) != w.Idx || s["raw"] != w.Raw || !slices.Equal(lines, w.Lines) {
				t.Fatalf("%s: SectionOut %d = %v, yue2.LyricsSections says %+v", id, i, s, w)
			}
			for j, before := range lines {
				resp := agent.Do(t, "POST", "/api/patches", map[string]any{"base_track_id": id, "addresses_comment_ids": []string{note}, "rationale": "seam", "extra_sections": []int{w.Idx},
					"ops": []map[string]any{{"op": "replace_line", "section_idx": w.Idx, "line_idx": j, "text": "replaced line"}}}, "Idempotency-Key", fmt.Sprintf("seam-%s-%d-%d", id, w.Idx, j))
				if resp.Status != 201 {
					t.Fatalf("%s section %d line %d: %d %s", id, w.Idx, j, resp.Status, resp.Body)
				}
				p := obj(resp.JSON(t))
				d := arr(obj(p["diff"])["lyrics"])
				if len(d) != 1 || obj(d[0])["before"] != before || obj(d[0])["section_idx"] != float64(w.Idx) || obj(d[0])["line_idx"] != float64(j) {
					t.Errorf("%s section %d line %d: diff %v, want before %q", id, w.Idx, j, d, before)
				}
				after := yue2.LyricsSections(obj(p["compiled"])["lyrics"].(string))
				wantAfter := yue2.LyricsSections(lyrics)
				wantAfter[w.Idx].Lines = slices.Clone(wantAfter[w.Idx].Lines)
				wantAfter[w.Idx].Lines[j] = "replaced line"
				if len(after) != len(wantAfter) || !slices.Equal(after[w.Idx].Lines, wantAfter[w.Idx].Lines) || strings.Count(obj(p["compiled"])["lyrics"].(string), "replaced line") != 1 {
					t.Errorf("%s section %d line %d: the edit landed elsewhere: %q", id, w.Idx, j, obj(p["compiled"])["lyrics"])
				}
				proposed++
			}
		}
	}
	if proposed < 10 || len(shapes) < 2 {
		t.Errorf("only %d line edits over %d section-count shapes: the seam check is thin", proposed, len(shapes))
	}
}
