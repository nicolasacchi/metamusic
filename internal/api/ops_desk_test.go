package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// deskScoreText is the score of the fixture take Luna (the fixture records its hash but writes no file).
const deskScoreText = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

var (
	deskEditor   = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Scopes: auth.ScopesFor(auth.RoleEditor), Via: "proxy"}
	deskListener = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", User: "carol", Scopes: auth.ScopesFor(auth.RoleListener), Via: "proxy"}
	deskAgent    = auth.Principal{Kind: auth.KindAgent, ID: "tok-desk", Name: "desk-agent", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
)

type deskRig struct {
	*rig
	dir string
	svc *desk.Service
}

func newDeskRig(t *testing.T) *deskRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureV2(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "takes", lt.TrackLuna), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "takes", lt.TrackLuna, "score.abc"), []byte(deskScoreText), 0o444); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return lt.FixedNow }
	signer := serve.NewSigner(testKey, now)
	svc := desk.New(desk.Options{Store: st, Data: root, Signer: signer, Now: now})
	r := &rig{mux: http.NewServeMux(), signer: signer, svc: library.New(st, root, now), st: st}
	api.New(r.mux, api.Deps{Store: st, Library: r.svc, Signer: signer, Now: now, Desk: svc})
	return &deskRig{rig: r, dir: dir, svc: svc}
}

// do calls the operation as p; create operations carry an Idempotency-Key.
func (r *deskRig) do(p auth.Principal, method, path string, body any) (int, map[string]any) {
	raw := ""
	if body != nil {
		b, _ := json.Marshal(body)
		raw = string(b)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(raw)).WithContext(auth.WithPrincipal(context.Background(), p))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == "POST" {
		req.Header.Set("Idempotency-Key", "k-"+path+"-"+raw)
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (r *deskRig) want(t *testing.T, p auth.Principal, method, path string, body any, status int, code string) map[string]any {
	t.Helper()
	got, m := r.do(p, method, path, body)
	if got != status || (code != "" && m["code"] != code) {
		t.Fatalf("%s %s: %d %v, want %d %s", method, path, got, m, status, code)
	}
	return m
}

func deskEdit(text string) map[string]any {
	return map[string]any{"rationale": "brighter", "ops": []map[string]any{{"op": "replace_bars", "voice": "1", "bar_from": 1, "bar_to": 1, "abc": text}}}
}

// TestOps_Desk_REST: every Desk operation, its happy path and each error code it can answer.
func TestOps_Desk_REST(t *testing.T) {
	t.Parallel()
	r := newDeskRig(t)
	// open the take's score: 201 first, 200 for the same plan afterwards
	plan := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")
	id := plan["id"].(string)
	if plan["source"] != "take" || plan["track_id"] != lt.TrackLuna || plan["bars"] != float64(6) || plan["abc"] != deskScoreText {
		t.Errorf("plan = %v", plan)
	}
	if by := obj(t, plan["created_by"]); by["id"] != "human:alice" || by["kind"] != "human" {
		t.Errorf("created_by = %v", by)
	}
	if again := r.want(t, deskAgent, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 200, ""); again["id"] != id || obj(t, again["created_by"])["id"] != "human" {
		t.Errorf("repeat = %v", again) // an agent sees a person only as "human"
	}
	for _, c := range []struct {
		track  string
		status int
		code   string
		reason string
	}{
		{"nope", 404, "NOT_FOUND", ""},
		{lt.TrackCitta, 422, "PLAN_SOURCE_UNSUPPORTED", "no_score"},
	} {
		m := r.want(t, human, "POST", "/api/tracks/"+c.track+"/plan", nil, c.status, c.code)
		if c.reason != "" && obj(t, m["details"])["reason"] != c.reason {
			t.Errorf("%s: details %v", c.track, m["details"])
		}
	}
	// read it back, with and without parts
	got := r.want(t, deskEditor, "GET", "/api/plans/"+id, nil, 200, "")
	if got["abc"] != deskScoreText || got["renders"] == nil || got["auditions"] == nil || got["bar_spans"] != nil {
		t.Errorf("get_plan = %v", got)
	}
	got = r.want(t, human, "GET", "/api/plans/"+id+"?include=bars", nil, 200, "")
	if got["abc"] != nil || got["bar_spans"] == nil {
		t.Errorf("include=bars = %v", got)
	}
	r.want(t, human, "GET", "/api/plans/"+id+"?include=loud", nil, 422, "VALIDATION_FAILED")
	r.want(t, human, "GET", "/api/plans/nope", nil, 404, "NOT_FOUND")
	// the score check
	rep := r.want(t, human, "POST", "/api/abc/check", map[string]any{"abc": deskScoreText}, 200, "")
	if rep["ok"] != true || rep["bars"] != float64(6) || rep["dialect"] != "yue2" {
		t.Errorf("check_abc = %v", rep)
	}
	if rep = r.want(t, human, "POST", "/api/abc/check", map[string]any{"abc": "X:1\nK:C\nCD&EF|\n"}, 200, ""); rep["ok"] != false {
		t.Errorf("a broken score checked ok: %v", rep)
	}
	r.want(t, human, "POST", "/api/abc/check", map[string]any{"abc": "x", "dialect": "abc9"}, 422, "")
	// edit it
	child := r.want(t, deskEditor, "POST", "/api/plans/"+id+"/edits", deskEdit("CDEFGABc"), 201, "")
	if child["source"] != "edit" || child["parent_plan_id"] != id || obj(t, child["edit"])["rationale"] != "brighter" {
		t.Errorf("edit_plan = %v", child)
	}
	if same := r.want(t, human, "POST", "/api/plans/"+id+"/edits", deskEdit("CDEFGABc"), 200, ""); same["id"] != child["id"] {
		t.Errorf("same result = %v", same["id"])
	}
	r.want(t, human, "POST", "/api/plans/"+id+"/edits", deskEdit("CDEF"), 422, "PLAN_NO_CHANGE")
	m := r.want(t, human, "POST", "/api/plans/"+id+"/edits", deskEdit("CD"), 422, "PLAN_EDIT_INVALID")
	if v, _ := obj(t, m["details"])["violations"].([]any); len(v) == 0 || obj(t, v[0])["code"] != "bar_length" {
		t.Errorf("violations = %v", m["details"])
	}
	m = r.want(t, human, "POST", "/api/plans/"+id+"/edits", map[string]any{"ops": []map[string]any{{"op": "replace_bars", "voice": "9", "bar_from": 1, "bar_to": 1, "abc": "CDEFGABc"}}}, 422, "PLAN_EDIT_INVALID")
	if obj(t, m["details"])["rule"] != "bad_voice" {
		t.Errorf("details = %v", m["details"])
	}
	r.want(t, human, "POST", "/api/plans/"+id+"/edits", map[string]any{"ops": []any{}}, 422, "")
	r.want(t, human, "POST", "/api/plans/nope/edits", deskEdit("CDEFGABc"), 404, "NOT_FOUND")
	// verdicts and the list
	v := r.want(t, human, "PUT", "/api/plans/"+child["id"].(string)+"/verdict", map[string]any{"verdict": "liked", "note": "lifts"}, 200, "")
	if vd := obj(t, v["verdicts"]); obj(t, vd["owner"])["verdict"] != "liked" || vd["critic"] != nil || v["verdict"] != "liked" {
		t.Errorf("verdict = %v", v["verdicts"])
	}
	r.want(t, human, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "meh"}, 422, "")
	r.want(t, human, "PUT", "/api/plans/nope/verdict", map[string]any{"verdict": nil}, 404, "NOT_FOUND")
	list := r.want(t, deskEditor, "GET", "/api/plans?source=edit&verdict=liked", nil, 200, "")
	if items, _ := list["items"].([]any); len(items) != 1 || obj(t, items[0])["id"] != child["id"] {
		t.Errorf("list = %v", list)
	}
	list = r.want(t, human, "GET", "/api/plans", nil, 200, "")
	if items, _ := list["items"].([]any); len(items) != 2 {
		t.Errorf("list all = %v", list)
	}
	r.want(t, human, "GET", "/api/plans?source=nope", nil, 422, "")
	r.want(t, human, "GET", "/api/plans?limit=101", nil, 422, "")
}

// TestOps_Desk_ListenerRefused: a Listener reaches no Desk operation (the U1 default of 2026-11-21), nor a Listener's device.
func TestOps_Desk_ListenerRefused(t *testing.T) {
	t.Parallel()
	r := newDeskRig(t)
	plan := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")
	id := plan["id"].(string)
	device := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleListener, ID: "d1", Name: "phone", Scopes: auth.ScopesFor(auth.RoleListener), Via: "device"}
	for _, p := range []auth.Principal{deskListener, device} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/plans", nil}, {"GET", "/api/plans/" + id, nil}, {"POST", "/api/abc/check", map[string]any{"abc": "x"}},
			{"POST", "/api/tracks/" + lt.TrackLuna + "/plan", nil}, {"POST", "/api/plans/" + id + "/edits", deskEdit("CDEFGABc")},
			{"PUT", "/api/plans/" + id + "/verdict", map[string]any{"verdict": "liked"}},
		} {
			if code, m := r.do(p, c.method, c.path, c.body); code != 403 {
				t.Errorf("%s %s %s: %d %v, want 403", p.Kind, c.method, c.path, code, m)
			}
		}
	}
	if n := r.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "1" {
		t.Errorf("%s plans after refused calls", n)
	}
}

// TestOps_Desk_EditorVerdictOwnerOnly: an Editor plans and edits but a verdict is the Owner's (403 OWNER_ONLY); the Owner's own
// device may write it.
func TestOps_Desk_EditorVerdictOwnerOnly(t *testing.T) {
	t.Parallel()
	r := newDeskRig(t)
	id := r.want(t, deskEditor, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")["id"].(string)
	r.want(t, deskEditor, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "liked"}, 403, "OWNER_ONLY")
	editorDevice := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleEditor, ID: "d2", Name: "tablet", Scopes: auth.ScopesFor(auth.RoleEditor), Via: "device"}
	r.want(t, editorDevice, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "liked"}, 403, "OWNER_ONLY")
	if n := r.scalar(`SELECT COUNT(*) FROM desk_plan_verdicts`); n != "0" {
		t.Errorf("%s verdict rows after refusals", n)
	}
	ownerDevice := auth.Principal{Kind: auth.KindDevice, Role: auth.RoleOwner, ID: "d3", Name: "phone", Scopes: auth.ScopesFor(auth.RoleOwner), Via: "device"}
	r.want(t, ownerDevice, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "rejected"}, 200, "")
	// an Editor's own plan is visible to the Owner
	if by := obj(t, r.want(t, human, "GET", "/api/plans/"+id, nil, 200, "")["created_by"]); by["id"] != "human:bob" {
		t.Errorf("created_by seen by the Owner = %v", by)
	}
}

// TestOps_Desk_AgentVerdictIsCritic: an agent's verdict is the critic's row; the Owner's row is untouched.
func TestOps_Desk_AgentVerdictIsCritic(t *testing.T) {
	t.Parallel()
	r := newDeskRig(t)
	id := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")["id"].(string)
	r.want(t, human, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "liked"}, 200, "")
	got := r.want(t, deskAgent, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "rejected", "note": "thin"}, 200, "")
	vd := obj(t, got["verdicts"])
	if obj(t, vd["owner"])["verdict"] != "liked" || obj(t, vd["critic"])["verdict"] != "rejected" || obj(t, obj(t, vd["critic"])["by"])["id"] != "agent:tok-desk" {
		t.Errorf("verdicts = %v", vd)
	}
	if got["verdict"] != "liked" || got["critic_verdict"] != "rejected" {
		t.Errorf("summary = %v %v", got["verdict"], got["critic_verdict"])
	}
	if n := r.scalar(`SELECT COUNT(*) FROM desk_plan_verdicts WHERE rater = 'critic' AND decided_by = 'agent:tok-desk'`); n != "1" {
		t.Errorf("critic rows = %s", n)
	}
	// a read-only agent writes nothing
	r.want(t, readOnly, "PUT", "/api/plans/"+id+"/verdict", map[string]any{"verdict": "liked"}, 403, "FORBIDDEN_SCOPE")
	r.want(t, readOnly, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 403, "FORBIDDEN_SCOPE")
	r.want(t, readOnly, "GET", "/api/plans/"+id, nil, 200, "")
}

// TestOps_Desk_NilServiceUnavailable: without a Desk every operation answers 503 UNAVAILABLE {component: desk}.
func TestOps_Desk_NilServiceUnavailable(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	api.New(mux, api.Deps{})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/plans", ""}, {"GET", "/api/plans/x", ""}, {"POST", "/api/abc/check", `{"abc":"x"}`},
		{"PUT", "/api/plans/x/verdict", `{"verdict":null}`}, {"POST", "/api/tracks/x/plan", ""},
		{"POST", "/api/plans/x/edits", `{"ops":[{"op":"set_tempo","bpm":90}]}`},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)).WithContext(auth.WithPrincipal(context.Background(), human))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		if w.Code != 503 || m["code"] != "UNAVAILABLE" || obj(t, m["details"])["component"] != "desk" {
			t.Errorf("%s %s: %d %v, want 503 UNAVAILABLE desk", c.method, c.path, w.Code, m)
		}
	}
}

func (r *deskRig) scalar(q string) string {
	var s string
	_ = r.st.R.QueryRow(q).Scan(&s)
	return s
}
