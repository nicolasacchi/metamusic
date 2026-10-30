package mcpgen_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m2cCases are the M2c cases: the changed shapes of the first-class tools over the reference rows. All three
// faces (REST, tool, call_operation) must answer the same.
var m2cCases = []ParityCase{
	{Op: "search_tracks", Variant: "recipe_unknown", Args: fixedArgs(map[string]any{"recipe_known": false, "limit": 10})},
	{Op: "search_tracks", Variant: "lossy", Args: fixedArgs(map[string]any{"lossy": true, "limit": 10})},
	{Op: "get_media_links", Variant: "lossy", Args: fixedArgs(map[string]any{"id": lt.TrackLossy})},
	{Op: "get_media_links", Variant: "reference_flac", Args: fixedArgs(map[string]any{"id": lt.TrackRefFlac})},
	{Op: "get_track", Variant: "lossy", Args: fixedArgs(map[string]any{"id": lt.TrackLossy})},
}

func init() {
	parityCases = append(parityCases, m2cCases...)
}

// TestParity_M2c_Shapes: the lossy media links carry a null flac over MCP too, and the new filters are
// advertised on search_tracks.
func TestParity_M2c_Shapes(t *testing.T) {
	t.Parallel()
	sr := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true})
	r := &rig{url: sr.URL, tokens: sr.Services.Tokens, api: sr.API}
	sess := r.client(t, r.token(t, "m2c", auth.ScopeRead))
	res, err := sess.CallTool(callCtx(), &mcp.CallToolParams{Name: "get_media_links", Arguments: map[string]any{"id": lt.TrackLossy}})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := structured(t, res).(map[string]any)
	if !ok {
		t.Fatalf("structured: %v", res.StructuredContent)
	}
	if v, present := m["flac"]; !present || v != nil || m["lossy"] != true {
		t.Errorf("flac=%v present=%v lossy=%v", v, present, m["lossy"])
	}
}
