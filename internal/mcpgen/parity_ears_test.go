package mcpgen_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// earsParityIDs are the M5b Ears operations that exist on this branch (grows with each lane; all generic: D-EARS-17).
var earsParityIDs = []string{"get_track_tier_b"}

// earsCases are the parity cases of the Ears operations. The shared script runs over a library with no listening check: 409 with
// the state, and 404; the populated half is TestOps_Ears_ParityMCP.
var earsCases = []ParityCase{
	{Op: "get_track_tier_b", Variant: "no listening check", Args: fixedArgs(map[string]any{"id": earstest.ClipTrack}), Status: 409},
	{Op: "get_track_tier_b", Variant: "unknown take", Args: fixedArgs(map[string]any{"id": "nope"}), Status: 404},
}

func init() { parityCases = append(parityCases, earsCases...) }

// TestParity_EarsCases: every Ears operation with an MCP face has a case and is generic, never a dedicated tool.
func TestParity_EarsCases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, op := range api.Operations(r.api) {
		if slices.Contains(earsParityIDs, op.ID) && op.Meta.MCP != "generic" {
			t.Errorf("%s is %q over MCP, want generic (D-EARS-17)", op.ID, op.Meta.MCP)
		}
	}
	for _, id := range earsParityIDs {
		if !slices.ContainsFunc(earsCases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	sess := r.client(t, r.token(t, "ears-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(earsParityIDs, tl.Name) {
			t.Errorf("%s is listed as a tool", tl.Name)
		}
	}
}

func seedEarsDoc(t *testing.T, r *servertest.Rig, trackID string, doc []byte) {
	t.Helper()
	dir := filepath.Join(r.DataDir, "takes", trackID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "analysis.json"), doc, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	if _, err := r.Store.W.Exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'analysis.json', ?, ?, ?, ?)`,
		trackID, hex.EncodeToString(sum[:]), len(doc), "takes/"+trackID+"/analysis.json", store.FormatTime(lt.FixedNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Services.Ears.AnalyzeTierB(t.Context(), trackID); err != nil {
		t.Fatal(err)
	}
}

// TestOps_Ears_ParityMCP: over a take with a listening check the 200 body is the same on REST, MCP (call_operation) and stdio.
func TestOps_Ears_ParityMCP(t *testing.T) {
	scopes := []auth.Scope{auth.ScopeRead}
	opts := parityRigOptions()
	ra, rb, rc := servertest.New(t, opts), servertest.New(t, opts), servertest.New(t, opts)
	doc := tierbtest.FromLyrics("[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away", tierbtest.Opts{Subs: map[string]string{"luna": "lena"}})
	for _, r := range []*servertest.Rig{ra, rb, rc} {
		r.Settle(t)
		seedEarsDoc(t, r, lt.TrackLuna, doc)
	}
	rest, _ := ra.Agent(t, scopes...)
	faces := map[string]*mcp.ClientSession{"mcp": rb.MCP(t, scopes...), "stdio": rc.Stdio(t, scopes...)}
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(ra.API) {
		ops[op.ID] = op
	}
	op := ops["get_track_tier_b"]
	args := map[string]any{"id": lt.TrackLuna}
	path, body, headers := restRequest(t, ra.API, op, args)
	resp := rest.Do(t, op.Method, path, body, headers...)
	if resp.Status != 200 {
		t.Fatalf("REST %d: %s", resp.Status, resp.Body)
	}
	want := servertest.Normalize(resp.JSON(t))
	for name, sess := range faces {
		res, tool := mcpRaw(t, sess, op, args, false)
		if tool != "call_operation" {
			t.Errorf("answered as the dedicated tool %s", tool)
		}
		if got := normalizeResult(t, res, op, tool, nil, false); !cmp.Equal(want, got) {
			t.Errorf("REST vs %s (-rest +%s):\n%s", name, name, cmp.Diff(want, got))
		}
	}
}
