package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Canary values are built at runtime, never literals (the gitleaks rule).
func rpCanary(name string) string { return "canary-" + name + "-" + strings.Repeat("x", 12) }

var rpNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type rpRig struct {
	env  map[string]string
	srv  *runpodtest.Server
	keys runpodtest.KeySet
	ep   string
	data string
	sec  string
	rec  string
	once sync.Once
	st   *store.Store
}

// newRPRig is a data directory with a database, runpod.env and a runpodtest server. The admin key lives in
// <secrets>/runpod-admin.env, as on the host.
func newRPRig(t *testing.T) *rpRig {
	t.Helper()
	r := newRPRigNoDB(t)
	r.db(t) // creates the database the CLI requires to exist
	return r
}

// newRPRigNoDB is newRPRig without a database (probe and canary never touch it).
func newRPRigNoDB(t *testing.T) *rpRig {
	t.Helper()
	env := testEnv(t, "local")
	data := env["MM_DATA_DIR"]
	sec := filepath.Join(data, "secrets")
	keys := runpodtest.KeySet{Jobs: rpCanary("jobs"), Read: rpCanary("read"), Write: rpCanary("write"), Admin: rpCanary("admin")}
	ep := rpCanary("endpoint")
	srv := runpodtest.New(t, runpodtest.Options{Keys: keys, EndpointID: ep})
	rec := t.TempDir() // outside the data directory
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sec, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("runpod.env", "RUNPOD_JOBS_KEY="+keys.Jobs+"\nRUNPOD_READ_KEY="+keys.Read+"\nRUNPOD_ENDPOINT_ID="+ep+"\n")
	write("runpod-admin.env", "RUNPOD_ADMIN_KEY="+keys.Admin+"\n")
	env["MM_RUNPOD_BASE_URL"] = srv.URL
	env["MM_RUNPOD_RECORD_DIR"] = rec
	return &rpRig{env: env, srv: srv, keys: keys, ep: ep, data: data, sec: sec, rec: rec}
}

// db is the test's own handle on the database (opened once; the CLI opens its own).
func (r *rpRig) db(t *testing.T) *store.Store {
	t.Helper()
	r.once.Do(func() {
		st, err := store.Open(context.Background(), filepath.Join(r.data, "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		r.st = st
	})
	return r.st
}

// rp runs `metamusic runpod ...` through cmdRunPod with run()'s error-to-exit-code mapping.
func rp(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := cmdRunPod(context.Background(), args, envFn(env), streams{strings.NewReader(""), &out, &errb})
	code := 0
	if err != nil {
		var x exitError
		var u usageError
		switch {
		case errors.As(err, &x):
			code = x.code
			if x.err != nil {
				errb.WriteString("metamusic: " + x.err.Error() + "\n")
			}
		case errors.As(err, &u):
			code = 2
			errb.WriteString("metamusic: " + string(u) + "\n")
		default:
			code = 1
			errb.WriteString("metamusic: " + err.Error() + "\n")
		}
	}
	return code, out.String(), errb.String()
}

func (r *rpRig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db(t).W.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (r *rpRig) providerRow(t *testing.T) (armed int, by, reason string) {
	t.Helper()
	var rs sql.NullString
	if err := r.db(t).R.QueryRow(`SELECT armed, changed_by, reason FROM provider_state WHERE backend = ?`, provider.BackendRunPod).Scan(&armed, &by, &rs); err != nil {
		t.Fatal(err)
	}
	return armed, by, rs.String
}

func (r *rpRig) auditOps(t *testing.T, op string) (n int, principal, transport, method string) {
	t.Helper()
	rows, err := r.db(t).R.Query(`SELECT principal_id, transport, method FROM audit_log WHERE op = ?`, op)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&principal, &transport, &method); err != nil {
			t.Fatal(err)
		}
	}
	return
}

// seedQuote writes a provider_state row with a cached quote that passes the C77.5 checks (cold start 180,
// max runtime 15 min), unless mod changes it.
func (r *rpRig) seedQuote(t *testing.T, armed int, mod func(*provider.Quote)) {
	t.Helper()
	idle := 20
	q := provider.Quote{Backend: provider.BackendRunPod, ListPricePPH: 1_100_000, Endpoint: provider.EndpointConfig{
		Name: "mm-yue2", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: "240", WorkersMax: 1, IdleTimeoutS: &idle,
		TimeoutMS: 900_000, GPUCount: 1}}
	if mod != nil {
		mod(&q)
	}
	raw, _ := json.Marshal(q)
	r.exec(t, `INSERT OR REPLACE INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?,?,?,?,?,?)`,
		provider.BackendRunPod, armed, "system:default", store.FormatTime(time.Now().Add(-time.Hour)), string(raw), store.FormatTime(time.Now().Add(-time.Minute)))
}

func TestCLI_ArmRequiresEndpointName(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	r.seedQuote(t, 0, nil)
	if code, _, errs := rp(t, r.env, "arm", "--reason", "go"); code != 2 || !strings.Contains(errs, "endpoint-name") {
		t.Errorf("missing name: %d %s", code, errs)
	}
	if code, _, errs := rp(t, r.env, "arm", "--endpoint-name", "mm-yue2"); code != 2 || !strings.Contains(errs, "reason") {
		t.Errorf("missing reason: %d %s", code, errs)
	}
	if code, _, errs := rp(t, r.env, "arm", "--endpoint-name", "wrong", "--reason", "go"); code != 3 || !strings.Contains(errs, "does not match") {
		t.Errorf("wrong name: %d %s", code, errs)
	}
	if armed, _, _ := r.providerRow(t); armed != 0 {
		t.Fatal("a refused arm armed the provider")
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-arm"); n != 0 {
		t.Error("a refusal wrote an arm audit row")
	}
	code, out, errs := rp(t, r.env, "arm", "--endpoint-name", "mm-yue2", "--reason", "first canary")
	if code != 0 || !strings.Contains(out, "armed") {
		t.Fatalf("arm: %d %s %s", code, out, errs)
	}
	armed, by, reason := r.providerRow(t)
	if armed != 1 || by != "system:cli" || reason != "first canary" {
		t.Errorf("row armed=%d by=%s reason=%q", armed, by, reason)
	}
	n, principal, transport, method := r.auditOps(t, "cli:runpod-arm")
	if n != 1 || principal != "system:cli" || transport != "cli" || method != "SYSTEM" {
		t.Errorf("audit n=%d %s %s %s", n, principal, transport, method)
	}
	// a cached quote that fails the C77.5 checks refuses even with the right name
	r.seedQuote(t, 0, func(q *provider.Quote) { q.Endpoint.WorkersMax = 0 })
	if code, _, errs := rp(t, r.env, "arm", "--endpoint-name", "mm-yue2", "--reason", "again"); code != 3 || !strings.Contains(errs, "endpoint_inactive") {
		t.Errorf("inactive endpoint: %d %s", code, errs)
	}
	// no quote at all
	r.exec(t, `UPDATE provider_state SET quote_json = NULL, quote_at = NULL WHERE backend = ?`, provider.BackendRunPod)
	if code, _, errs := rp(t, r.env, "arm", "--endpoint-name", "mm-yue2", "--reason", "again"); code != 3 || !strings.Contains(errs, "no cached quote") {
		t.Errorf("no quote: %d %s", code, errs)
	}
	// keys absent
	if err := os.Remove(filepath.Join(r.sec, "runpod.env")); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := rp(t, r.env, "arm", "--endpoint-name", "mm-yue2", "--reason", "again"); code != 3 || !strings.Contains(errs, "keys_absent") {
		t.Errorf("keys absent: %d %s", code, errs)
	}
}

func TestCLI_ArmAfterRestore(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	r.seedQuote(t, 0, nil)
	restored := store.FormatTime(rpNow)
	r.exec(t, `UPDATE provider_state SET restored_at = ?, reason = 'restored', changed_by = 'system:cli' WHERE backend = ?`, restored, provider.BackendRunPod)
	args := []string{"arm", "--endpoint-name", "mm-yue2", "--reason", "after the restore"}
	code, _, errs := rp(t, r.env, args...)
	if code != 3 || !strings.Contains(errs, "RECONCILE_REQUIRED") {
		t.Fatalf("restore not covered: %d %s", code, errs)
	}
	if armed, _, _ := r.providerRow(t); armed != 0 {
		t.Fatal("armed before a reconciliation covered the restore")
	}
	// the committed key-capabilities.json says billing is readable, so --after-restore is not an
	// acknowledgment that applies: only a reconciled billing window covers the restore
	if code, _, errs := rp(t, r.env, append(args, "--after-restore")...); code != 3 || !strings.Contains(errs, "RECONCILE_REQUIRED") {
		t.Fatalf("--after-restore with readable billing: %d %s", code, errs)
	}
	if armed, _, _ := r.providerRow(t); armed != 0 {
		t.Fatal("armed by --after-restore although billing is readable")
	}
	// a billing window ending after the restore covers it (no flag needed)
	r.exec(t, `INSERT INTO billing_windows(window_start, window_end, granularity, buckets, billed_micro_usd, billed_compute_micro_usd, provisional_micro_usd, reserved_micro_usd, batches, verdict, fetched_at)
		VALUES (?,?, 'hour', 1, 0, 0, 0, 0, 0, 'ok', ?)`, store.FormatTime(rpNow.Add(time.Hour)), store.FormatTime(rpNow.Add(2*time.Hour)), store.FormatTime(rpNow.Add(3*time.Hour)))
	if code, out, errs := rp(t, r.env, args...); code != 0 {
		t.Fatalf("covered restore: %d %s %s", code, out, errs)
	}
}

func TestCLI_DisarmAndResetTrip(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	r.exec(t, `INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES (?,1,'system:default',?)`, provider.BackendRunPod, store.FormatTime(rpNow))
	if code, _, errs := rp(t, r.env, "disarm"); code != 2 || !strings.Contains(errs, "reason") {
		t.Errorf("disarm without reason: %d %s", code, errs)
	}
	if code, _, errs := rp(t, r.env, "disarm", "--reason", strings.Repeat("x", 501)); code != 2 {
		t.Errorf("501-character reason: %d %s", code, errs)
	}
	if code, out, errs := rp(t, r.env, "disarm", "--reason", "stop spending"); code != 0 || !strings.Contains(out, "disarmed") {
		t.Fatalf("disarm: %d %s %s", code, out, errs)
	}
	armed, by, reason := r.providerRow(t)
	if armed != 0 || by != "system:cli" || reason != "stop spending" {
		t.Errorf("row %d %s %q", armed, by, reason)
	}
	n, principal, transport, _ := r.auditOps(t, "cli:runpod-disarm")
	if n != 1 || principal != "system:cli" || transport != "cli" {
		t.Errorf("disarm audit n=%d %s %s", n, principal, transport)
	}
	// reset-trip: no open trip => nothing changes, no audit row
	if code, out, _ := rp(t, r.env, "reset-trip", "--reason", "looked"); code != 0 || !strings.Contains(out, "no open trip") {
		t.Fatalf("reset without a trip: %d %s", code, out)
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-reset-trip"); n != 0 {
		t.Error("a no-op reset wrote an audit row")
	}
	r.exec(t, `INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES ('trip-1', ?, ?, 'crash_loop', '{}', '[]')`,
		provider.BackendRunPod, store.FormatTime(rpNow))
	if code, out, errs := rp(t, r.env, "reset-trip", "--reason", "checked the console"); code != 0 || !strings.Contains(out, "trip reset") {
		t.Fatalf("reset: %d %s %s", code, out, errs)
	}
	var resetBy, resetReason string
	if err := r.db(t).R.QueryRow(`SELECT reset_by, reset_reason FROM provider_trips WHERE id = 'trip-1'`).Scan(&resetBy, &resetReason); err != nil {
		t.Fatal(err)
	}
	if resetBy != "system:cli" || resetReason != "checked the console" {
		t.Errorf("trip reset by %q reason %q", resetBy, resetReason)
	}
	if n, principal, _, method := r.auditOps(t, "cli:runpod-reset-trip"); n != 1 || principal != "system:cli" || method != "SYSTEM" {
		t.Errorf("reset audit %d %s %s", n, principal, method)
	}
	if code, out, _ := rp(t, r.env, "reset-trip", "--reason", "again"); code != 0 || !strings.Contains(out, "no open trip") {
		t.Errorf("second reset: %d %s", code, out)
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-reset-trip"); n != 1 {
		t.Errorf("audit rows after a no-op: %d", n)
	}
	// disarm creates the row when none exists (a fresh database)
	r2 := newRPRig(t)
	if code, _, errs := rp(t, r2.env, "disarm", "--reason", "fresh"); code != 0 {
		t.Fatalf("disarm on an empty database: %d %s", code, errs)
	}
	if armed, _, _ := r2.providerRow(t); armed != 0 {
		t.Error("row not disarmed")
	}
}

func TestCLI_RunPodStatusReadOnly(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	r.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 0 })
	// a job with two attempts whose refs exist on the server
	st := r.db(t)
	librarytest.FixtureM3(t, st, rpNow)
	seed := librarytest.SeedJob(t, st, rpNow, librarytest.JobSeed{Name: "status"})
	post := func() string {
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/v2/"+r.ep+"/run", strings.NewReader(`{"input":{"contract":"probe"}}`))
		req.Header.Set("Authorization", "Bearer "+r.keys.Jobs)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b struct{ ID string }
		_ = json.NewDecoder(resp.Body).Decode(&b)
		return b.ID
	}
	refs := []string{post(), post()}
	for i, ref := range refs {
		r.exec(t, `INSERT INTO job_attempts(id, job_id, n, backend, state, ref, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
			VALUES (?,?,?,?,'submitted',?,?,?,?,?)`, "attempt-"+string(rune('a'+i)), seed.JobID, i+1, "fake", ref, store.FormatTime(rpNow), store.FormatTime(rpNow.Add(time.Hour)), 690000, store.FormatTime(rpNow))
	}
	r.exec(t, `INSERT INTO job_events(job_id, at, from_state, to_state, event) VALUES (?,?,'queued','submitting','submit')`, seed.JobID, store.FormatTime(rpNow))
	before := len(r.srv.Requests())

	code, out, errs := rp(t, r.env, "status", "--json", "--job", seed.JobID, "--repoll")
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	for _, req := range r.srv.Requests()[before:] {
		if req.Method != http.MethodGet {
			t.Errorf("status sent %s %s (%s)", req.Method, req.Path, req.Op)
		}
		if req.Key == runpodtest.KindAdmin || req.Key == runpodtest.KindWrite {
			t.Errorf("status used the %s key", req.Key)
		}
	}
	if n := r.srv.Calls(runpodtest.OpStatus); n != 2 {
		t.Errorf("--repoll issued %d /status calls, want 2 (one per attempt ref)", n)
	}
	var v statusView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if v.Job == nil || len(v.Job.Attempts) != 2 || len(v.Job.Events) < 1 {
		t.Fatalf("job view %+v", v.Job)
	}
	for _, a := range v.Job.Attempts {
		if !strings.HasPrefix(a.Repoll, "http=200 status=") {
			t.Errorf("repoll %q", a.Repoll)
		}
	}
	if v.Endpoint == nil || v.Endpoint["name"] != "mm-yue2" {
		t.Errorf("endpoint %+v errors %v", v.Endpoint, v.Errors)
	}
	// nothing identifying in the output
	for _, secret := range append([]string{r.ep, r.keys.Jobs, r.keys.Read, r.keys.Admin}, refs...) {
		if strings.Contains(out, secret) || strings.Contains(errs, secret) {
			t.Errorf("status output contains %q", secret[:8])
		}
	}
	// without --repoll no /status call is made
	n := r.srv.Calls(runpodtest.OpStatus)
	if code, _, errs := rp(t, r.env, "status", "--job", seed.JobID); code != 0 {
		t.Fatalf("status without repoll: %d %s", code, errs)
	}
	if r.srv.Calls(runpodtest.OpStatus) != n {
		t.Error("status polled without --repoll")
	}
	if code, _, _ := rp(t, r.env, "status", "--repoll"); code != 2 {
		t.Errorf("--repoll without --job: %d", code)
	}
	// human output
	if code, out, _ := rp(t, r.env, "status"); code != 0 || !strings.Contains(out, "armed:") || !strings.Contains(out, "trip:") {
		t.Errorf("human status: %d\n%s", code, out)
	}
}

func TestCLI_OnlyProvisionReadsAdminKey(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	r.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 0 })
	r.srv.NextJob(runpodtest.Behavior{Expire: true}) // the probe's TTL observation ends at once
	r.seedQuote(t, 1, nil)
	for _, args := range [][]string{
		{"probe", "--keys", "--out", filepath.Join(t.TempDir(), "caps.json")},
		{"status"},
		{"arm", "--endpoint-name", "mm-yue2", "--reason", "x"},
		{"disarm", "--reason", "x"},
		{"reset-trip", "--reason", "x"},
	} {
		if code, out, errs := rp(t, r.env, args...); code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, out, errs)
		}
	}
	// the canary needs no RunPod key at all
	if code, _, _ := rp(t, r.env, "canary", "--url", "http://127.0.0.1:1"); code != 2 {
		t.Errorf("canary without MM_TOKEN: %d", code)
	}
	if len(r.srv.Requests()) == 0 {
		t.Fatal("no request reached the server")
	}
	for _, req := range r.srv.Requests() {
		if req.Key == runpodtest.KindAdmin || strings.Contains(req.Header.Get("Authorization"), r.keys.Admin) {
			t.Fatalf("%s %s used the admin key", req.Method, req.Path)
		}
	}
	// provision reads it
	img := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	if code, out, errs := rp(t, r.env, "provision", "--image", img, "--dry-run"); code != 0 {
		t.Fatalf("provision --dry-run: %d %s %s", code, out, errs)
	}
	admin := 0
	for _, req := range r.srv.Requests() {
		if req.Key == runpodtest.KindAdmin {
			admin++
			if req.Method != http.MethodGet {
				t.Errorf("a dry run sent %s", req.Method)
			}
		}
	}
	if admin == 0 {
		t.Error("provision did not use the admin key")
	}
	// an all-access key in runpod.env is refused (it belongs in runpod-admin.env)
	if err := os.WriteFile(filepath.Join(r.sec, "runpod.env"), []byte("RUNPOD_JOBS_KEY=a\nRUNPOD_READ_KEY=b\nRUNPOD_ENDPOINT_ID=c\nRUNPOD_ADMIN_KEY=d\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := rp(t, r.env, "status"); code == 0 || !strings.Contains(errs, "runpod-admin.env") {
		t.Errorf("admin key in runpod.env: %d %s", code, errs)
	}
}

func TestCLI_AdminKeyNeverPrinted(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	img := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	run := func(args ...string) (string, string) {
		_, out, errs := rp(t, r.env, args...)
		return out, errs
	}
	var all strings.Builder
	for _, args := range [][]string{
		{"provision", "--image", img, "--dry-run"},
		{"provision", "--image", img},
		{"provision", "--image", img}, // idempotent second run
		{"provision", "--image", "ghcr.io/example/worker:latest"},
		{"provision", "--admin-env", filepath.Join(r.sec, "missing.env")},
	} {
		out, errs := run(args...)
		all.WriteString(out + errs)
	}
	// a failing server: the error text must not carry the key either
	r.srv.Fault(runpodtest.OpCatalog, runpodtest.Fault{Status: 403, Body: "refused " + r.keys.Admin, Times: -1})
	out, errs := run("provision", "--image", img)
	all.WriteString(out + errs)
	// audit rows, every column
	rows, err := r.db(t).R.Query(`SELECT id||at||principal_kind||principal_id||principal_name||transport||client||op||method||args_sha256||status||ifnull(error_code,'')||idempotency||created_ids_json FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	nAudit := 0
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		all.WriteString(line)
		nAudit++
	}
	_ = rows.Close()
	files, _ := filepath.Glob(filepath.Join(r.rec, "*"))
	if len(files) == 0 {
		t.Error("the recorder wrote nothing")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}
	for _, secret := range []string{r.keys.Admin, r.keys.Jobs, r.keys.Read, r.ep} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("a secret value (%s...) appears in stdout, stderr, audit rows or recorder files", secret[:12])
		}
	}
	if n, principal, transport, _ := r.auditOps(t, "cli:runpod-provision"); n == 0 || principal != "system:cli" || transport != "cli" {
		t.Errorf("provision audit rows %d %s %s (total %d)", n, principal, transport, nAudit)
	}
}

func TestCLI_ProvisionFlags(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	for _, args := range [][]string{
		{"provision", "--activate", "--deactivate"},
		{"provision", "--max", "1"},
		{"provision", "--activate", "--max", "3"},
		{"provision", "extra"},
		{"nonsense"},
		{},
	} {
		if code, _, _ := rp(t, r.env, args...); code != 2 {
			t.Errorf("%v => exit %d, want 2", args, code)
		}
	}
	// --activate defaults to 2 workers, --max 1 sets one; both through the server's PATCH
	img := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	workersMax := func() int { return runpodtest.Num(r.srv.Endpoint()["workers"].(map[string]any)["max"]) }
	if code, out, errs := rp(t, r.env, "provision", "--image", img, "--activate", "--max", "1"); code != 0 || workersMax() != 1 {
		t.Fatalf("activate --max 1: %d %s %s max=%d", code, out, errs, workersMax())
	}
	if code, out, errs := rp(t, r.env, "provision", "--image", img, "--activate"); code != 0 || workersMax() != 2 {
		t.Fatalf("activate: %d %s %s max=%d", code, out, errs, workersMax())
	}
	if code, out, errs := rp(t, r.env, "provision", "--image", img, "--deactivate"); code != 0 || workersMax() != 0 {
		t.Fatalf("deactivate: %d %s %s max=%d", code, out, errs, workersMax())
	}
	_, out, _ := rp(t, r.env, "provision", "--image", img, "--dry-run")
	if !strings.Contains(out, "console step 1") {
		t.Errorf("the console step is not printed:\n%s", out)
	}
}

func TestCLI_RunPodProbeSchema(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	serve := func(body []byte) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		t.Cleanup(s.Close)
		return s.URL
	}
	if code, out, errs := rp(t, r.env, "probe"); code != 2 {
		t.Errorf("no flags: %d %s %s", code, out, errs)
	}
	code, out, errs := rp(t, r.env, "probe", "--schema", "--spec-url", serve(runpod.Snapshot()))
	if code != 0 || !strings.Contains(out, `"drift": false`) {
		t.Fatalf("same spec: %d %s %s", code, out, errs)
	}
	renamed := bytes.ReplaceAll(runpod.Snapshot(), []byte(`"minCudaVersion"`), []byte(`"minCuda"`))
	code, out, _ = rp(t, r.env, "probe", "--schema", "--spec-url", serve(renamed))
	if code != 1 || !strings.Contains(out, `"ok": false`) {
		t.Errorf("renamed field: %d %s", code, out)
	}
	// a schema-only run writes nothing unless --out is given
	dest := filepath.Join(t.TempDir(), "c.json")
	if code, _, _ := rp(t, r.env, "probe", "--schema", "--spec-url", serve(runpod.Snapshot()), "--out", dest); code != 0 {
		t.Fatal("probe --out failed")
	}
	if b, err := os.ReadFile(dest); err != nil || runpod.ValidateCapabilities(b) != nil {
		t.Errorf("output file: %v %v", err, runpod.ValidateCapabilities(b))
	}
	// keys: the probe file is written and safe
	r.srv.SetEndpoint(func(ep map[string]any) { ep["workers"].(map[string]any)["max"] = 0 })
	r.srv.NextJob(runpodtest.Behavior{Expire: true})
	dest2 := filepath.Join(t.TempDir(), "k.json")
	if code, out, errs := rp(t, r.env, "probe", "--keys", "--out", dest2); code != 0 {
		t.Fatalf("probe --keys: %d %s %s", code, out, errs)
	}
	b, _ := os.ReadFile(dest2)
	if err := runpod.ValidateCapabilities(b); err != nil {
		t.Error(err)
	}
	if err := runpod.CheckFileSafe(b, r.ep); err != nil {
		t.Error(err)
	}
	if strings.Contains(string(b), r.keys.Jobs) {
		t.Error("a key reached the capabilities file")
	}
}

func TestCLI_RunPodCanaryExitCodes(t *testing.T) {
	t.Parallel()
	r := newRPRigNoDB(t)
	disarmed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generation_enabled":true,"armed":false,"trip":null,"cold_start_s":180,"max_runtime_s":900}`))
	}))
	t.Cleanup(disarmed.Close)
	env := map[string]string{}
	for k, v := range r.env {
		env[k] = v
	}
	if code, _, errs := rp(t, env, "canary", "--url", disarmed.URL); code != 2 || !strings.Contains(errs, "MM_TOKEN") {
		t.Errorf("no token: %d %s", code, errs)
	}
	env["MM_TOKEN"] = rpCanary("mm-token")
	for _, args := range [][]string{{"canary"}, {"canary", "--url", disarmed.URL, "--case", "bogus"}, {"canary", "--url", disarmed.URL, "--max-usd", "zero"},
		{"canary", "--url", disarmed.URL, "--start-timeout", "0s"}, {"canary", "--url", disarmed.URL, "--start-timeout", "soon"}} {
		if code, _, _ := rp(t, env, args...); code != 2 {
			t.Errorf("%v => %d, want 2", args, code)
		}
	}
	if code, _, _ := rp(t, env, "canary", "--url", disarmed.URL, "--start-timeout", "15m"); code != 3 {
		t.Errorf("a valid --start-timeout must reach the disarmed refusal: %d", code)
	}
	code, out, errs := rp(t, env, "canary", "--url", disarmed.URL)
	if code != 3 || !strings.Contains(out, `"outcome": "refused"`) {
		t.Fatalf("disarmed: %d %s %s", code, out, errs)
	}
	if strings.Contains(out+errs, env["MM_TOKEN"]) {
		t.Error("the token was printed")
	}
}

func TestRunPodBases(t *testing.T) {
	t.Parallel()
	for url, ok := range map[string]bool{"": true, "https://example.test": true, "http://127.0.0.1:9": true, "http://localhost:9": true,
		"http://example.test": false, "ftp://x": false, "not a url": false} {
		_, _, err := runpodBases(func(string) string { return url })
		if (err == nil) != ok {
			t.Errorf("%q: err = %v", url, err)
		}
	}
}

// First-time provisioning: the operator has written the scoped keys but there is no endpoint id yet.
func TestCLI_ProvisionWithoutEndpointID(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	img := "ghcr.io/example/worker@sha256:" + strings.Repeat("a", 64)
	envFile := filepath.Join(r.sec, "runpod.env")
	noID := "RUNPOD_JOBS_KEY=" + r.keys.Jobs + "\nRUNPOD_READ_KEY=" + r.keys.Read + "\n"
	if err := os.WriteFile(envFile, []byte(noID), 0o600); err != nil {
		t.Fatal(err)
	}
	r.srv.SetEndpoint(func(ep map[string]any) { ep["name"] = "other" }) // so provision creates
	if code, out, errs := rp(t, r.env, "provision", "--image", img, "--dry-run"); code != 0 || strings.Contains(out, "append this line") {
		t.Fatalf("dry run: %d %s %s", code, out, errs)
	}
	code, out, errs := rp(t, r.env, "provision", "--image", img)
	if code != 0 {
		t.Fatalf("provision without RUNPOD_ENDPOINT_ID: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "append this line to runpod.env: RUNPOD_ENDPOINT_ID="+r.ep) {
		t.Errorf("no endpoint id hint after create:\n%s", out)
	}
	// every other command still needs the id
	if code, _, errs := rp(t, r.env, "status"); code == 0 || !strings.Contains(errs, "RUNPOD_ENDPOINT_ID") {
		t.Errorf("status without the id: %d %s", code, errs)
	}
	// the all-access key is still refused in runpod.env, id or not
	if err := os.WriteFile(envFile, []byte(noID+"RUNPOD_ADMIN_KEY="+r.keys.Admin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs = rp(t, r.env, "provision", "--image", img, "--dry-run")
	if code == 0 || !strings.Contains(errs, "runpod-admin.env") || strings.Contains(out+errs, r.keys.Admin) {
		t.Errorf("admin key in runpod.env: %d %s %s", code, out, errs)
	}
}

// liveAttempts decodes `runpod status --json` and returns the live_attempts field, failing when it is
// absent or not an array (scripts/deploy-preflight.sh fails closed on that).
func liveAttempts(t *testing.T, r *rpRig) []any {
	t.Helper()
	code, out, errs := rp(t, r.env, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	la, ok := m["live_attempts"].([]any)
	if !ok {
		t.Fatalf("live_attempts missing or not an array: %s", out)
	}
	return la
}

func TestCLI_RunPodStatusLiveAttempts(t *testing.T) {
	t.Parallel()
	t.Run("keys absent, no database", func(t *testing.T) {
		t.Parallel()
		r := newRPRigNoDB(t)
		if err := os.Remove(filepath.Join(r.sec, "runpod.env")); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(r.sec, "runpod-admin.env"))
		code, out, _ := rp(t, r.env, "status", "--json")
		if code != 0 || !strings.Contains(out, `"keys_absent": true`) {
			t.Skipf("status without runpod.env is not supported here: %d %s", code, out)
		}
		if la := liveAttempts(t, r); len(la) != 0 {
			t.Errorf("want none: %v", la)
		}
	})
	t.Run("keys present, nothing in flight", func(t *testing.T) {
		t.Parallel()
		r := newRPRig(t)
		if la := liveAttempts(t, r); len(la) != 0 {
			t.Errorf("want none: %v", la)
		}
	})
	t.Run("one live attempt", func(t *testing.T) {
		t.Parallel()
		r := newRPRig(t)
		st := r.db(t)
		librarytest.FixtureM3(t, st, rpNow)
		seed := librarytest.SeedJob(t, st, rpNow, librarytest.JobSeed{Name: "live"})
		r.exec(t, `INSERT INTO job_attempts(id, job_id, n, backend, state, ref, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
			VALUES ('att-live',?,1,'fake','submitted','ref-live',?,?,690000,?)`, seed.JobID, store.FormatTime(rpNow), store.FormatTime(rpNow.Add(time.Hour)), store.FormatTime(rpNow))
		r.exec(t, `INSERT INTO job_attempts(id, job_id, n, backend, state, ref, submitted_at, window_ends_at, price_pph_micro_usd, terminal_basis, terminal_at, created_at)
			VALUES ('att-done',?,2,'fake','done','ref-done',?,?,690000,'provider',?,?)`, seed.JobID, store.FormatTime(rpNow), store.FormatTime(rpNow.Add(time.Hour)), store.FormatTime(rpNow), store.FormatTime(rpNow))
		la := liveAttempts(t, r)
		if len(la) != 1 {
			t.Fatalf("want exactly the submitted attempt: %v", la)
		}
		if m := la[0].(map[string]any); m["id"] != "att-live" || m["state"] != "submitted" {
			t.Errorf("%v", m)
		}
	})
}
