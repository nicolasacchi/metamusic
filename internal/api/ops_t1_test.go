package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// t1Op is one row of the C212.1 table: seven operations, all cost free, all generic MCP operations (D-T1-21), none human-only.
type t1Op struct {
	id, method, path, ui string
	scope                auth.Scope
	create, heavy        bool
	body                 int // MaxBodyBytes; 0 => no body
	reqBody              string
}

const t1BodyCap = 300 << 10

var t1Ops = []t1Op{
	{id: "estimate_transform", method: "POST", path: "/api/transforms/estimate", scope: auth.ScopeWrite, ui: "/tracks/:id", body: t1BodyCap, reqBody: `{"kind":"glacier","glacier":{"source_id":"x"}}`},
	{id: "create_transform", method: "POST", path: "/api/transforms", scope: auth.ScopeWrite, ui: "/tracks/:id", create: true, heavy: true, body: t1BodyCap, reqBody: `{"kind":"glacier","glacier":{"source_id":"x"}}`},
	{id: "get_transform", method: "GET", path: "/api/transforms/{id}", scope: auth.ScopeRead, ui: "/transforms"},
	{id: "list_transforms", method: "GET", path: "/api/transforms", scope: auth.ScopeRead, ui: "/transforms"},
	{id: "wait_for_transform", method: "GET", path: "/api/transforms/{id}/wait", scope: auth.ScopeRead, ui: "/tracks/:id"},
	{id: "cancel_transform", method: "POST", path: "/api/transforms/{id}/cancel", scope: auth.ScopeWrite, ui: "/transforms"},
	{id: "get_transform_capabilities", method: "GET", path: "/api/transforms/capabilities", scope: auth.ScopeRead, ui: "/tracks/:id"},
}

// implementedT1 lists the operations whose stub a lane has replaced. A lane appends its ids from its own file's init()
// (ops_t1_<lane>_test.go) and never edits this file (D-T1-26).
var implementedT1 []string

func t1Rig(t *testing.T, d api.Deps) *rig {
	t.Helper()
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

func t1Call(r *rig, p auth.Principal, w t1Op) (int, map[string]any) {
	rec := r.callWith(p, w.method, strings.NewReplacer("{id}", "x").Replace(w.path), w.reqBody, idemHdr(w.create)...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func t1Agent(scopes ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: "tok-t1", Name: "t1", Scopes: scopes, Via: "token"}
}

// TestRegistry_T1Meta: the seven rows of C212.1 verbatim: scopes, costs, the MCP face (all generic, no tool, nothing excluded),
// the UI routes, the create and heavy flags, the default statuses and the body caps; none is human-only.
func TestRegistry_T1Meta(t *testing.T) {
	t.Parallel()
	if len(t1Ops) != 7 {
		t.Fatalf("the C212.1 table has %d rows, want 7", len(t1Ops))
	}
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	for _, w := range t1Ops {
		op, ok := byID[w.id]
		if !ok {
			t.Errorf("%s not registered", w.id)
			continue
		}
		m := op.Meta
		idem, rate := "", ""
		if w.create {
			idem = "create"
		}
		if w.heavy {
			rate = "heavy"
		}
		if op.Method != w.method || op.Path != w.path || m.MCP != "generic" || m.UI != w.ui || m.Scope != w.scope || m.Cost != "free" ||
			m.HumanOnly || m.Idempotency != idem || m.Rate != rate || m.AliasOf != "" || len(m.SecretFields) != 0 || m.MCPImage {
			t.Errorf("%s = %+v, want %+v", w.id, op, w)
		}
		if strings.HasPrefix(w.path, "/human/api/") {
			t.Errorf("%s is under /human/api/: no transform operation is human-only", w.id)
		}
		o := pathOperation(a, op)
		if o.MaxBodyBytes != int64(w.body) || (o.RequestBody != nil) != (w.body != 0) {
			t.Errorf("%s: MaxBodyBytes %d (body %v), want %d", w.id, o.MaxBodyBytes, o.RequestBody != nil, w.body)
		}
		if o.DefaultStatus != 0 && o.DefaultStatus != 200 {
			t.Errorf("%s: default status %d, want 200 (create answers 200 or 202 from its output Status)", w.id, o.DefaultStatus)
		}
	}
}

// TestOps_StubsAnswer501T1: until a lane lands, every handler answers 501 NOT_IMPLEMENTED naming its lane (details.task).
func TestOps_StubsAnswer501T1(t *testing.T) {
	t.Parallel()
	r := t1Rig(t, api.Deps{Transform: transform.New(transform.Options{})})
	for _, w := range t1Ops {
		if slices.Contains(implementedT1, w.id) {
			continue
		}
		code, body := t1Call(r, t1Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 501 || body["code"] != "NOT_IMPLEMENTED" || d["task"] != "T1-B4" {
			t.Errorf("%s: %d %v, want 501 NOT_IMPLEMENTED task T1-B4", w.id, code, body)
		}
	}
}

// TestOps_NilDepsUnavailableT1: without the transform service (MM_TRANSFORM_ENABLED=false, or a rig without it) every
// operation answers 503 UNAVAILABLE {component: transform}.
func TestOps_NilDepsUnavailableT1(t *testing.T) {
	t.Parallel()
	r := t1Rig(t, api.Deps{})
	for _, w := range t1Ops {
		code, body := t1Call(r, t1Agent(auth.ScopeRead, auth.ScopeWrite), w)
		d, _ := body["details"].(map[string]any)
		if code != 503 || body["code"] != "UNAVAILABLE" || d["component"] != "transform" {
			t.Errorf("%s: %d %v, want 503 UNAVAILABLE component transform", w.id, code, body)
		}
	}
}

// TestOps_AuthorizationT1: a read-only agent cannot call the write operations (estimate, create, cancel); an agent with write
// scope reaches the handlers.
func TestOps_AuthorizationT1(t *testing.T) {
	t.Parallel()
	r := t1Rig(t, api.Deps{Transform: transform.New(transform.Options{})})
	for _, w := range t1Ops {
		if w.scope != auth.ScopeRead {
			if code, _ := t1Call(r, t1Agent(auth.ScopeRead), w); code != 403 {
				t.Errorf("%s with read scope only: %d, want 403", w.id, code)
			}
		}
	}
}

// TestRequest_T1KindMembers: every one of the nine kinds is accepted in the request schema (the hi-fi kinds and their fields
// included), a wrong kind is a validation error, and `null` is accepted for an optional field ("null = the default").
func TestRequest_T1KindMembers(t *testing.T) {
	t.Parallel()
	r := t1Rig(t, api.Deps{Transform: transform.New(transform.Options{})})
	w := t1Op{id: "estimate_transform", method: "POST", path: "/api/transforms/estimate", scope: auth.ScopeWrite}
	good := map[string]string{
		"morph":        `{"kind":"morph","morph":{"track_a_id":"a","track_b_id":"b","bars":8,"a_at":{"bar":3},"b_at":{"section":"chorus#1"},"downbeat_a_s":1.5,"downbeat_b_s":"auto","key_policy":"to_a","semitones_b":null}}`,
		"shepard":      `{"kind":"shepard","shepard":{"source_id":"a","direction":"down","loop_bars":4,"layers":5,"period_s":24,"duration_s":120}}`,
		"glacier":      `{"kind":"glacier","title":null,"seed":null,"target_lufs":null,"glacier":{"source_id":"a","factor":20,"length_s":30,"window_s":0.5}}`,
		"polytempo":    `{"kind":"polytempo","polytempo":{"track_a_id":"a","track_b_id":"b","converge_bar":12,"unison_bars":8,"pan":"split","downbeat_a_s":"auto"}}`,
		"ensemble":     `{"kind":"ensemble","ensemble":{"source_id":"a","space":"cathedral","wet_db":-6,"dry_db":null,"tail_s":20}}`,
		"score_render": `{"kind":"score_render","score_render":{"score":{"abc":"X:1\nK:C\nCDEF|"},"dialect":"standard","soundfont":"orchestra","bpm":120,"tail_s":3,"gain":0.6}}`,
		"master":       `{"kind":"master","master":{"source_id":"a","preset":"warm","tilt_db":-1.5,"comp_amount":0.3,"low_cut_hz":25,"target_lufs":-16,"ceiling_dbtp":-2}}`,
		"widen":        `{"kind":"widen","widen":{"source_id":"a","width":1.3,"decorrelate":0.1,"bass_mono_hz":120}}`,
		"binaural":     `{"kind":"binaural","binaural":{"source_id":"a","model":"hrtf","preset":"studio","left_az_deg":-30,"right_az_deg":30,"elevation_deg":0,"room":"studio","room_mix_db":-24,"tail_s":1}}`,
	}
	if len(good) != 9 {
		t.Fatal("nine kinds")
	}
	for kind, body := range good {
		rec := r.callWith(t1Agent(auth.ScopeRead, auth.ScopeWrite), w.method, w.path, body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if rec.Code != 501 { // past validation, into the stub
			t.Errorf("%s: %d %v, want the request to validate and reach the 501 stub", kind, rec.Code, m)
		}
	}
	for name, body := range map[string]string{
		"unknown kind":   `{"kind":"reverb"}`,
		"out of range":   `{"kind":"widen","widen":{"source_id":"a","width":2.5}}`,
		"bad enum":       `{"kind":"master","master":{"source_id":"a","preset":"loud"}}`,
		"bad downbeat":   `{"kind":"morph","morph":{"track_a_id":"a","track_b_id":"b","downbeat_a_s":"maybe"}}`,
		"azimuth":        `{"kind":"binaural","binaural":{"source_id":"a","left_az_deg":-5}}`,
		"missing source": `{"kind":"glacier","glacier":{}}`,
	} {
		rec := r.callWith(t1Agent(auth.ScopeRead, auth.ScopeWrite), w.method, w.path, body)
		if rec.Code != 422 && rec.Code != 400 {
			t.Errorf("%s: %d %s, want a validation error", name, rec.Code, rec.Body.String())
		}
	}
}
