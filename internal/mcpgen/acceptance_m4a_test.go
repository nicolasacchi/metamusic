package mcpgen_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M4a_ResourcesAndPrompts: the three MCP faces (PAT over HTTP, an OAuth connector over HTTP, stdio) list
// the same resource templates and prompts and return, byte for byte, what the REST operations return for a principal
// with the same scopes: the context resource equals get_track_context, the prompts equal get_prompt, the listening
// summary prompt included. Behind the real wiring of app.Build (no stub, no fake source).
func TestAcceptance_M4a_ResourcesAndPrompts(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, M4a: true, OIDC: true,
		Backend: "fake", M2b: servertest.M2bOptions{Fixture: true}})
	connector, _ := r.Connector(t, servertest.ConnectorOpts{Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}})
	faces := map[string]*mcp.ClientSession{"pat": r.MCP(t, readScopes...), "oauth": connector, "stdio": r.Stdio(t, readScopes...)}
	rest, _ := newGatedFace(t, r, readScopes...)

	status, wantCtx := rest.get(t, "/api/tracks/"+lt.TrackLuna+"/context")
	if status != 200 {
		t.Fatalf("REST context: %d %s", status, wantCtx)
	}
	cases := []struct {
		name string
		args map[string]string
	}{
		{"critique_track", map[string]string{"track_id": lt.TrackLuna}},
		{"write_variant", map[string]string{"track_id": lt.TrackLuna, "goal": "a quieter bridge"}},
		{"session_review", nil},
	}
	var wantPrompts []restPrompt
	for _, c := range cases {
		wantPrompts = append(wantPrompts, restGetPrompt(t, rest, c.name, c.args))
	}

	for name, s := range faces {
		tmpl, err := s.ListResourceTemplates(callCtx(), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(tmpl.ResourceTemplates) != len(mcpgen.Resources) {
			t.Fatalf("%s: %d resource templates, want %d", name, len(tmpl.ResourceTemplates), len(mcpgen.Resources))
		}
		for i, rs := range mcpgen.Resources {
			if g := tmpl.ResourceTemplates[i]; g.URITemplate != rs.URITemplate || g.MIMEType != rs.MIMEType {
				t.Errorf("%s: template %d = %+v", name, i, g)
			}
		}
		res, err := readResource(t, s, "metamusic://tracks/"+lt.TrackLuna+"/context")
		if err != nil || len(res.Contents) != 1 {
			t.Fatalf("%s: context resource: %v %+v", name, err, res)
		}
		if diff := cmp.Diff(string(wantCtx), res.Contents[0].Text); diff != "" {
			t.Errorf("%s: context resource differs from get_track_context (-rest +resource):\n%s", name, diff)
		}
		list, err := s.ListPrompts(callCtx(), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := map[string]bool{}
		for _, p := range list.Prompts {
			got[p.Name] = true
		}
		for _, c := range cases {
			if !got[c.name] {
				t.Errorf("%s: prompt %s not listed (%v)", name, c.name, got)
			}
		}
		for i, c := range cases {
			p, err := getMCPPrompt(t, s, c.name, c.args)
			if err != nil {
				t.Fatalf("%s %s: %v", name, c.name, err)
			}
			sameMessages(t, name+"/"+c.name, wantPrompts[i], p)
		}
	}
}
