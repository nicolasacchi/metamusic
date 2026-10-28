package mcpgen_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// toolsCeilingBytes is the absolute C41 ceiling: the compact-JSON tools array
// of tools/list (about 10 000 tokens at 4 bytes per token).
//
// M2b-B2 (D-M2b-22) re-baselined 40000 -> 55500 (measured 55308, about 13 800 tokens): the four tools agents
// call every session (next_duel, submit_duel, get_track_analysis, get_track_spectrogram) are first-class, and
// create_duel, get_ladder and compare_tracks were demoted to mcp generic (call_operation) because the seven
// M2b tools alone were 28 198 bytes, over the 16 000 byte rule.
//
// M2c-B1 re-baselined 55500 -> 56200 (measured 56122, +814): the nullable flac link and lossy flag of
// get_media_links, recipe_known/lossy on the first-class track shapes and two search_tracks parameters.
//
// M3-B2 (D-M3-36) re-baselined 56200 -> 87300 (measured 87295, +31 173, 32 tools): seven first-class M3 tools whose
// output schemas (Estimate, Batch, Job) are ~3 KB each plus the generation/env fields of the track shapes. D-M3-36
// demoted get_budget, list_jobs, get_batch and request_approval to generic and shortened every M3 description to
// 200 characters; the seven M3 tools alone are still ~29 000 bytes, over the 16 000 byte rule, and the remaining
// steps (demoting M2 tools) were NOT taken: the plan says to stop and ask the operator (see the M3-B2 report).
//
// L1-B2 (D-L1-20) re-baselined 87300 -> 102600 (measured 102587, +15 287, 39 tools): the seven first-class L1 tools at that point (six after the list_songs demotion below)
// (list_muses, get_muse, create_muse, list_songs, get_song, propose_pick, search_sounds) take 15 285 compact bytes, inside
// the 16 000 byte rule (TestToolsList_L1Budget), so no demotion was needed; the nine other L1 operations and the two
// human-judgment ones add nothing to tools/list.
//
// L1-B5 bound the list_songs and search_sounds filters (C96, +~810 bytes each) and the C98 track fields; the seven tools
// then measured 16 905, so list_songs was demoted to generic (D-L1-20): the six remaining L1 tools take ~14 700. The ceiling is re-baselined 102600 -> 103500 (measured 103405) for the
// C98 fields (song_id, is_pick, session_id, TrackDetail.song, the /context sections and library_model).
//
// Merging main (songwriting targets) re-baselined 103500 -> 104300 (measured 104266, +861): get_songwriting_settings and the
// lint Issue's optional details object.
//
// M4a-X1 merge: 104300 -> 104500 (measured 104472, +206): the nullable cover object of get_media_links.
//
// Wave 4b: 104500 -> 104700 (measured 104656, +156): song_title and song_angle on the TrackSummary rows.
//
// Wave 4a (D1) on top of 4b: 104700 -> 104800 (measured 104699, +43): the nullable step_up_until of whoami.
//
// L2-F2 (D-L2-21) re-baselined 104800 -> 107100 (measured 106964, +2 265, 39 tools): L2 adds no dedicated tool. The five planned
// tools measured 15 645 bytes, over the 9 000 byte rule; after the rule's demotions get_record and propose_record still took
// 10 940, and the operator decided on 2026-10-28 to make them generic too (15 generic, 2 human-only). The +2 265 bytes are
// the curation filters of search_sounds and search_tracks and the L2 fields of get_song, get_muse, get_stats and the
// context bundle.
const toolsCeilingBytes = 107100

// TestToolsList_Ceiling: tools/list stays within the absolute ceiling. The
// measured size at the M2a-B4a contract freeze is recorded in its commit message.
func TestToolsList_Ceiling(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "ceiling", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.Tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tools/list: %d tools, %d compact bytes (ceiling %d)", len(res.Tools), len(b), toolsCeilingBytes)
	if len(b) > toolsCeilingBytes {
		t.Fatalf("tools/list is %d compact bytes, over the %d ceiling: demote first-class tools to generic (C41)", len(b), toolsCeilingBytes)
	}
}

// TestToolsList_SlimOutputSchemas: advertised output schemas carry the result
// shape only; descriptions stay available through describe_operation.
func TestToolsList_SlimOutputSchemas(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "slim", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.OutputSchema == nil {
			continue
		}
		b, _ := json.Marshal(tl.OutputSchema)
		var doc map[string]any
		_ = json.Unmarshal(b, &doc)
		var walk func(path string, v any)
		walk = func(path string, v any) {
			m, ok := v.(map[string]any)
			if !ok {
				return
			}
			for k, x := range m {
				switch k {
				case "description", "additionalProperties", "required":
					if path != "properties" { // a property may be called "description"
						t.Errorf("%s: output schema keeps %q at %s", tl.Name, k, path)
					}
				}
				walk(k, x)
			}
		}
		walk("", doc)
	}
	// the full documentation is one call away
	desc, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "describe_operation", Arguments: map[string]any{"operation_id": "get_track"}})
	if err != nil || desc.IsError {
		t.Fatalf("describe_operation: %v %+v", err, desc)
	}
	b, _ := json.Marshal(desc.StructuredContent)
	var doc struct {
		OutputSchema map[string]any `json:"output_schema"`
	}
	_ = json.Unmarshal(b, &doc)
	if doc.OutputSchema == nil {
		t.Errorf("describe_operation lost the full output schema: %s", b)
	}
}

// TestOutputSchemas_M2aShapes validates filled-in values of the new output
// types against the schemas tools/list advertises: nullable fields are declared
// nullable, and keys such as _budget and _links pass.
func TestOutputSchemas_M2aShapes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "shapes", auth.ScopeRead))
	lres, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tl := range lres.Tools {
		if tl.OutputSchema == nil {
			continue
		}
		b, _ := json.Marshal(tl.OutputSchema)
		var s jsonschema.Schema
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", tl.Name, err)
		}
		rs, err := s.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: %v", tl.Name, err)
		}
		schemas[tl.Name] = rs
	}
	stars := 4
	ref := auth.PrincipalRef{Kind: "human", ID: "human", Name: "human"}
	row := &library.RatingRow{Stars: &stars, Verdict: nil, By: ref, UpdatedAt: "2026-10-06T12:00:00Z"}
	lint := yue2.LintResult{Errors: []yue2.Issue{}, Warnings: []yue2.Issue{{Code: "no_sections", Severity: "warning", Message: "m"}}, EstimatedDurationS: 105.6, DurationBasis: "lyrics_default_bpm"}
	draft := library.DraftDetail{Sections: []library.DraftSection{}, Collections: []string{}, Group: []library.GroupMember{}, Lint: lint, Revision: 2, Editable: true}
	draft.Request.ABC = new(string)
	tc := judgments.Comment{ID: "c1", TrackID: "t1", Body: "b", Link: "/tracks/t1?t=1.0", URL: "http://x/tracks/t1?t=1.0", Kind: "fix", State: "open", Source: "mcp", Author: ref}
	tests := []struct {
		tool  string
		value any
	}{
		{"rate_track", library.TrackRating{TrackID: "t1", Human: row, Critic: nil}},
		{"rate_track", library.TrackRating{TrackID: "t1", Human: nil, Critic: row}},
		{"set_tags", library.TrackTags{TrackID: "t1", Tags: []library.TagRef{{Name: "ballad", Sources: []string{"human", "inherited"}}}}},
		{"add_comment", tc},
		{"update_comment", func() judgments.Comment {
			c := tc
			at := 12.3
			c.AtSecond, c.Section = &at, &judgments.SectionRef{Label: "chorus 1", Source: "plan", Confidence: 0.7}
			return c
		}()},
		{"create_draft", draft},
		{"lint_recipe", yue2.LintReport{OK: true, Items: []yue2.LintResult{lint}, BatchErrors: []yue2.Issue{}}},
		{"list_playlists", map[string]any{"items": []playlists.PlaylistSummary{{ID: "p1", Name: "n", ItemsCount: 1, Revision: 1}}}},
		{"create_playlist", playlists.Playlist{ID: "p1", Items: []playlists.Item{{Position: 1, Track: library.TrackSummary{StyleTerms: []string{}, Tags: []string{}}}}}},
		{"export_jsonl", exports.Export{ID: "e1", Kind: "songs_jsonl", JSONL: &exports.JSONLInfo{Renamed: []exports.Rename{}, Validation: exports.Validation{OK: true, Issues: []exports.ExportIssue{}}, ABCFiles: []exports.ABCFile{}, Warnings: []export.Note{}, Style: exports.StyleInfo{KeyOrder: []string{}}}}},
		{"get_track_context", trackctx.Bundle{Format: "mm.context/1", Track: map[string]any{"id": "t1", "stars": nil},
			Recipe: map[string]any{}, Provenance: map[string]any{}, Lineage: map[string]any{}, Judgments: map[string]any{}, Perception: map[string]any{}, Listening: map[string]any{}, NextActions: []string{"x"},
			Links: map[string]any{"stream": map[string]any{"mp3": "u"}}, Budget: trackctx.Budget{Chars: 100, Limit: 24000, Truncated: []string{}}}},
	}
	for _, c := range tests {
		rs, ok := schemas[c.tool]
		if !ok {
			t.Errorf("%s: no advertised outputSchema", c.tool)
			continue
		}
		b, err := json.Marshal(c.value)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		_ = json.Unmarshal(b, &v)
		if err := rs.Validate(v); err != nil {
			t.Errorf("%s: %s violates its outputSchema: %v", c.tool, b, err)
		}
	}
}
