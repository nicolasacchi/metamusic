package mcpgen_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// promptRig is a2Rig: app.Build wires the real prompt sources behind the gated handler every MCP face dispatches into.
func promptRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return a2Rig(t)
}

// promptFaces returns an HTTP and a stdio session of one agent, and that agent's gated REST face.
func promptFaces(t *testing.T, r *servertest.Rig, scopes ...auth.Scope) (map[string]*mcp.ClientSession, gatedFace) {
	t.Helper()
	face, tok := newGatedFace(t, r, scopes...)
	a, gated := r.Services.Gated()
	srv, err := mcpgen.NewServer(a, gated, "v0")
	if err != nil {
		t.Fatal(err)
	}
	h := mcpgen.Handler(srv)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, err := r.Services.Tokens.Verify(req.Context(), tok)
		if err != nil {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
	}))
	t.Cleanup(ts.Close)
	hs, err := mcp.NewClient(&mcp.Implementation{Name: "a2", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hs.Close() })
	return map[string]*mcp.ClientSession{"http": hs, "stdio": r.Stdio(t, scopes...)}, face
}

type restPrompt struct {
	Description string `json:"description"`
	Messages    []struct {
		Role    string `json:"role"`
		Content struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Resource *struct {
				URI      string `json:"uri"`
				MIMEType string `json:"mime_type"`
				Text     string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
	} `json:"messages"`
}

func restGetPrompt(t *testing.T, f gatedFace, name string, args map[string]string) restPrompt {
	t.Helper()
	q := url.Values{}
	for k, v := range args {
		q.Set(k, v)
	}
	status, body := f.get(t, "/api/prompts/"+name+"?"+q.Encode())
	if status != 200 {
		t.Fatalf("get_prompt %s: %d %s", name, status, body)
	}
	var p restPrompt
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func getMCPPrompt(t *testing.T, s *mcp.ClientSession, name string, args map[string]string) (*mcp.GetPromptResult, error) {
	t.Helper()
	return s.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: name, Arguments: args})
}

// sameMessages compares an MCP prompt message by message with the get_prompt REST body.
func sameMessages(t *testing.T, label string, want restPrompt, got *mcp.GetPromptResult) {
	t.Helper()
	if len(want.Messages) == 0 || len(got.Messages) != len(want.Messages) {
		t.Fatalf("%s: %d messages, want %d", label, len(got.Messages), len(want.Messages))
	}
	for i, w := range want.Messages {
		g := got.Messages[i]
		if string(g.Role) != w.Role {
			t.Errorf("%s message %d: role %q, want %q", label, i, g.Role, w.Role)
		}
		switch c := g.Content.(type) {
		case *mcp.TextContent:
			if w.Content.Type != "text" || c.Text != w.Content.Text {
				t.Errorf("%s message %d: text differs from get_prompt (type %s)", label, i, w.Content.Type)
			}
		case *mcp.EmbeddedResource:
			r := w.Content.Resource
			if w.Content.Type != "resource" || r == nil || c.Resource == nil || c.Resource.URI != r.URI ||
				c.Resource.MIMEType != r.MIMEType || c.Resource.Text != r.Text {
				t.Errorf("%s message %d: embedded resource differs from get_prompt", label, i)
			}
		default:
			t.Errorf("%s message %d: unexpected content %T", label, i, g.Content)
		}
	}
	if got.Description != want.Description {
		t.Errorf("%s: description %q, want %q", label, got.Description, want.Description)
	}
}

func TestPrompts_GetEqualsTool(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	sessions, face := promptFaces(t, r, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	cases := []struct {
		name string
		args map[string]string
	}{
		{"critique_track", map[string]string{"track_id": lt.TrackLuna}},
		{"write_variant", map[string]string{"track_id": lt.TrackLuna, "goal": "a quieter bridge"}},
		{"write_variant", map[string]string{"track_id": lt.TrackLuna}},
		{"session_review", nil},
		{"session_review", map[string]string{"since": "2026-10-01T00:00:00Z"}},
	}
	for face_, s := range sessions {
		for _, c := range cases {
			want := restGetPrompt(t, face, c.name, c.args)
			got, err := getMCPPrompt(t, s, c.name, c.args)
			if err != nil {
				t.Fatalf("%s %s: %v", face_, c.name, err)
			}
			sameMessages(t, face_+"/"+c.name, want, got)
		}
	}
}

func TestPrompts_CritiqueEmbedsContext(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	sessions, face := promptFaces(t, r, auth.ScopeRead, auth.ScopeWrite)
	status, want := face.get(t, "/api/tracks/"+lt.TrackLuna+"/context")
	if status != 200 {
		t.Fatalf("context %d", status)
	}
	for name, s := range sessions {
		got, err := getMCPPrompt(t, s, "critique_track", map[string]string{"track_id": lt.TrackLuna})
		if err != nil || len(got.Messages) != 2 {
			t.Fatalf("%s: %v %+v", name, err, got)
		}
		er, ok := got.Messages[1].Content.(*mcp.EmbeddedResource)
		if !ok || er.Resource == nil || er.Resource.Text != string(want) ||
			er.Resource.URI != "metamusic://tracks/"+lt.TrackLuna+"/context" {
			t.Errorf("%s: embedded context differs from get_track_context", name)
		}
	}
}

func TestPrompts_WriteVariantDependsOnGenerateScope(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	args := map[string]string{"track_id": lt.TrackLuna}
	text := func(scopes ...auth.Scope) string {
		sessions, _ := promptFaces(t, r, scopes...)
		var out string
		for name, s := range sessions {
			got, err := getMCPPrompt(t, s, "write_variant", args)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			tc, ok := got.Messages[0].Content.(*mcp.TextContent)
			if !ok {
				t.Fatalf("%s: first message %T", name, got.Messages[0].Content)
			}
			if out != "" && out != tc.Text {
				t.Errorf("faces disagree for the same scopes")
			}
			out = tc.Text
		}
		return out
	}
	with := text(auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	without := text(auth.ScopeRead, auth.ScopeWrite)
	if with == without {
		t.Fatal("the generate scope changes nothing in write_variant")
	}
	if !strings.Contains(with, "estimate_variant") || strings.Contains(without, "estimate_variant") ||
		!strings.Contains(without, "generate scope") {
		t.Errorf("endings wrong:\nwith: %s\nwithout: %s", with, without)
	}
}

func TestPrompts_Errors(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	sessions, _ := promptFaces(t, r, auth.ScopeRead, auth.ScopeWrite)
	for name, s := range sessions {
		for _, c := range []struct {
			label, prompt string
			args          map[string]string
			code          string
		}{
			{"unknown track", "critique_track", map[string]string{"track_id": "01900000-0000-7000-8000-00000000ffff"}, "NOT_FOUND"},
			{"reference track", "write_variant", map[string]string{"track_id": lt.TrackRefFlac}, "RECIPE_UNKNOWN"},
			{"missing required argument", "critique_track", nil, ""},
		} {
			res, err := getMCPPrompt(t, s, c.prompt, c.args)
			if err == nil {
				t.Errorf("%s %s: succeeded: %+v", name, c.label, res)
				continue
			}
			if c.code != "" {
				if _, msg := rpcCode(t, err); msg != c.code {
					t.Errorf("%s %s: error %q, want %s", name, c.label, msg, c.code)
				}
			}
		}
		if _, err := getMCPPrompt(t, s, "no_such_prompt", nil); err == nil {
			t.Errorf("%s: unknown prompt succeeded", name)
		}
	}
}

func TestPrompts_ScopeRefusal(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	sessions, _ := promptFaces(t, r, auth.ScopeWrite)
	for name, s := range sessions {
		_, err := getMCPPrompt(t, s, "session_review", nil)
		if err == nil {
			t.Fatalf("%s: a token without read got a prompt", name)
		}
		if _, msg := rpcCode(t, err); msg != "FORBIDDEN_SCOPE" {
			t.Errorf("%s: error %q, want FORBIDDEN_SCOPE", name, msg)
		}
	}
}

func goldenCheck(t *testing.T, file string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", file)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s differs from golden (-want +got; rerun with -update):\n%s", file, diff)
	}
}

func TestPrompts_ListGolden(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	for name, s := range resourceSessions(t, r, auth.ScopeRead) {
		res, err := s.ListPrompts(context.Background(), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "http" {
			goldenCheck(t, "prompts_list.golden.json", res.Prompts)
			continue
		}
		var round any
		if err := json.Unmarshal([]byte(mustJSON(t, res.Prompts)), &round); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(round, goldenOf(t, "prompts_list.golden.json")); diff != "" {
			t.Errorf("stdio prompts/list differs from the HTTP face:\n%s", diff)
		}
	}
}

func TestResources_TemplatesGolden(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	res, err := r.MCP(t, auth.ScopeRead).ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "resource_templates.golden.json", res.ResourceTemplates)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func goldenOf(t *testing.T, file string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// mcpListsBudgetBytes bounds what a client pays on connect for prompts and resource templates (C112.4).
const mcpListsBudgetBytes = 4000

func TestMCPLists_Budget(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	s := r.MCP(t, auth.ScopeRead)
	ps, err := s.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := s.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := len(mustJSON(t, ps)) + len(mustJSON(t, ts))
	if n > mcpListsBudgetBytes {
		t.Errorf("prompts/list + resources/templates/list = %d bytes, over the %d budget", n, mcpListsBudgetBytes)
	}
}
