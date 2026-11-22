package api_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// nightSnap counts the rows a read must never change.
func nightSnap(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(`SELECT (SELECT COUNT(*) FROM night_shift_runs) || '|' || (SELECT COUNT(*) FROM batches) || '|' || (SELECT COUNT(*) FROM ledger) || '|' ||
		(SELECT COUNT(*) FROM estimates) || '|' || (SELECT COUNT(*) FROM audit_log WHERE op NOT LIKE 'get_%' AND op NOT LIKE 'preview_%') || '|' ||
		(SELECT enabled || COALESCE(halted_at, '') || at || updated_by FROM night_shift_settings)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNightShiftOps_REST walks the four operations over a real rig: the shift starts off, reads are Owner-side only, enabling is the
// Owner's act in the app and needs a live mandate, nobody else (agent, phone, Editor, Listener) can change it, and halting is open to
// the Owner's phone and to a write agent but not to a read-only one.
func TestNightShiftOps_REST(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	bob, carol := r.User(t, "bob", "editor"), r.User(t, "carol", "listener")
	reader, _ := r.Agent(t, auth.ScopeRead)
	writer, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	dev := r.Device(t)

	// off by default, no mandate
	got := obj(t, owner.OK(t, "GET", "/api/night-shift", nil))
	st := obj(t, got["settings"])
	au := obj(t, got["authority"])
	if st["enabled"] != false || st["at"] != "01:30" || st["max_takes"] != float64(8) || st["listening_checks"] != "on" || st["halted"] != nil ||
		au["mandate"] != nil || au["reason"] != "no_mandate" || got["next_run_at"] != nil || len(arr(got["runs"])) != 0 {
		t.Fatalf("fresh: %v", got)
	}
	for _, c := range []*servertest.Client{reader, writer, admin, dev} {
		c.OK(t, "GET", "/api/night-shift", nil)
		c.OK(t, "GET", "/api/night-shift/preview", nil)
	}
	for _, c := range []*servertest.Client{bob, carol} {
		status(t, c.Do(t, "GET", "/api/night-shift", nil), 403, "OWNER_ONLY")
		status(t, c.Do(t, "GET", "/api/night-shift/preview", nil), 403, "OWNER_ONLY")
		status(t, c.Do(t, "POST", "/api/night-shift/halt", map[string]any{"reason": "x"}), 403, "OWNER_ONLY")
	}

	// nobody but the Owner in the app changes the settings
	on := map[string]any{"enabled": true}
	for _, c := range []struct {
		who  *servertest.Client
		want int
		code string
	}{{admin, 403, "HUMAN_ONLY"}, {writer, 403, "HUMAN_ONLY"}, {dev, 401, "UNAUTHENTICATED"}, {bob, 403, "OWNER_ONLY"}, {carol, 403, "OWNER_ONLY"}} {
		status(t, c.who.Do(t, "PUT", "/human/api/night-shift", on), c.want, c.code)
	}
	// enabling needs a live mandate; other edits do not
	status(t, owner.Do(t, "PUT", "/human/api/night-shift", on), 409, "MANDATE_REQUIRED")
	ed := obj(t, owner.OK(t, "PUT", "/human/api/night-shift", map[string]any{"at": "02:15", "max_takes": 5, "explore_ppm": 200000, "min_stars": 3, "listening_checks": "off"}))
	if ed["enabled"] != false || ed["at"] != "02:15" || ed["max_takes"] != float64(5) || ed["explore_ppm"] != float64(200000) || ed["listening_checks"] != "off" ||
		obj(t, ed["updated_by"])["id"] != "human:alice" {
		t.Fatalf("edit: %v", ed)
	}
	for field, bad := range map[string]any{"at": "25:00", "max_takes": 13, "explore_ppm": 500001, "min_stars": 0} {
		resp := owner.Do(t, "PUT", "/human/api/night-shift", map[string]any{field: bad})
		if resp.Status != 422 {
			t.Errorf("%s=%v: %d %s", field, bad, resp.Status, resp.Body)
		}
	}
	status(t, owner.Do(t, "PUT", "/human/api/night-shift", map[string]any{"surprise": 1}), 422, "")
	status(t, owner.Do(t, "PUT", "/human/api/night-shift", map[string]any{"max_micro_usd": 5}), 422, "") // no money field exists

	// a live mandate lets the Owner enable
	mid := obj(t, owner.OK(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-night"))["id"].(string)
	en := obj(t, owner.OK(t, "PUT", "/human/api/night-shift", on))
	if en["enabled"] != true {
		t.Fatalf("enable: %v", en)
	}
	got = obj(t, owner.OK(t, "GET", "/api/night-shift", nil))
	au = obj(t, got["authority"])
	if obj(t, au["mandate"])["id"] != mid || au["reason"] != nil || got["next_run_at"] == nil {
		t.Fatalf("enabled: %v", got)
	}

	// halting: a read-only agent may not, a write agent and the phone may; idempotent; later enabling clears it
	status(t, reader.Do(t, "POST", "/api/night-shift/halt", map[string]any{"reason": "x"}), 403, "FORBIDDEN_SCOPE")
	status(t, writer.Do(t, "POST", "/api/night-shift/halt", map[string]any{"reason": ""}), 422, "")
	h := obj(t, writer.OK(t, "POST", "/api/night-shift/halt", map[string]any{"reason": "too loud"}))
	halted := obj(t, h["halted"])
	if halted["reason"] != "too loud" || obj(t, halted["by"])["kind"] != "agent" || h["enabled"] != true {
		t.Fatalf("halt: %v", h)
	}
	h2 := obj(t, dev.OK(t, "POST", "/api/night-shift/halt", map[string]any{"reason": "again"}))
	if obj(t, h2["halted"])["reason"] != "too loud" {
		t.Fatalf("second halt replaced the first: %v", h2)
	}
	got = obj(t, owner.OK(t, "GET", "/api/night-shift", nil))
	if got["next_run_at"] != nil || obj(t, got["settings"])["halted"] == nil {
		t.Fatalf("halted: %v", got)
	}
	pv := obj(t, owner.OK(t, "GET", "/api/night-shift/preview", nil))
	if pv["would_skip"] != "halted" {
		t.Fatalf("halted preview: %v", pv)
	}
	if cleared := obj(t, owner.OK(t, "PUT", "/human/api/night-shift", on)); cleared["halted"] != nil {
		t.Fatalf("enabling kept the halt: %v", cleared)
	}
}

// TestNightShiftOps_Preview: with a live mandate the preview plans over the fixture library, prices it under the mandate's per-night
// ceiling, and writes nothing.
func TestNightShiftOps_Preview(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	owner.OK(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-prev")
	before := nightSnap(t, r)
	pv := obj(t, owner.OK(t, "GET", "/api/night-shift/preview", nil))
	if nightSnap(t, r) != before {
		t.Fatal("a preview wrote")
	}
	m := obj(t, pv["mandate"])
	if pv["night"] == "" || pv["seed"] == "" || m["covered"] != true || len(arr(m["reasons"])) != 0 {
		t.Fatalf("preview: %v", pv)
	}
	items := arr(pv["items"])
	q := obj(t, pv["quote"])
	if len(items) != 2 || int(q["takes"].(float64)) != 2 || q["ceiling_micro_usd"].(float64) > 943_587 || pv["would_skip"] != "disabled" || pv["night"] != "2026-10-07" {
		t.Fatalf("preview: %v", pv)
	}
}

// TestUpdateNightShift_HumanOnly: update_night_shift is not an agent tool: its meta is human-only and Owner-only, it has no MCP face, and
// the call_operation route does not know it.
func TestUpdateNightShift_HumanOnly(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	u := byID["update_night_shift"]
	if !u.Meta.HumanOnly || !u.Meta.OwnerOnly || u.Meta.MCP != "exclude:human-only" || u.Path != "/human/api/night-shift" || u.Meta.Cost != "free" {
		t.Errorf("update_night_shift meta %+v", u)
	}
	h := byID["halt_night_shift"]
	if h.Meta.HumanOnly || !h.Meta.OwnerOnly || h.Meta.Scope != auth.ScopeWrite || h.Path != "/api/night-shift/halt" {
		t.Errorf("halt_night_shift meta %+v", h)
	}
	for _, id := range []string{"get_night_shift", "preview_night_shift"} {
		if m := byID[id].Meta; m.Scope != auth.ScopeRead || m.Cost != "free" || !m.OwnerOnly || byID[id].Method != "GET" {
			t.Errorf("%s meta %+v", id, m)
		}
	}
}

func TestOps_BodyLimitsM6NNight(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range []struct{ method, path string }{{"PUT", "/human/api/night-shift"}, {"POST", "/api/night-shift/halt"}} {
		if o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path}); o.MaxBodyBytes != 64<<10 {
			t.Errorf("%s %s: MaxBodyBytes = %d, want 65536", w.method, w.path, o.MaxBodyBytes)
		}
	}
}
