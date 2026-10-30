package mcpgen_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
)

type picIn struct {
	Mode string `path:"mode"`
}
type picBody struct {
	Name  string         `json:"name"`
	Image *api.ImageData `json:"image,omitempty"`
}
type picOut struct{ Body picBody }

// imageAPI registers get_pic (mcp_image) whose mode picks the payload, and get_plain (no mcp_image).
func imageAPI(t *testing.T) (huma.API, *http.ServeMux, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "0.0.1")
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath, cfg.CreateHooks = "/api/openapi", "", "", nil
	a := humago.New(mux, cfg)
	img := bytes.Repeat([]byte{0xff, 0xd8, 0x42}, 1000) // 3000 bytes
	meta := func(image bool) api.Meta {
		return api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test", MCPImage: image}
	}
	op := func(id, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: "GET", Path: path, Summary: "S " + id, Description: "D " + id}
	}
	handler := func(_ context.Context, in *picIn) (*picOut, error) {
		out := &picOut{}
		out.Body.Name = in.Mode
		switch in.Mode {
		case "ok":
			out.Body.Image = &api.ImageData{MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(img)}
		case "garbage":
			out.Body.Image = &api.ImageData{MIMEType: "image/jpeg", DataBase64: "!!not base64!!"}
		case "big":
			out.Body.Image = &api.ImageData{MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(make([]byte, 100001))}
		case "limit":
			out.Body.Image = &api.ImageData{MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(make([]byte, 100000))}
		case "link":
			out.Body.Image = &api.ImageData{MIMEType: "image/jpeg"} // inline=false: a link only
		}
		return out, nil
	}
	api.Register(a, op("get_pic", "/api/pic/{mode}"), meta(true), handler)
	api.Register(a, op("get_plain", "/api/plain/{mode}"), meta(false), handler)
	return a, mux, img
}

type faces struct {
	name string
	sess *mcp.ClientSession
}

func imageFaces(t *testing.T) ([]faces, []byte, *http.ServeMux) {
	t.Helper()
	a, mux, img := imageAPI(t)
	agent := auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "tester", Via: "token", Scopes: []auth.Scope{auth.ScopeRead}}

	srv, err := mcpgen.NewServer(a, mux, "v0")
	if err != nil {
		t.Fatal(err)
	}
	h := mcpgen.Handler(srv)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), agent)))
	}))
	t.Cleanup(ts.Close)
	httpSess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpSess.Close() })

	ssrv, err := mcpgen.NewStdioServer(a, mux, mcpgen.StdioOptions{Principal: func(context.Context) (auth.Principal, error) { return agent, nil }, Version: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := ssrv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	stdioSess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdioSess.Close() })
	return []faces{{"http", httpSess}, {"stdio", stdioSess}}, img, mux
}

func errCode(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("want a tool error, got %+v", res)
	}
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &p); err != nil {
		t.Fatal(err)
	}
	return p.Code
}

func TestMCP_ImageContent(t *testing.T) {
	t.Parallel()
	fs, img, _ := imageFaces(t)
	for _, f := range fs {
		for _, route := range []string{"direct", "call_operation"} {
			call := func(mode string) *mcp.CallToolResult {
				name, args := "get_pic", map[string]any{"mode": mode}
				if route == "call_operation" {
					name, args = "call_operation", map[string]any{"operation_id": "get_pic", "arguments": args}
				}
				return callTool(t, f.sess, name, args)
			}
			id := f.name + "/" + route

			res := call("ok")
			if res.IsError || len(res.Content) != 2 {
				t.Fatalf("%s: ok = %+v", id, res)
			}
			ic, ok := res.Content[0].(*mcp.ImageContent)
			if !ok || ic.MIMEType != "image/jpeg" || !bytes.Equal(ic.Data, img) {
				t.Errorf("%s: first content = %#v, want the image bytes", id, res.Content[0])
			}
			tc, ok := res.Content[1].(*mcp.TextContent)
			if !ok {
				t.Fatalf("%s: second content %T", id, res.Content[1])
			}
			if strings.Contains(tc.Text, "data_base64") {
				t.Errorf("%s: text repeats the base64: %.80s", id, tc.Text)
			}
			sc, _ := json.Marshal(res.StructuredContent)
			if strings.Contains(string(sc), "data_base64") {
				t.Errorf("%s: structured content has data_base64", id)
			}
			var text any
			_ = json.Unmarshal([]byte(tc.Text), &text)
			var structured any
			_ = json.Unmarshal(sc, &structured)
			if d := cmp.Diff(structured, text); d != "" {
				t.Errorf("%s: text and structured content differ:\n%s", id, d)
			}
			want := map[string]any{"name": "ok", "image": map[string]any{"mime_type": "image/jpeg"}}
			if d := cmp.Diff(want, structured); d != "" {
				t.Errorf("%s: structured content (-want +got):\n%s", id, d)
			}

			if c := errCode(t, call("garbage")); c != "INTERNAL" {
				t.Errorf("%s: garbage base64 => %s, want INTERNAL", id, c)
			}
			if c := errCode(t, call("big")); c != "IMAGE_TOO_LARGE" {
				t.Errorf("%s: 100001 bytes => %s, want IMAGE_TOO_LARGE", id, c)
			}
			if r := call("limit"); r.IsError {
				t.Errorf("%s: exactly 100000 bytes must pass: %+v", id, r.Content)
			}
			// without an image (inline=false or no image at all) the normal result applies
			for _, mode := range []string{"none", "link"} {
				r := call(mode)
				if r.IsError || len(r.Content) != 1 {
					t.Errorf("%s: %s => %+v, want one text content", id, mode, r)
					continue
				}
				if _, ok := r.Content[0].(*mcp.TextContent); !ok {
					t.Errorf("%s: %s content %T", id, mode, r.Content[0])
				}
			}
		}
		// an operation without mcp_image keeps base64 inline in the text
		r := callTool(t, f.sess, "get_plain", map[string]any{"mode": "ok"})
		if len(r.Content) != 1 || !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "data_base64") {
			t.Errorf("%s: plain op must not be converted: %+v", f.name, r.Content)
		}
	}
}

func TestDescribeOperation_ImageResult(t *testing.T) {
	t.Parallel()
	fs, _, _ := imageFaces(t)
	for _, id := range []string{"get_pic", "get_plain"} {
		res := callTool(t, fs[0].sess, "describe_operation", map[string]any{"operation_id": id})
		m, _ := res.StructuredContent.(map[string]any)
		if got := m["image_result"] == true; got != (id == "get_pic") {
			t.Errorf("%s: image_result = %v", id, m["image_result"])
		}
	}
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(callCtx(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// restJSON calls the fake API directly, as REST would.
func restJSON(t *testing.T, mux *http.ServeMux, path string) any {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil).WithContext(auth.WithPrincipal(context.Background(),
		auth.Principal{Kind: auth.KindAgent, ID: "t1", Scopes: []auth.Scope{auth.ScopeRead}}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var v any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	return v
}

// TestParity_ImageOps: the parity rules for image operations hold on the HTTP and stdio faces.
func TestParity_ImageOps(t *testing.T) {
	t.Parallel()
	fs, _, mux := imageFaces(t)
	for _, mode := range []string{"ok", "link", "none"} {
		rest := restJSON(t, mux, "/api/pic/"+mode)
		for _, f := range fs {
			res := callTool(t, f.sess, "get_pic", map[string]any{"mode": mode})
			checkImageParity(t, f.name+"/"+mode, rest, res)
			if d := cmp.Diff(withoutImageData(rest), normalizeJSON(res.StructuredContent)); d != "" {
				t.Errorf("%s/%s: REST minus data_base64 vs structured content:\n%s", f.name, mode, d)
			}
		}
	}
}

func normalizeJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// withoutImageData drops image.data_base64 from a REST body.
func withoutImageData(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, x := range m {
		out[k] = x
	}
	if img, ok := m["image"].(map[string]any); ok {
		c := make(map[string]any, len(img))
		for k, x := range img {
			if k != "data_base64" {
				c[k] = x
			}
		}
		out["image"] = c
	}
	return out
}

// checkImageParity applies the C52 rules to one MCP result of an image operation: ImageContent.Data equals the
// decoded REST base64, the text content equals the structured content, and the result stays within 140 000 bytes.
func checkImageParity(t *testing.T, id string, rest any, res *mcp.CallToolResult) {
	t.Helper()
	if res.IsError {
		t.Errorf("%s: tool error %+v", id, res.Content)
		return
	}
	var b64 string
	if m, ok := rest.(map[string]any); ok {
		if img, ok := m["image"].(map[string]any); ok {
			b64, _ = img["data_base64"].(string)
		}
	}
	structured := normalizeJSON(res.StructuredContent)
	if b64 == "" {
		if len(res.Content) != 1 {
			t.Errorf("%s: no image => one text content, got %d", id, len(res.Content))
		}
	} else {
		want, err := base64.StdEncoding.DecodeString(b64)
		ic, ok := res.Content[0].(*mcp.ImageContent)
		if err != nil || !ok || !bytes.Equal(ic.Data, want) {
			t.Errorf("%s: ImageContent does not carry the REST image bytes (%v)", id, err)
		}
	}
	tc, ok := res.Content[len(res.Content)-1].(*mcp.TextContent)
	if !ok {
		t.Errorf("%s: last content %T, want text", id, res.Content[len(res.Content)-1])
		return
	}
	var text any
	if err := json.Unmarshal([]byte(tc.Text), &text); err != nil {
		t.Errorf("%s: text is not JSON: %v", id, err)
	} else if b64 != "" {
		if d := cmp.Diff(structured, text); d != "" {
			t.Errorf("%s: text content differs from structured content:\n%s", id, d)
		}
	}
	if raw, _ := json.Marshal(res); len(raw) > 140000 {
		t.Errorf("%s: serialized result is %d bytes, over 140000", id, len(raw))
	}
}
