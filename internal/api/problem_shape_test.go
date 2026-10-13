package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestProblem_ShapesAgree(t *testing.T) {
	t.Parallel()
	details := map[string]any{"n": float64(7), "s": "x"}
	mux := http.NewServeMux()
	a := humago.New(mux, api.HumaConfig())
	api.Register(a, huma.Operation{OperationID: "shape", Method: "POST", Path: "/api/shape", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, in *echoIn) (*outT, error) {
			return nil, api.Err(409, "CONFLICT", "seven", details)
		})
	p := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeWrite}}
	r := httptest.NewRequest("POST", "/api/shape", strings.NewReader(`{"n":1}`)).WithContext(auth.WithPrincipal(context.Background(), p))
	r.Header.Set("Content-Type", "application/json")
	hw := httptest.NewRecorder()
	mux.ServeHTTP(hw, r)

	pw := httptest.NewRecorder()
	problem.Write(pw, 409, "CONFLICT", "seven", details)

	if hw.Code != pw.Code {
		t.Fatalf("status %d vs %d", hw.Code, pw.Code)
	}
	if a, b := hw.Header().Get("Content-Type"), pw.Header().Get("Content-Type"); a != b {
		t.Fatalf("content type %q vs %q", a, b)
	}
	var hb, pb map[string]any
	if err := json.Unmarshal(hw.Body.Bytes(), &hb); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pw.Body.Bytes(), &pb); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(hb, pb); diff != "" {
		t.Fatal(diff)
	}
}
