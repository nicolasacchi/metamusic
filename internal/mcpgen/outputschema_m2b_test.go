package mcpgen_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var m2bOps = map[string]bool{"get_clip": true, "create_duel": true, "next_duel": true, "get_duel": true, "list_duels": true, "submit_duel": true,
	"get_ladder": true, "get_track_analysis": true, "get_track_spectrogram": true, "compare_tracks": true}

func resolve(t *testing.T, name string, schema any) *jsonschema.Resolved {
	t.Helper()
	b, _ := json.Marshal(schema)
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: resolve: %v", name, err)
	}
	return rs
}

func fixtureJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "fixtures", "m2b", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestToolResultsConformToOutputSchema_M2b: MCP clients reject structured results that do not conform to the
// tool's outputSchema, so (1) every successful result of an M2b parity case on the HTTP and stdio faces and
// (2) the typed fixtures (the shapes the lanes must return, nullable objects included) validate against the
// advertised schema of first-class tools and against describe_operation's full schema for generic ones.
func TestToolResultsConformToOutputSchema_M2b(t *testing.T) {
	t.Parallel()
	scopes := []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	httpSess, stdioSess := r.MCP(t, scopes...), r.Stdio(t, scopes...)
	ctx := context.Background()

	lres, err := httpSess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	advertised := map[string]*jsonschema.Resolved{}
	for _, tl := range lres.Tools {
		if m2bOps[tl.Name] && tl.OutputSchema != nil {
			advertised[tl.Name] = resolve(t, tl.Name, tl.OutputSchema)
		}
	}
	for _, name := range []string{"next_duel", "submit_duel", "get_track_analysis", "get_track_spectrogram"} {
		if advertised[name] == nil {
			t.Errorf("first-class tool %s has no advertised outputSchema", name)
		}
	}

	// (1) the results of the parity script on both faces
	for _, c := range parityCases {
		rs, ok := advertised[c.Op]
		if !ok {
			continue
		}
		for face, sess := range map[string]*mcp.ClientSession{"http": httpSess, "stdio": stdioSess} {
			var args map[string]any
			if c.Args != nil {
				args = c.Args(r)
			}
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: c.Op, Arguments: args})
			if err != nil {
				t.Fatalf("%s/%s: %v", c.Op, face, err)
			}
			if res.IsError {
				if c.Status == 0 {
					t.Errorf("%s/%s: tool error with real services: %+v", c.Op, face, res.Content)
				}
				continue // an expected failure: the problem body is not a structured result
			}
			if err := rs.Validate(normalize(res.StructuredContent)); err != nil {
				t.Errorf("%s/%s: result violates its outputSchema: %v", c.Op, face, err)
			}
		}
	}

	// (2) typed fixtures against the schemas
	open, revealed := fixtureJSON(t, "duel_open_blind.json"), fixtureJSON(t, "duel_revealed.json")
	verdict := revealed["human_verdict"]
	spec := fixtureJSON(t, "spectrogram_view.json")
	specInline := fixtureJSON(t, "spectrogram_view.json")
	specInline["image"] = map[string]any{"mime_type": "image/jpeg"} // data_base64 is stripped from MCP results
	cases := []struct {
		op    string
		value any
	}{
		{"next_duel", map[string]any{"duel": open, "open_total": 2}},
		{"next_duel", map[string]any{"duel": revealed, "open_total": 1}},
		{"next_duel", map[string]any{"duel": nil, "open_total": 0}},
		{"submit_duel", map[string]any{"duel": revealed, "verdict": verdict, "replayed": false}},
		{"get_track_analysis", fixtureJSON(t, "dossier.json")},
		{"get_track_spectrogram", spec},
		{"get_track_spectrogram", specInline},
		{"create_duel", open},
		{"get_duel", revealed},
		{"get_ladder", fixtureJSON(t, "ladder_page.json")},
		{"compare_tracks", fixtureJSON(t, "comparison.json")},
	}
	for i, c := range cases {
		rs := advertised[c.op]
		if rs == nil { // generic: the full schema is one describe_operation call away
			res, err := httpSess.CallTool(ctx, &mcp.CallToolParams{Name: "describe_operation", Arguments: map[string]any{"operation_id": c.op}})
			if err != nil || res.IsError {
				t.Fatalf("describe_operation %s: %v %+v", c.op, err, res)
			}
			doc := res.StructuredContent.(map[string]any)
			rs = resolve(t, c.op, doc["output_schema"])
		}
		if err := rs.Validate(normalize(c.value)); err != nil {
			t.Errorf("case %d (%s): fixture violates the output schema: %v", i, c.op, err)
		}
	}
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
