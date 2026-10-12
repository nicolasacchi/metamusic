package mcpgen_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestToolResultsConformToOutputSchema validates real tool results against the
// outputSchema advertised by tools/list (MCP requires structured results to
// conform). Nullable sub-objects such as track.truncated must be declared so.
func TestToolResultsConformToOutputSchema(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-1", auth.ScopeRead)
	sess := r.client(t, tok)
	ctx := context.Background()

	lres, err := sess.ListTools(ctx, nil)
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
			t.Fatalf("%s: resolve: %v", tl.Name, err)
		}
		schemas[tl.Name] = rs
	}

	calls := []mcp.CallToolParams{
		{Name: "search_tracks", Arguments: map[string]any{"limit": 10}},
		{Name: "list_drafts", Arguments: map[string]any{"limit": 10}},
		{Name: "get_draft", Arguments: map[string]any{"id": lt.Draft1}},
		{Name: "get_stats"},
		{Name: "get_media_links", Arguments: map[string]any{"id": lt.TrackLuna}},
	}
	for _, id := range []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort} {
		calls = append(calls, mcp.CallToolParams{Name: "get_track", Arguments: map[string]any{"id": id}})
	}
	for _, c := range calls {
		rs, ok := schemas[c.Name]
		if !ok {
			t.Errorf("%s: no outputSchema advertised", c.Name)
			continue
		}
		res, err := sess.CallTool(ctx, &c)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		v := structured(t, res)
		if err := rs.Validate(v); err != nil {
			t.Errorf("%s %v: result violates outputSchema: %v", c.Name, c.Arguments, err)
		}
	}
}
