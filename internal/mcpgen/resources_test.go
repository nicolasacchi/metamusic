package mcpgen_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// a2Rig is a full rig (fixture library, M2b ears fixture, files) plus the faces A2 compares.
func a2Rig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, M4a: true,
		Backend: "fake", M2b: servertest.M2bOptions{Fixture: true}})
}

var readScopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}

// gatedFace is the in-process REST face of a principal: the gated handler the MCP servers dispatch into, so a
// comparison is exact even where the rig's HTTP server lacks a dependency.
type gatedFace struct {
	h http.Handler
	p auth.Principal
}

func newGatedFace(t *testing.T, r *servertest.Rig, scopes ...auth.Scope) (gatedFace, string) {
	t.Helper()
	tok, _, err := r.Services.Tokens.Create(context.Background(), "a2-"+t.Name(), scopes)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Services.Tokens.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	_, h := r.Services.Gated()
	return gatedFace{h, p}, tok
}

func (f gatedFace) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(auth.WithPrincipal(context.Background(), f.p))
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// resourceSessions returns an HTTP (PAT) and a stdio session of one rig, authenticated as the same kind of agent.
func resourceSessions(t *testing.T, r *servertest.Rig, scopes ...auth.Scope) map[string]*mcp.ClientSession {
	t.Helper()
	return map[string]*mcp.ClientSession{"http": r.MCP(t, scopes...), "stdio": r.Stdio(t, scopes...)}
}

func readResource(t *testing.T, s *mcp.ClientSession, uri string) (*mcp.ReadResourceResult, error) {
	t.Helper()
	return s.ReadResource(callCtx(), &mcp.ReadResourceParams{URI: uri})
}

func TestResources_RealRegistry(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	for face, s := range resourceSessions(t, r, readScopes...) {
		res, err := s.ListResourceTemplates(callCtx(), nil)
		if err != nil {
			t.Fatalf("%s: %v", face, err)
		}
		if len(res.ResourceTemplates) != len(mcpgen.Resources) {
			t.Fatalf("%s: %d templates, want %d", face, len(res.ResourceTemplates), len(mcpgen.Resources))
		}
		for i, rs := range mcpgen.Resources {
			got := res.ResourceTemplates[i]
			if got.URITemplate != rs.URITemplate || got.MIMEType != rs.MIMEType || got.Name != rs.Name ||
				!strings.Contains(got.Description, "tool") {
				t.Errorf("%s: template %d = %+v", face, i, got)
			}
		}
	}
}

func TestResources_ReadEqualsTool(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	face, _ := newGatedFace(t, r, readScopes...)
	wantCtxStatus, wantCtx := face.get(t, "/api/tracks/"+lt.TrackLuna+"/context")
	wantSpecStatus, wantSpec := face.get(t, "/api/tracks/"+lt.TrackLuna+"/spectrogram")
	if wantCtxStatus != 200 || wantSpecStatus != 200 {
		t.Fatalf("REST: context %d, spectrogram %d: %s", wantCtxStatus, wantSpecStatus, wantSpec)
	}
	var spec struct {
		Image struct {
			MIMEType string `json:"mime_type"`
			Data     string `json:"data_base64"`
		} `json:"image"`
	}
	if err := json.Unmarshal(wantSpec, &spec); err != nil || spec.Image.Data == "" {
		t.Fatalf("spectrogram body: %v", err)
	}
	wantJPEG, err := base64.StdEncoding.DecodeString(spec.Image.Data)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range resourceSessions(t, r, readScopes...) {
		t.Run(name, func(t *testing.T) {
			res, err := readResource(t, s, "metamusic://tracks/"+lt.TrackLuna+"/context")
			if err != nil || len(res.Contents) != 1 {
				t.Fatalf("context: %v %+v", err, res)
			}
			c := res.Contents[0]
			if c.MIMEType != "application/json" || c.URI != "metamusic://tracks/"+lt.TrackLuna+"/context" || c.Blob != nil {
				t.Errorf("context content %+v", c)
			}
			if diff := cmp.Diff(string(wantCtx), c.Text); diff != "" {
				t.Errorf("context text differs from get_track_context (-rest +resource):\n%s", diff)
			}
			res, err = readResource(t, s, "metamusic://tracks/"+lt.TrackLuna+"/spectrogram")
			if err != nil || len(res.Contents) != 1 {
				t.Fatalf("spectrogram: %v %+v", err, res)
			}
			c = res.Contents[0]
			if c.MIMEType != "image/jpeg" || c.Text != "" || string(c.Blob) != string(wantJPEG) || len(wantJPEG) == 0 {
				t.Errorf("spectrogram content mime %q, %d bytes (want %d)", c.MIMEType, len(c.Blob), len(wantJPEG))
			}
		})
	}
}

func TestResources_ListEmpty(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	for name, s := range resourceSessions(t, r, readScopes...) {
		before := auditCount(t, r)
		res, err := s.ListResources(callCtx(), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(res.Resources) != 0 {
			t.Errorf("%s: resources/list = %+v, want empty", name, res.Resources)
		}
		if after := auditCount(t, r); after != before {
			t.Errorf("%s: resources/list wrote %d audit rows", name, after-before)
		}
	}
}

func auditCount(t *testing.T, r *servertest.Rig) int {
	t.Helper()
	n, cursor := 0, ""
	for {
		page, err := r.Services.Audit.List(context.Background(), audit.Filter{Limit: 200, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		n += len(page.Items)
		if page.NextCursor == "" {
			return n
		}
		cursor = page.NextCursor
	}
}

func rpcCode(t *testing.T, err error) (int64, string) {
	t.Helper()
	var je *jsonrpc.Error
	if !errors.As(err, &je) {
		t.Fatalf("not a JSON-RPC error: %v", err)
	}
	return je.Code, je.Message
}

func TestResources_UnknownIDNotFound(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	for name, s := range resourceSessions(t, r, readScopes...) {
		for _, uri := range []string{
			"metamusic://tracks/01900000-0000-7000-8000-00000000ffff/context", // well-formed, no such track
			"metamusic://tracks/01900000-0000-7000-8000-00000000ffff/spectrogram",
			"metamusic://tracks/not-a-uuid/context",
			"metamusic://tracks/" + strings.ToUpper("0190000a-0000-7000-8000-00000000010b") + "/context",
			"metamusic://tracks/" + lt.TrackLuna + "/context/extra",
			"metamusic://tracks/" + lt.TrackLuna,
			"metamusic://other/" + lt.TrackLuna + "/context",
		} {
			res, err := readResource(t, s, uri)
			if err == nil {
				t.Errorf("%s %s: read succeeded: %+v", name, uri, res)
				continue
			}
			if code, msg := rpcCode(t, err); code != jsonrpc.CodeInvalidParams || msg != "Resource not found" {
				t.Errorf("%s %s: code %d %q, want resource-not-found (%v)", name, uri, code, msg, err)
			}
		}
	}
}

func TestResources_ScopeRefusal(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	for name, s := range resourceSessions(t, r, auth.ScopeWrite) { // a token without read
		for _, uri := range []string{"metamusic://tracks/" + lt.TrackLuna + "/context", "metamusic://tracks/" + lt.TrackLuna + "/spectrogram"} {
			res, err := readResource(t, s, uri)
			if err == nil {
				t.Errorf("%s %s: read succeeded: %+v", name, uri, res)
				continue
			}
			if _, msg := rpcCode(t, err); msg != "FORBIDDEN_SCOPE" {
				t.Errorf("%s %s: error %q, want message FORBIDDEN_SCOPE", name, uri, msg)
			}
		}
	}
}

// TestResources_ReadIsAudited: a read is a gated call like any tool (D-M4a-20), with the transport of its face.
func TestResources_ReadIsAudited(t *testing.T) {
	t.Parallel()
	r := a2Rig(t)
	ss := resourceSessions(t, r, readScopes...)
	for name, s := range ss {
		before := auditCount(t, r)
		if _, err := readResource(t, s, "metamusic://tracks/"+lt.TrackLuna+"/context"); err != nil {
			t.Fatal(err)
		}
		if after := auditCount(t, r); after != before+1 {
			t.Errorf("%s: a read wrote %d audit rows, want 1", name, after-before)
		}
	}
	page, err := r.Services.Audit.List(context.Background(), audit.Filter{Op: "get_track_context", Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("audit rows %v %v", page.Items, err)
	}
	seen := map[string]bool{}
	for _, row := range page.Items {
		seen[row.Transport] = true
	}
	if !seen["mcp"] || !seen["stdio"] {
		t.Errorf("transports %v, want mcp and stdio", seen)
	}
}
