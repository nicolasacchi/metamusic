package mcpgen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResourceSpec is one MCP resource template: a presentation of a GET registry operation whose only path parameter
// is "id" (C112.1). Blob marks an op whose response carries image.data_base64 (mcp_image).
type ResourceSpec struct {
	URITemplate, Name, Title, Description, MIMEType, Op string
	Blob                                                bool
}

// Resources are the resource templates every MCP server (HTTP and stdio) serves. resources/list stays empty
// (D-M4a-20): the templates are the discovery surface.
var Resources = []ResourceSpec{
	{URITemplate: "metamusic://tracks/{id}/context", Name: "track_context", Title: "Track context bundle",
		Description: "Everything known about a take for a critique: loudness, sections, flags, comments, listening. Same as the get_track_context tool.",
		MIMEType:    "application/json", Op: "get_track_context"},
	{URITemplate: "metamusic://tracks/{id}/spectrogram", Name: "track_spectrogram", Title: "Track spectrogram",
		Description: "Spectrogram image of a take. Same as the get_track_spectrogram tool.",
		MIMEType:    "image/jpeg", Op: "get_track_spectrogram", Blob: true},
}

// uuidRE is the only id shape a resource URI accepts: a 36-char lowercase UUID.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validateResources checks every spec against the callable operations: the op exists (and so is not excluded
// from MCP), is a GET, and its only path parameter is "id". The URI template must have the single {id} variable.
func validateResources(callable map[string]entry, specs []ResourceSpec) error {
	for _, rs := range specs {
		e, ok := callable[rs.Op]
		if !ok {
			return fmt.Errorf("mcpgen: resource %s: operation %q does not exist or is excluded from MCP", rs.Name, rs.Op)
		}
		if e.spec.Method != http.MethodGet {
			return fmt.Errorf("mcpgen: resource %s: operation %s is not GET", rs.Name, rs.Op)
		}
		var paths []string
		for _, p := range e.spec.Params {
			if p.In == "path" {
				paths = append(paths, p.Arg)
			}
		}
		if len(paths) != 1 || paths[0] != "id" {
			return fmt.Errorf("mcpgen: resource %s: operation %s must have exactly the path parameter id, has %v", rs.Name, rs.Op, paths)
		}
		if strings.Count(rs.URITemplate, "{") != 1 || !strings.Contains(rs.URITemplate, "{id}") {
			return fmt.Errorf("mcpgen: resource %s: URI template %q must use only {id}", rs.Name, rs.URITemplate)
		}
	}
	return nil
}

// idFromURI extracts the id of uri against the template (exact match, UUID id), else "".
func idFromURI(template, uri string) string {
	pre, post, _ := strings.Cut(template, "{id}")
	if !strings.HasPrefix(uri, pre) || !strings.HasSuffix(uri, post) || len(uri) < len(pre)+len(post) {
		return ""
	}
	id := uri[len(pre) : len(uri)-len(post)]
	if !uuidRE.MatchString(id) {
		return ""
	}
	return id
}

func (g *gen) addResources(srv *mcp.Server, specs []ResourceSpec) error {
	if err := validateResources(g.callable, specs); err != nil {
		return err
	}
	for _, rs := range specs {
		srv.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: rs.URITemplate, Name: rs.Name, Title: rs.Title, Description: rs.Description, MIMEType: rs.MIMEType,
		}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return g.read(ctx, req, rs)
		})
	}
	return nil
}

// rawServe runs an in-process request and returns the status and capped body.
func rawServe(inner http.Handler, req *http.Request) (int, []byte, error) {
	rec := &capRecorder{ResponseRecorder: httptest.NewRecorder()}
	inner.ServeHTTP(rec, req)
	if rec.overflow || rec.Body.Len() > maxBody {
		return 0, nil, errors.New("response too large")
	}
	return rec.Code, rec.Body.Bytes(), nil
}

// problemError is the JSON-RPC error of a refused presentation: the message is the problem code, never the body.
func problemError(status int, body []byte) error {
	var p struct {
		Code string `json:"code"`
	}
	code := "INTERNAL"
	if json.Unmarshal(body, &p) == nil && p.Code != "" {
		code = p.Code
	}
	var rpc int64 = jsonrpc.CodeInternalError
	if status >= 400 && status < 500 {
		rpc = jsonrpc.CodeInvalidParams
	}
	return &jsonrpc.Error{Code: rpc, Message: code}
}

// present dispatches operation op with args through the gated handler as the session's caller. A non-nil error
// is the JSON-RPC error to answer with.
func (g *gen) present(ctx context.Context, extra *mcp.RequestExtra, session *mcp.ServerSession, op string, args map[string]any) (int, []byte, error) {
	e, ok := g.callable[op]
	if !ok {
		return 0, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	ctx, code := g.resolve(ctx)
	if code != "" {
		return 0, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: code}
	}
	ctx = withOrigin(ctx, origin{transport: g.transport, client: sessionLabel(extra, session)})
	raw, _ := json.Marshal(args)
	hreq, bad := buildRequest(ctx, e.spec, raw)
	if bad != nil {
		return 0, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	status, body, err := rawServe(g.inner, hreq)
	if err != nil {
		return 0, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	if status < 200 || status >= 300 {
		return status, body, problemError(status, body)
	}
	return status, body, nil
}

func (g *gen) read(ctx context.Context, req *mcp.ReadResourceRequest, rs ResourceSpec) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	id := idFromURI(rs.URITemplate, uri)
	if id == "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	status, body, err := g.present(ctx, req.Extra, req.Session, rs.Op, map[string]any{"id": id})
	if err != nil {
		if status == http.StatusNotFound {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	if !rs.Blob {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: rs.MIMEType, Text: string(body)}}}, nil
	}
	var v struct {
		Image struct {
			MIMEType string `json:"mime_type"`
			Data     string `json:"data_base64"`
		} `json:"image"`
	}
	if json.Unmarshal(body, &v) != nil || v.Image.Data == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	data, derr := base64.StdEncoding.DecodeString(v.Image.Data)
	if derr != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	if len(data) > maxImageBytes {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "IMAGE_TOO_LARGE"}
	}
	mime := v.Image.MIMEType
	if mime == "" {
		mime = rs.MIMEType
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Blob: data}}}, nil
}
