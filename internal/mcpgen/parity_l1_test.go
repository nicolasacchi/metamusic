package mcpgen_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l1Cases covers the 16 L1 operations that have an MCP face (6 tools, 10 generic); pin_pick and dismiss_pick_proposal
// have none and are covered by REST-only tests (TestPin_*, TestDismiss_HumanOnly, TestOps_HumanJudgment_NoMCP). Every
// face runs against its own settled L1 rig, so REST = MCP tool = generic = stdio after normalisation (C101.2).
var l1Cases = []ParityCase{
	{Op: "list_muses", Args: settledArgs(map[string]any{"limit": 10})},
	{Op: "get_muse", Args: museArgs(nil)},
	{Op: "create_muse", Masks: []string{"id", "created_by"}, Args: fixedArgs(map[string]any{"kind": "reference", "title": "Synthetic Muse 900", "artist": "Synthetic Artist 900"})},
	{Op: "update_muse", SkipGeneric: true, Masks: []string{"updated_at"}, Args: museArgs(map[string]any{"title": "Synthetic Muse 901"})},
	{Op: "assign_muse_songs", Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		settle(r)
		return map[string]any{"id": oneID(r, `SELECT id FROM muses ORDER BY id LIMIT 1`), "song_ids": []string{oneID(r, `SELECT id FROM songs WHERE retired_at IS NULL ORDER BY id LIMIT 1`)}}
	}},
	{Op: "list_songs", Args: settledArgs(map[string]any{"limit": 10})},
	{Op: "get_song", Args: songArgs(nil)},
	{Op: "update_song", SkipGeneric: true, Masks: []string{"updated_at"}, Args: songArgs(map[string]any{"title": "Synthetic Song 901"})},
	{Op: "propose_pick", Args: proposeArgs, Masks: []string{"id", "song_id", "proposed_by"}}, // ids come from each rig's own sequence
	{Op: "list_pick_events", Args: settledArgs(map[string]any{"limit": 10})},
	{Op: "list_sessions", Args: settledArgs(map[string]any{"limit": 10})},
	{Op: "update_session", SkipGeneric: true, Masks: []string{"updated_at"}, Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		settle(r)
		return map[string]any{"id": oneID(r, `SELECT id FROM sessions ORDER BY id LIMIT 1`), "revision": 1, "name": "Synthetic Session 901"}
	}},
	{Op: "search_sounds", Args: settledArgs(map[string]any{"limit": 0})}, // counts only: the Song page is list_songs
	{Op: "get_sound_taxonomy", Args: settledArgs(map[string]any{"limit": 10})},
	{Op: "map_sound_term", Args: fixedArgs(map[string]any{"mappings": []map[string]any{{"term": "dub", "facet": "genre_family", "value": "reggae", "op": "add"}}})},
	{Op: "get_muse_board", Masks: []string{"id"}, Args: settledArgs(map[string]any{"limit": 10})},
}

func settle(r *servertest.Rig) {
	if err := r.Services.Sounds.EnsureSeed(context.Background()); err != nil {
		panic(err)
	}
	if _, err := r.Provenance.Settle(context.Background()); err != nil {
		panic(err)
	}
}

func oneID(r *servertest.Rig, q string) string {
	var id string
	if err := r.Store.R.QueryRow(q).Scan(&id); err != nil {
		panic(err)
	}
	return id
}

// settledArgs returns m after settling the rig, so read cases compare the derived rows on every face.
func settledArgs(m map[string]any) func(*servertest.Rig, map[string]any) map[string]any {
	return func(r *servertest.Rig, _ map[string]any) map[string]any { settle(r); return m }
}

func withID(id string, extra map[string]any) map[string]any {
	out := map[string]any{"id": id}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// museArgs addresses the first Muse of the settled rig; extra adds update fields (revision 1 is the backfilled row's).
func museArgs(extra map[string]any) func(*servertest.Rig, map[string]any) map[string]any {
	return func(r *servertest.Rig, _ map[string]any) map[string]any {
		settle(r)
		m := withID(oneID(r, `SELECT id FROM muses ORDER BY id LIMIT 1`), extra)
		if extra != nil {
			m["revision"] = 1
		}
		return m
	}
}

func songArgs(extra map[string]any) func(*servertest.Rig, map[string]any) map[string]any {
	return func(r *servertest.Rig, _ map[string]any) map[string]any {
		settle(r)
		m := withID(oneID(r, `SELECT id FROM songs WHERE retired_at IS NULL ORDER BY id LIMIT 1`), extra)
		if extra != nil {
			var rev int
			if err := r.Store.R.QueryRow(`SELECT revision FROM songs WHERE id = ?`, m["id"]).Scan(&rev); err != nil {
				panic(err)
			}
			m["revision"] = rev // assign_muse_songs ran first and bumped it
		}
		return m
	}
}

// proposeArgs proposes a non-Pick take of the first Song with two live takes in the rig's base fixture. The rigs are
// identical, so the three faces see the same ids.
func proposeArgs(r *servertest.Rig, _ map[string]any) map[string]any {
	settle(r)
	var song, take string
	err := r.Store.R.QueryRow(`SELECT ts.song_id, (SELECT t.id FROM take_songs x JOIN tracks t ON t.id = x.track_id
		WHERE x.song_id = ts.song_id AND t.status <> 'discarded' AND t.id <> sp.take_id ORDER BY t.recorded_at, t.id LIMIT 1)
		FROM take_songs ts JOIN tracks t ON t.id = ts.track_id JOIN song_picks sp ON sp.song_id = ts.song_id
		WHERE t.status <> 'discarded' GROUP BY ts.song_id HAVING COUNT(*) >= 2 ORDER BY ts.song_id LIMIT 1`).Scan(&song, &take)
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

// TestToolsList_L1Budget: the six first-class L1 tools cost at most 16 000 compact bytes of tools/list (D-L1-20, the
// M2b/M3 rule); list_songs is already demoted (generic); the order search_sounds, create_muse, get_muse applies otherwise. It prints the
// measurement.
func TestToolsList_L1Budget(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l1-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
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
		t.Errorf("the L1 tools take %d bytes, over 16 000: list_songs is already generic: demote search_sounds, create_muse, get_muse in that order", total)
	}
}

// TestOps_HumanJudgment_NoMCP: pin_pick and dismiss_pick_proposal are human judgment (D-L1-11): neither is a tool nor
// reachable through call_operation.
func TestOps_HumanJudgment_NoMCP(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l1-judgment", auth.ScopeRead, auth.ScopeWrite))
	tools, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "pin_pick" || tl.Name == "dismiss_pick_proposal" {
			t.Errorf("%s must not be listed", tl.Name)
		}
	}
	for _, op := range []string{"pin_pick", "dismiss_pick_proposal"} {
		res, err := sess.CallTool(callCtx(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": op, "arguments": map[string]any{"id": "x"}}})
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
