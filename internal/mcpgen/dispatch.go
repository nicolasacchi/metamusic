package mcpgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxBody = 8 << 20

// problemResult builds an IsError result carrying a problem+json body.
func problemResult(code, msg string) *mcp.CallToolResult {
	b, _ := json.Marshal(map[string]any{"code": code, "message": msg, "details": map[string]any{}})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// scalar renders a JSON value as a query/path/header string.
func scalar(raw json.RawMessage) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case bool:
		return fmt.Sprint(t), nil
	case nil:
		return "", fmt.Errorf("null is not allowed")
	}
	return "", fmt.Errorf("value must be a string, number or boolean")
}

// buildRequest turns tool arguments into an in-process HTTP request.
func buildRequest(ctx context.Context, s *toolSpec, rawArgs json.RawMessage) (*http.Request, *mcp.CallToolResult) {
	args := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(rawArgs)) > 0 && !bytes.Equal(bytes.TrimSpace(rawArgs), []byte("null")) {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, problemResult("VALIDATION_FAILED", "arguments must be a JSON object")
		}
	}
	path := s.Path
	q := url.Values{}
	hdr := http.Header{}
	used := map[string]bool{}
	for _, p := range s.Params {
		raw, ok := args[p.Arg]
		if !ok {
			if p.Required {
				return nil, problemResult("VALIDATION_FAILED", "missing required argument "+p.Arg)
			}
			continue
		}
		if p.In != "path" && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && !p.Required {
			used[p.Arg] = true // null means absent for optional parameters
			continue
		}
		used[p.Arg] = true
		var list []json.RawMessage
		if p.In != "path" && json.Unmarshal(raw, &list) == nil && len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '[' {
			if p.In == "header" {
				return nil, problemResult("VALIDATION_FAILED", "argument "+p.Arg+" must be a scalar")
			}
			var vals []string
			for _, e := range list {
				v, err := scalar(e)
				if err != nil {
					return nil, problemResult("VALIDATION_FAILED", "argument "+p.Arg+": "+err.Error())
				}
				if !p.Explode && strings.Contains(v, ",") {
					return nil, problemResult("VALIDATION_FAILED", "argument "+p.Arg+": elements must not contain commas")
				}
				vals = append(vals, v)
			}
			if p.Explode {
				for _, v := range vals {
					q.Add(p.Wire, v)
				}
			} else if len(vals) > 0 {
				q.Set(p.Wire, strings.Join(vals, ","))
			}
			continue
		}
		v, err := scalar(raw)
		if err != nil {
			return nil, problemResult("VALIDATION_FAILED", "argument "+p.Arg+": "+err.Error())
		}
		switch p.In {
		case "path":
			if v == "" || v == "." || v == ".." {
				return nil, problemResult("VALIDATION_FAILED", "argument "+p.Arg+": invalid path value")
			}
			path = strings.ReplaceAll(path, "{"+p.Wire+"}", url.PathEscape(v))
		case "query":
			q.Set(p.Wire, v)
		case "header":
			hdr.Set(p.Wire, v)
		}
	}

	var body io.Reader
	switch s.Body {
	case bodyNested:
		used["body"] = true
		if raw, ok := args["body"]; ok {
			body = bytes.NewReader(raw)
		}
	case bodyFlat:
		obj := map[string]json.RawMessage{}
		for _, n := range s.BodyProps {
			if raw, ok := args[n]; ok {
				obj[n] = raw
				used[n] = true
			}
		}
		b, _ := json.Marshal(obj) // always an object so Huma reports field-level errors
		body = bytes.NewReader(b)
	}
	for n := range args {
		if !used[n] {
			return nil, problemResult("VALIDATION_FAILED", "unknown argument "+n)
		}
	}

	// path holds escaped segments; keep both forms so %2F survives.
	plain, err := url.PathUnescape(path)
	if err != nil {
		return nil, problemResult("VALIDATION_FAILED", "invalid path argument")
	}
	u := url.URL{Path: plain, RawPath: path, RawQuery: q.Encode()}
	req, err := http.NewRequestWithContext(ctx, s.Method, u.String(), body)
	if err != nil {
		return nil, problemResult("INTERNAL", "could not build request")
	}
	req.Header = hdr
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "@mcp"
	return req, nil
}

// serve runs the request on inner and converts the response to a tool result.
func serve(inner http.Handler, req *http.Request) *mcp.CallToolResult {
	rec := &capRecorder{ResponseRecorder: httptest.NewRecorder()}
	inner.ServeHTTP(rec, req)
	raw := bytes.TrimSpace(rec.Body.Bytes())
	if rec.overflow || len(raw) > maxBody {
		return problemResult("INTERNAL", "response too large")
	}
	res := &mcp.CallToolResult{}
	if len(raw) == 0 {
		raw = []byte("{}")
		if rec.Code < 200 || rec.Code >= 300 {
			return problemResult("INTERNAL", fmt.Sprintf("upstream status %d", rec.Code))
		}
	}
	var v any
	isJSON := json.Unmarshal(raw, &v) == nil
	if rec.Code < 200 || rec.Code >= 300 {
		if m, ok := v.(map[string]any); ok && isJSON && rec.Code >= 400 && m["code"] != nil {
			res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
			res.IsError = true
			return res
		}
		return problemResult("INTERNAL", fmt.Sprintf("upstream status %d", rec.Code))
	}
	res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	if m, ok := v.(map[string]any); ok && isJSON {
		res.StructuredContent = m
	}
	return res
}

// dispatch executes spec with args on inner, using ctx (which carries the
// already-resolved principal).
func dispatch(ctx context.Context, inner http.Handler, s *toolSpec, args json.RawMessage) *mcp.CallToolResult {
	req, bad := buildRequest(ctx, s, args)
	if bad != nil {
		return bad
	}
	return serve(inner, req)
}

// capRecorder stops buffering once the body exceeds maxBody, so a runaway
// handler cannot exhaust memory. Writes past the cap fail, which also lets
// well-behaved handlers stop early.
type capRecorder struct {
	*httptest.ResponseRecorder
	overflow bool
}

func (c *capRecorder) Write(b []byte) (int, error) {
	if c.overflow || c.Body.Len()+len(b) > maxBody {
		c.overflow = true
		return 0, errors.New("response too large")
	}
	return c.ResponseRecorder.Write(b)
}

func (c *capRecorder) WriteString(s string) (int, error) { return c.Write([]byte(s)) }
