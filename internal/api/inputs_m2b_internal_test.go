package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// probeAPI registers the real M2b input types behind probe handlers that record what they decoded.
func probeAPI(t *testing.T) (mux *http.ServeMux, clipIn **getClipIn, duelIn **createDuelIn, spec **spectrogramIn) {
	t.Helper()
	mux = http.NewServeMux()
	a := humago.New(mux, HumaConfig())
	var ci *getClipIn
	var di *createDuelIn
	var si *spectrogramIn
	meta := Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:test"}
	type out struct{ Body struct{ OK bool } }
	Register(a, huma.Operation{OperationID: "probe_clip", Method: "GET", Path: "/api/probe/{id}/clip", Summary: "s", Description: "d"}, meta,
		func(_ context.Context, in *getClipIn) (*out, error) { ci = in; return &out{}, nil })
	wm := meta
	wm.Scope, wm.Idempotency = auth.ScopeWrite, "create"
	Register(a, created(huma.Operation{OperationID: "probe_duel", Method: "POST", Path: "/api/probe/duels", Summary: "s", Description: "d"}), wm,
		func(_ context.Context, in *createDuelIn) (*out, error) { di = in; return &out{}, nil })
	Register(a, huma.Operation{OperationID: "probe_spec", Method: "GET", Path: "/api/probe/{id}/spec", Summary: "s", Description: "d"}, meta,
		func(_ context.Context, in *spectrogramIn) (*out, error) { si = in; return &out{}, nil })
	return mux, &ci, &di, &si
}

func probeCall(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).
		WithContext(auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestOptParam_Float(t *testing.T) {
	t.Parallel()
	mux, ci, _, _ := probeAPI(t)
	if w := probeCall(mux, "GET", "/api/probe/t1/clip", ""); w.Code != 200 {
		t.Fatalf("absent: %d %s", w.Code, w.Body)
	}
	in := *ci
	if in.FromS.ptr() != nil || in.ToS.ptr() != nil || in.MatchLUFS.ptr() != nil || in.FromS.IsSet {
		t.Errorf("absent parameters must be unset: %+v", in)
	}
	if w := probeCall(mux, "GET", "/api/probe/t1/clip?from_s=12.5&match_lufs=-14", ""); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	in = *ci
	if p := in.FromS.ptr(); p == nil || *p != 12.5 || !in.FromS.IsSet {
		t.Errorf("from_s = %v, want 12.5", in.FromS.ptr())
	}
	if p := in.MatchLUFS.ptr(); p == nil || *p != -14 {
		t.Errorf("match_lufs = %v, want -14", in.MatchLUFS.ptr())
	}
	if in.ToS.ptr() != nil {
		t.Error("to_s was not sent")
	}
	// get_clip's match_lufs is validated by the schema: outside [-40, -6] is 422
	for _, v := range []string{"-5", "-41", "0"} {
		if w := probeCall(mux, "GET", "/api/probe/t1/clip?match_lufs="+v, ""); w.Code != 422 {
			t.Errorf("match_lufs=%s: %d, want 422", v, w.Code)
		}
	}
	if w := probeCall(mux, "GET", "/api/probe/t1/clip?from_s=-1", ""); w.Code != 422 {
		t.Errorf("from_s=-1: %d, want 422", w.Code)
	}
}

// Huma's setDefaults would overwrite an explicit false on a defaulted bool;
// blind and match_loudness are *bool without a default so false survives (C53).
func TestCreateDuel_ExplicitFalseHonoured(t *testing.T) {
	t.Parallel()
	mux, _, di, _ := probeAPI(t)
	tests := []struct {
		body         string
		blind, match *bool
	}{
		{`{"a_track_id":"a","b_track_id":"b"}`, nil, nil},
		{`{"a_track_id":"a","b_track_id":"b","blind":false,"match_loudness":false}`, ptrTo(false), ptrTo(false)},
		{`{"a_track_id":"a","b_track_id":"b","blind":true,"match_loudness":false}`, ptrTo(true), ptrTo(false)},
	}
	for _, tc := range tests {
		if w := probeCall(mux, "POST", "/api/probe/duels", tc.body); w.Code != 201 {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		got := *di
		if !eqBool(got.Body.Blind, tc.blind) || !eqBool(got.Body.MatchLoudness, tc.match) {
			t.Errorf("%s: blind=%v match=%v, want %v %v", tc.body, deref(got.Body.Blind), deref(got.Body.MatchLoudness), deref(tc.blind), deref(tc.match))
		}
	}
	// an unknown field such as note is refused (D-M2b-19)
	if w := probeCall(mux, "POST", "/api/probe/duels", `{"a_track_id":"a","b_track_id":"b","note":"x"}`); w.Code != 422 {
		t.Errorf("note: %d, want 422", w.Code)
	}
}

// inline defaults to true only when the parameter is absent.
func TestGetTrackSpectrogram_InlineQuery(t *testing.T) {
	t.Parallel()
	mux, _, _, si := probeAPI(t)
	for q, want := range map[string]bool{"": true, "?inline=true": true, "?inline=false": false} {
		if w := probeCall(mux, "GET", "/api/probe/t1/spec"+q, ""); w.Code != 200 {
			t.Fatalf("%q: %d %s", q, w.Code, w.Body)
		}
		if got := (*si).Inline; got != want {
			t.Errorf("inline%s = %v, want %v", q, got, want)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

func eqBool(a, b *bool) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
