package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type echoIn struct {
	Body struct {
		N int `json:"n" minimum:"1"`
	}
}

func TestProblemShape(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	a := humago.New(mux, cfg)
	api.Register(a, huma.Operation{OperationID: "echo", Method: "POST", Path: "/api/echo", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, in *echoIn) (*outT, error) {
			if in.Body.N == 7 {
				return nil, api.Err(409, "CONFLICT", "seven", map[string]any{"n": 7})
			}
			return &outT{}, nil
		})
	p := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeWrite}}
	do := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/echo", strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), p))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	type prob struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"validation", `{"n":0}`, "VALIDATION_FAILED", 422},
		{"custom", `{"n":7}`, "CONFLICT", 409},
		// malformed JSON is deliberately BAD_REQUEST (400), distinct from schema failures (422).
		{"malformed", `{`, "BAD_REQUEST", 400},
	} {
		w := do(tc.body)
		if w.Code != tc.status {
			t.Fatalf("%s: status %d %s", tc.name, w.Code, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: content type %q", tc.name, ct)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 3 {
			t.Errorf("%s: keys %v", tc.name, raw)
		}
		var p prob
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		if p.Code != tc.code || p.Message == "" || p.Details == nil {
			t.Errorf("%s: %+v", tc.name, p)
		}
		if tc.code == "VALIDATION_FAILED" {
			if errs, ok := p.Details["errors"].([]any); !ok || len(errs) == 0 {
				t.Errorf("details.errors missing: %v", p.Details)
			}
		}
	}
}

func TestInnerMuxProblems(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Version: "x"})
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/api/nope", 404, "NOT_FOUND"},
		{"GET", "/human/api/nope", 404, "NOT_FOUND"},
		{"POST", "/api/ping", 405, "METHOD_NOT_ALLOWED"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || w.Header().Get("Content-Type") != "application/problem+json" ||
			!strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("%s %s: %d %q %s", tc.method, tc.path, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
}

func TestProblem5xxLogRedacted(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	// The logger is injected through the request context: the test never touches the process-global default,
	// so parallel tests that log a 5xx cannot write into (or race on) this buffer.
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	canary := "mmp_" + strings.Repeat("a2b3", 13)

	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	a := humago.New(mux, cfg)
	api.Register(a, huma.Operation{OperationID: "boom", Method: "GET", Path: "/api/boom", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test"},
		func(context.Context, *nothing) (*outT, error) {
			// a plain error is what real handlers return for an upstream failure; huma turns it into a 500 through
			// NewErrorWithContext, which logs through the logger carried by the request context
			return nil, errors.New("upstream said no for " + canary)
		})
	p := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeRead}}
	r := httptest.NewRequest("GET", "/api/boom", nil).WithContext(api.WithLogger(auth.WithPrincipal(context.Background(), p), logger))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	logged := buf.String()
	if strings.Contains(logged, canary) || !strings.Contains(logged, "mmp_<redacted>") {
		t.Fatalf("log not redacted: %s", logged)
	}
	if strings.Contains(w.Body.String(), canary) {
		t.Fatalf("response leaks: %s", w.Body)
	}
}

func TestDeps_NewIDFunc(t *testing.T) {
	t.Parallel()
	if id := (api.Deps{}).NewIDFunc()(); len(id) != 36 || id[14] != '7' {
		t.Fatalf("default id %q is not a UUIDv7", id)
	}
	if got := (api.Deps{NewID: func() string { return "fixed" }}).NewIDFunc()(); got != "fixed" {
		t.Fatalf("injected id = %q", got)
	}
}

func TestProblemCanceledRequestLogsInfoNotError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	a := humago.New(mux, cfg)
	api.Register(a, huma.Operation{OperationID: "gone", Method: "GET", Path: "/api/gone", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test"},
		func(context.Context, *nothing) (*outT, error) {
			return nil, fmt.Errorf("track refs: %w", context.Canceled)
		})
	p := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeRead}}
	r := httptest.NewRequest("GET", "/api/gone", nil).WithContext(api.WithLogger(auth.WithPrincipal(context.Background(), p), logger))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("response contract changed: status %d", w.Code)
	}
	if got := buf.String(); strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, `"level":"INFO"`) {
		t.Fatalf("a client-canceled request must log at INFO, got %s", got)
	}
}
