package api_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func mandateRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake"})
}

func mandateBody(until time.Duration) map[string]any {
	return map[string]any{"holder": "night_shift", "title": "Night shift", "budget_micro_usd": 1_000_000,
		"valid_until": lt.FixedNow.Add(until).Format(time.RFC3339),
		"scope": map[string]any{"modes": []string{"new_seed", "style_edit"}, "engines": []string{"yue2"}, "min_stars": 3, "root_track_ids": nil,
			"max_takes_per_lineage": 3, "max_takes_per_batch": 6, "max_batch_ceiling_micro_usd": 943_587, "max_batches_per_day": 1, "tier_b": true}}
}

func status(t *testing.T, r *servertest.Response, want int, code string) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status %d, want %d: %s", r.Status, want, r.Body)
	}
	if code != "" && obj(t, r.JSON(t))["code"] != code {
		t.Fatalf("code %v, want %s: %s", obj(t, r.JSON(t))["code"], code, r.Body)
	}
}

// TestMandateOps_REST walks the four operations: the Owner creates (201, replayable), nobody else can, every Owner-side caller reads and
// revokes, an Editor and a Listener see nothing, and the audit row of the create carries the budget as its cost.
func TestMandateOps_REST(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	bob, carol := r.User(t, "bob", "editor"), r.User(t, "carol", "listener")
	reader, _ := r.Agent(t, auth.ScopeRead)
	writer, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	dev := r.Device(t)

	// create: only the Owner in the app
	for _, c := range []struct {
		who  *servertest.Client
		want int
		code string
	}{{admin, 403, "HUMAN_ONLY"}, {dev, 401, "UNAUTHENTICATED"}, // devices never reach /human/
		{bob, 403, "OWNER_ONLY"}, {carol, 403, "OWNER_ONLY"}} {
		status(t, c.who.Do(t, "POST", "/human/api/mandates", mandateBody(7*24*time.Hour), "Idempotency-Key", "k-no"), c.want, c.code)
	}
	status(t, owner.Do(t, "POST", "/human/api/mandates", mandateBody(7*24*time.Hour)), 428, "IDEMPOTENCY_KEY_REQUIRED")
	first := owner.Do(t, "POST", "/human/api/mandates", mandateBody(7*24*time.Hour), "Idempotency-Key", "k-1")
	status(t, first, 201, "")
	m := obj(t, first.JSON(t))
	id := m["id"].(string)
	usage := obj(t, m["usage"])
	if m["holder"] != "night_shift" || m["effective_state"] != "live" || m["budget_micro_usd"] != float64(1_000_000) || m["budget_usd"] != "1" ||
		m["revoked"] != nil || usage["remaining_micro_usd"] != float64(1_000_000) || usage["committed_micro_usd"] != float64(0) ||
		obj(t, m["scope"])["root_track_ids"] != nil || obj(t, m["created_by"])["id"] != "human:alice" {
		t.Fatalf("created: %v", m)
	}
	replay := owner.Do(t, "POST", "/human/api/mandates", mandateBody(7*24*time.Hour), "Idempotency-Key", "k-1")
	status(t, replay, 201, "")
	if obj(t, replay.JSON(t))["id"] != id {
		t.Fatal("a replay made another mandate")
	}
	var cost int64
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'create_mandate' AND status = 201 AND cost_micro_usd = 1000000`).Scan(&cost); err != nil || cost != 1 {
		t.Fatalf("%d audit rows carry the budget as cost (%v), want exactly the create", cost, err)
	}
	status(t, owner.Do(t, "POST", "/human/api/mandates", mandateBody(7*24*time.Hour), "Idempotency-Key", "k-2"), 409, "MANDATE_ACTIVE")

	// reads: the Owner, the Owner's phone and agents; never an Editor or a Listener
	for _, c := range []*servertest.Client{owner, reader, writer, admin, dev} {
		pg := obj(t, c.OK(t, "GET", "/api/mandates?effective_state=live", nil))
		if items := arr(pg["items"]); len(items) != 1 || obj(t, items[0])["id"] != id {
			t.Fatalf("list: %v", pg)
		}
		if got := obj(t, c.OK(t, "GET", "/api/mandates/"+id, nil)); got["id"] != id {
			t.Fatalf("get: %v", got)
		}
	}
	for _, c := range []*servertest.Client{bob, carol} {
		status(t, c.Do(t, "GET", "/api/mandates", nil), 403, "OWNER_ONLY")
		status(t, c.Do(t, "GET", "/api/mandates/"+id, nil), 403, "OWNER_ONLY")
		status(t, c.Do(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "x"}), 403, "OWNER_ONLY")
	}
	status(t, owner.Do(t, "GET", "/api/mandates/nope", nil), 404, "NOT_FOUND")
	status(t, owner.Do(t, "GET", "/api/mandates?limit=101", nil), 422, "")

	// revoke: a read-only agent may not, a write agent may, and only once
	status(t, reader.Do(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "x"}), 403, "FORBIDDEN_SCOPE")
	status(t, writer.Do(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": ""}), 422, "")
	rv := writer.Do(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "spending too fast"})
	status(t, rv, 200, "")
	done := obj(t, rv.JSON(t))
	if done["effective_state"] != "revoked" || obj(t, done["revoked"])["reason"] != "spending too fast" {
		t.Fatalf("revoked: %v", done)
	}
	status(t, owner.Do(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "again"}), 409, "INVALID_TRANSITION")
	status(t, owner.Do(t, "POST", "/api/mandates/nope/revoke", map[string]any{"reason": "x"}), 404, "NOT_FOUND")
	if pg := obj(t, owner.OK(t, "GET", "/api/mandates?effective_state=revoked", nil)); len(arr(pg["items"])) != 1 {
		t.Fatalf("revoked list: %v", pg)
	}
	// a revoked mandate frees the holder: a new one is allowed
	status(t, owner.Do(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-3"), 201, "")
}

// TestMandateOps_OwnersPhoneRevokes: the Owner's paired phone cannot create (devices never reach /human/) but can stop spend.
func TestMandateOps_OwnersPhoneRevokes(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner, dev := r.Human(t), r.Device(t)
	id := obj(t, owner.OK(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-1"))["id"].(string)
	rv := obj(t, dev.OK(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "from the phone"}))
	if by := obj(t, obj(t, rv["revoked"])["by"]); by["kind"] != "device" {
		t.Fatalf("revoked by %v", by)
	}
}

func TestMandateOps_ScopeRefusals(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	for i, c := range []struct {
		mut  func(map[string]any)
		rule string
	}{
		{func(s map[string]any) { s["engines"] = []string{"acestep15"} }, "engine_not_adopted"},
		{func(s map[string]any) { s["modes"] = []string{"lyric_edit"} }, "mode_unknown"},
		{func(s map[string]any) { s["min_stars"] = 9 }, "min_stars_range"},
		{func(s map[string]any) { s["root_track_ids"] = []string{"no-such-track"} }, "root_not_found"},
		{func(s map[string]any) { s["max_batch_ceiling_micro_usd"] = 1_000_001 }, "ceiling_above_budget"},
		{func(s map[string]any) { s["max_batches_per_day"] = 5 }, "max_batches_per_day_range"},
	} {
		b := mandateBody(24 * time.Hour)
		c.mut(b["scope"].(map[string]any))
		resp := owner.Do(t, "POST", "/human/api/mandates", b, "Idempotency-Key", "k-"+string(rune('a'+i)))
		status(t, resp, 422, "MANDATE_SCOPE_INVALID")
		if d := obj(t, obj(t, resp.JSON(t))["details"]); d["rule"] != c.rule {
			t.Errorf("rule %v, want %s", d["rule"], c.rule)
		}
	}
	// an unknown scope key is refused by the schema, never stored
	b := mandateBody(24 * time.Hour)
	b["scope"].(map[string]any)["surprise"] = 1
	if resp := owner.Do(t, "POST", "/human/api/mandates", b, "Idempotency-Key", "k-x"); resp.Status != 422 {
		t.Errorf("unknown scope key: %d %s", resp.Status, resp.Body)
	}
	// a mandate never raises a cap: the batch cap is read from spend_caps, and a ceiling above it is refused
	b = mandateBody(24 * time.Hour)
	b["budget_micro_usd"] = 100_000_000
	b["scope"].(map[string]any)["max_batch_ceiling_micro_usd"] = 6_000_000 // the default batch cap is 5 000 000
	resp := owner.Do(t, "POST", "/human/api/mandates", b, "Idempotency-Key", "k-cap")
	status(t, resp, 422, "MANDATE_SCOPE_INVALID")
	if obj(t, obj(t, resp.JSON(t))["details"])["rule"] != "ceiling_above_batch_cap" {
		t.Errorf("%s", resp.Body)
	}
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM mandates`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d mandates stored after only refusals (%v)", n, err)
	}
}

func TestMandateOps_GETNeverWrites(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	id := obj(t, owner.OK(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-1"))["id"].(string)
	snap := func() string {
		var s string
		if err := r.Store.R.QueryRow(`SELECT (SELECT COUNT(*) FROM mandates) || '|' || (SELECT COUNT(*) FROM batches) || '|' || (SELECT COUNT(*) FROM ledger) || '|' ||
			(SELECT COUNT(*) FROM estimates) || '|' || (SELECT state FROM mandates)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snap()
	owner.OK(t, "GET", "/api/mandates", nil)
	owner.OK(t, "GET", "/api/mandates/"+id, nil)
	if after := snap(); after != before {
		t.Fatalf("a GET wrote: %s -> %s", before, after)
	}
}

func TestOps_BodyLimitsM6NMandates(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	for _, w := range []struct{ method, path string }{{"POST", "/human/api/mandates"}, {"POST", "/api/mandates/{id}/revoke"}} {
		if o := pathOperation(a, api.OpInfo{Method: w.method, Path: w.path}); o.MaxBodyBytes != 64<<10 {
			t.Errorf("%s %s: MaxBodyBytes = %d, want 65536", w.method, w.path, o.MaxBodyBytes)
		}
	}
}

func TestMandateOps_NilServiceUnavailable(t *testing.T) {
	t.Parallel()
	r := m3Rig(t, api.Deps{})
	for _, w := range []struct{ method, path string }{{"GET", "/api/mandates"}, {"GET", "/api/mandates/x"}} {
		res := m3Call(r, m3Op{id: w.path, method: w.method, path: w.path})
		if d, _ := res.body["details"].(map[string]any); res.code != 503 || d["component"] != "mandates" {
			t.Errorf("%s: %d %v", w.path, res.code, res.body)
		}
	}
}

// TestRevokeMandate_CancelsInSameTx: revoking over REST records a cancel intent, by system:mandate-revoke, on every unfinished job of
// the mandate's batches, in the same transaction as the state change, and touches no other batch.
func TestRevokeMandate_CancelsInSameTx(t *testing.T) {
	t.Parallel()
	r := mandateRig(t)
	owner := r.Human(t)
	id := obj(t, owner.OK(t, "POST", "/human/api/mandates", mandateBody(24*time.Hour), "Idempotency-Key", "k-1"))["id"].(string)
	mine := lt.SeedJob(t, r.Store, lt.FixedNow, lt.JobSeed{Name: "mandate-mine", Principal: "agent:nightshift"})
	other := lt.SeedJob(t, r.Store, lt.FixedNow, lt.JobSeed{Name: "mandate-other", Principal: "agent:nightshift"})
	// the column is frozen after insert (the spend path sets it at create); the test attaches it the one way a rig can
	if _, err := r.Store.W.Exec(`DROP TRIGGER batches_mandate_frozen`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.W.Exec(`UPDATE batches SET mandate_id = ? WHERE id = ?`, id, mine.BatchID); err != nil {
		t.Fatal(err)
	}
	by := func(job string) string {
		var v *string
		if err := r.Store.R.QueryRow(`SELECT cancel_requested_by FROM jobs WHERE id = ?`, job).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v == nil {
			return ""
		}
		return *v
	}
	if by(mine.JobID) != "" {
		t.Fatal("the job starts cancelled")
	}
	obj(t, owner.OK(t, "POST", "/api/mandates/"+id+"/revoke", map[string]any{"reason": "stop"}))
	if got := by(mine.JobID); got != "system:mandate-revoke" {
		t.Fatalf("job of the mandate: cancel_requested_by = %q", got)
	}
	if got := by(other.JobID); got != "" {
		t.Fatalf("a batch of no mandate was cancelled by %q", got)
	}
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'revoke_mandate' AND status = 200`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows %d (%v)", n, err)
	}
}
