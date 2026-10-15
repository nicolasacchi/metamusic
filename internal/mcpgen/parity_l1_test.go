package mcpgen_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l1Cases covers the 16 L1 operations that have an MCP face (7 tools, 9 generic); pin_pick and dismiss_pick_proposal
// have none and are covered by REST-only tests (TestPin_*, TestDismiss_HumanOnly, TestOps_HumanJudgment_NoMCP). Until the
// owning lane fills a handler all three faces must answer the same 501 problem (C101.2); the lane then removes Status
// and builds the arguments from the rig's L1 rows. L1-B5 took over the Muse, Song, Session and Board cases: this runner's
// rig has no L1 rows (L1 false), so the list and Board cases agree on an empty library, create_muse creates one Muse and
// the id-addressed cases agree on the same 404 problem. L1-B7 enriches them with the settled L1 rig.
var l1Cases = []ParityCase{
	{Op: "list_muses", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_muse", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "create_muse", Masks: []string{"id", "created_by"}, Args: fixedArgs(map[string]any{"kind": "reference", "title": "Synthetic Muse 900", "artist": "Synthetic Artist 900"})},
	{Op: "update_muse", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "revision": 1, "title": "Synthetic Muse 901"})},
	{Op: "assign_muse_songs", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "song_ids": []string{lt.TrackCitta}})},
	{Op: "list_songs", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_song", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "update_song", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "revision": 1, "title": "x"})},
	{Op: "propose_pick", Args: proposeArgs, Masks: []string{"id", "song_id", "proposed_by"}}, // ids come from each rig's own sequence
	{Op: "list_pick_events", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "list_sessions", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "update_session", Status: 404, Args: fixedArgs(map[string]any{"id": lt.TrackLuna, "revision": 1, "name": "x"})},
	{Op: "search_sounds", Status: 501, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "get_sound_taxonomy", Status: 501, Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "map_sound_term", Status: 501, Args: fixedArgs(map[string]any{"mappings": []map[string]any{{"term": "dub", "facet": "genre_family", "value": "reggae", "op": "add"}}})},
	{Op: "get_muse_board", Masks: []string{"id"}, Args: fixedArgs(map[string]any{"limit": 10})},
}

// proposeArgs proposes the non-Pick take of the first Song with two live takes in the rig's base fixture (the Reconciler
// builds the Songs structurally; the Pick of a settled Song is its newest take, so the oldest is proposed). The rigs are
// identical, so the three faces see the same ids.
func proposeArgs(r *servertest.Rig, _ map[string]any) map[string]any {
	if _, err := r.Provenance.Settle(context.Background()); err != nil {
		panic(err)
	}
	var song, take string
	err := r.Store.R.QueryRow(`SELECT ts.song_id, (SELECT t.id FROM take_songs x JOIN tracks t ON t.id = x.track_id WHERE x.song_id = ts.song_id
		AND t.status <> 'discarded' ORDER BY t.recorded_at LIMIT 1)
		FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE t.status <> 'discarded' GROUP BY ts.song_id HAVING COUNT(*) >= 2 ORDER BY ts.song_id LIMIT 1`).Scan(&song, &take)
	if err != nil {
		panic(err)
	}
	return map[string]any{"id": song, "take_id": take, "rationale": "parity"}
}

func init() {
	parityCases = append(parityCases, l1Cases...)
}

// l1Tools are the first-class L1 tools (D-L1-20).
//
// list_songs was demoted to generic by L1-B5 (D-L1-20, the first step of the demotion order): binding its 14 filters
// over REST and MCP added ~810 bytes to it and to search_sounds, taking the seven tools from 15 285 to 16 905 compact
// bytes, over the 16 000 rule. It stays fully reachable through call_operation with the same arguments.
var l1Tools = []string{"list_muses", "get_muse", "create_muse", "get_song", "propose_pick", "search_sounds"}

// TestParity_L1Cases: a case exists for each of the 16 ops with an MCP face, and for no other (the runner itself fails
// on a missing case by name, TestParityEveryOperation).
func TestParity_L1Cases(t *testing.T) {
	t.Parallel()
	if len(l1Cases) != 16 {
		t.Fatalf("%d L1 parity cases, want 16", len(l1Cases))
	}
	seen := map[string]bool{}
	for _, c := range l1Cases {
		if seen[c.Op] {
			t.Errorf("duplicate case for %s", c.Op)
		}
		seen[c.Op] = true
		if c.Op == "pin_pick" || c.Op == "dismiss_pick_proposal" {
			t.Errorf("%s has no MCP face and must not have a parity case", c.Op)
		}
	}
}

// TestToolsList_L1Budget: the first-class L1 tools cost at most 16 000 compact bytes of tools/list (D-L1-20, the
// M2b/M3 rule); the demotion order list_songs, search_sounds, create_muse, get_muse applies otherwise. It prints the
// measurement.
func TestToolsList_L1Budget(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l1-budget", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, n := range l1Tools {
		want[n] = true
	}
	total, found := 0, 0
	for _, tl := range res.Tools {
		if want[tl.Name] {
			b, err := json.Marshal(tl)
			if err != nil {
				t.Fatal(err)
			}
			total += len(b)
			found++
		}
	}
	t.Logf("the %d L1 tools take %d compact bytes of tools/list (rule: 16 000)", found, total)
	if found != len(l1Tools) {
		t.Fatalf("found %d of the %d L1 tools in tools/list", found, len(l1Tools))
	}
	if total > 16000 {
		t.Errorf("the L1 tools take %d bytes, over 16 000: demote list_songs, search_sounds, create_muse, get_muse in that order", total)
	}
}

// TestOps_HumanJudgment_NoMCP: pin_pick and dismiss_pick_proposal are human judgment (D-L1-11): neither is a tool nor
// reachable through call_operation.
func TestOps_HumanJudgment_NoMCP(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l1-judgment", auth.ScopeRead, auth.ScopeWrite))
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "pin_pick" || tl.Name == "dismiss_pick_proposal" {
			t.Errorf("%s must not be listed", tl.Name)
		}
	}
	for _, op := range []string{"pin_pick", "dismiss_pick_proposal"} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": op, "arguments": map[string]any{"id": "x"}}})
		if err != nil {
			t.Fatal(err)
		}
		var txt string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				txt += tc.Text
			}
		}
		if !res.IsError || !strings.Contains(txt, "NOT_FOUND") {
			t.Errorf("call_operation(%s) must answer NOT_FOUND: %+v %s", op, res.IsError, txt)
		}
	}
}
