package mcpgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// session serves the test API's MCP server behind a stub that injects an
// already-resolved agent principal, as the real auth middleware does.
func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	a, mux := testAPI(t)
	srv, err := NewServer(a, mux, "v0")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(srv)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "tester", Via: "token", Scopes: []auth.Scope{auth.ScopeRead}}
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(ts.Close)
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func call(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestToolListHonoursMCPMetadata(t *testing.T) {
	t.Parallel()
	s := session(t)
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		got[tl.Name] = tl
	}
	for _, want := range []string{"get_item", "add_note", "collide", "tree", "spend_op", "describe_operation", "call_operation"} {
		if got[want] == nil {
			t.Errorf("missing tool %q", want)
		}
	}
	for _, not := range []string{"generic_op", "hidden_op"} {
		if got[not] != nil {
			t.Errorf("%q must not be a tool", not)
		}
	}
	if !got["get_item"].Annotations.ReadOnlyHint || got["add_note"].Annotations.ReadOnlyHint {
		t.Error("readOnlyHint must follow the HTTP method")
	}
	if o := got["spend_op"].Annotations.OpenWorldHint; o == nil || !*o {
		t.Error("spend-cost ops must set openWorldHint")
	}
	for _, n := range []string{"get_item", "add_note", "describe_operation"} {
		if o := got[n].Annotations.OpenWorldHint; o == nil || *o {
			t.Errorf("%s: free ops must set openWorldHint=false explicitly", n)
		}
	}
	if !got["get_item"].Annotations.IdempotentHint || got["add_note"].Annotations.IdempotentHint {
		t.Error("idempotentHint must follow the HTTP method")
	}
	if !strings.HasPrefix(got["get_item"].Description, "S get_item\n\nD get_item") {
		t.Errorf("description %q", got["get_item"].Description)
	}
}

func TestToolsDispatchParamsAndBodies(t *testing.T) {
	t.Parallel()
	s := session(t)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"get_item", map[string]any{"id": "a/b", "idempotency_key": "k"}, "a/b|k"},
		{"add_note", map[string]any{"id": "7", "text": "hi", "tags": []string{"x", "y"}}, "7:hi:x,y"},
		{"collide", map[string]any{"id": "p", "body": map[string]any{"id": "b"}}, "p/b"},
	}
	for _, c := range cases {
		res := call(t, s, c.name, c.args)
		if res.IsError {
			t.Fatalf("%s: %s", c.name, text(res))
		}
		if got := res.StructuredContent.(map[string]any)["id"]; got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	res := call(t, s, "tree", map[string]any{"name": "r", "children": []any{map[string]any{"name": "c"}}})
	if res.IsError || res.StructuredContent.(map[string]any)["name"] != "r" {
		t.Fatalf("tree: %+v", res)
	}
}

func TestErrorsBecomeIsErrorWithProblemJSON(t *testing.T) {
	t.Parallel()
	s := session(t)
	res := call(t, s, "add_note", map[string]any{"id": "7"}) // text missing
	if !res.IsError {
		t.Fatal("expected error result")
	}
	var p struct{ Code string }
	if err := json.Unmarshal([]byte(text(res)), &p); err != nil || p.Code != "VALIDATION_FAILED" {
		t.Fatalf("text %q err %v", text(res), err)
	}
}

func TestDescribeAndCallOperation(t *testing.T) {
	t.Parallel()
	s := session(t)
	res := call(t, s, "describe_operation", map[string]any{"operation_id": "generic_op"})
	if res.IsError {
		t.Fatal(text(res))
	}
	d := res.StructuredContent.(map[string]any)
	if d["method"] != "GET" || d["path"] != "/api/generic/{id}" || d["input_schema"] == nil {
		t.Fatalf("describe %v", d)
	}
	res = call(t, s, "call_operation", map[string]any{"operation_id": "generic_op", "arguments": map[string]any{"id": "9"}})
	if res.IsError || res.StructuredContent.(map[string]any)["id"] != "g9" {
		t.Fatalf("call %+v / %s", res.StructuredContent, text(res))
	}
	// tool-class operations are reachable through the generic pair too
	res = call(t, s, "call_operation", map[string]any{"operation_id": "get_item", "arguments": map[string]any{"id": "z"}})
	if res.IsError {
		t.Fatal(text(res))
	}
}

func TestGenericRefusesExcludedAndUnknown(t *testing.T) {
	t.Parallel()
	s := session(t)
	for _, id := range []string{"hidden_op", "nope", ""} {
		for _, tool := range []string{"describe_operation", "call_operation"} {
			res := call(t, s, tool, map[string]any{"operation_id": id})
			if !res.IsError {
				t.Errorf("%s(%q) must be refused", tool, id)
			}
			if strings.Contains(text(res), "hidden_op") && strings.Contains(text(res), "internal") {
				t.Errorf("%s leaked exclusion reason: %s", tool, text(res))
			}
		}
	}
}

func TestArrayQueryParamsReachHandlerIntact(t *testing.T) {
	t.Parallel()
	s := session(t)
	for _, tool := range []string{"list_tags", "list_tags_x"} {
		args := map[string]any{"tags": []string{"a", "b", "c"}}
		res := call(t, s, tool, args)
		if res.IsError || res.StructuredContent.(map[string]any)["id"] != "a|b|c" {
			t.Errorf("%s: %+v %s", tool, res.StructuredContent, text(res))
		}
		res = call(t, s, "call_operation", map[string]any{"operation_id": tool, "arguments": args})
		if res.IsError || res.StructuredContent.(map[string]any)["id"] != "a|b|c" {
			t.Errorf("call_operation %s: %+v %s", tool, res.StructuredContent, text(res))
		}
	}
	res := call(t, s, "list_tags", map[string]any{"tags": []string{"a,b"}})
	if !res.IsError || !strings.Contains(text(res), "VALIDATION_FAILED") {
		t.Errorf("comma element in non-exploded array must be refused: %s", text(res))
	}
	res = call(t, s, "list_tags_x", map[string]any{"tags": []string{"a,b"}})
	if res.IsError || res.StructuredContent.(map[string]any)["id"] != "a,b" {
		t.Errorf("exploded array keeps commas: %s", text(res))
	}
}

func TestNullOptionalParamIsAbsent(t *testing.T) {
	t.Parallel()
	s := session(t)
	res := call(t, s, "get_item", map[string]any{"id": "n", "limit": nil, "idempotency_key": nil})
	if res.IsError || res.StructuredContent.(map[string]any)["id"] != "n|" {
		t.Fatalf("%+v %s", res.StructuredContent, text(res))
	}
	res = call(t, s, "get_item", map[string]any{"id": nil})
	if !res.IsError {
		t.Fatal("null required param must be refused")
	}
}

func TestOptionalBodyCanBeOmitted(t *testing.T) {
	t.Parallel()
	s := session(t)
	res := call(t, s, "opt_body", map[string]any{})
	if res.IsError || res.StructuredContent.(map[string]any)["id"] != "none" {
		t.Fatalf("%+v %s", res.StructuredContent, text(res))
	}
	res = call(t, s, "opt_body", map[string]any{"body": map[string]any{"text": "hey"}})
	if res.IsError || res.StructuredContent.(map[string]any)["id"] != "hey" {
		t.Fatalf("%+v %s", res.StructuredContent, text(res))
	}
}

func TestHostHeaderNotRestricted(t *testing.T) {
	t.Parallel()
	a, mux := testAPI(t)
	srv, err := NewServer(a, mux, "v0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(srv))
	defer ts.Close()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, _ := http.NewRequest("POST", ts.URL, strings.NewReader(body))
	req.Host = "mm.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestServeCapsRecorderMemory(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 64; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	req := httptest.NewRequest("GET", "/", nil)
	res := serve(h, req)
	if !res.IsError || !strings.Contains(text(res), "response too large") {
		t.Fatalf("%s", text(res))
	}
}
