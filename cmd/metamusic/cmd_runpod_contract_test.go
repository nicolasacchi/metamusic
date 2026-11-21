package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const cliDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// quote caches a quote that reports digest, observed at at, as the serverless backend's provider_state.
func (r *rpRig) quote(t *testing.T, digest string, at time.Time) {
	t.Helper()
	idle := 20
	q := provider.Quote{Backend: provider.BackendRunPod, ListPricePPH: 690000, ObservedAt: at,
		Endpoint: provider.EndpointConfig{Name: "mm-yue2", Type: "QUEUE", ImageDigest: digest, WorkersMax: 2, IdleTimeoutS: &idle}}
	b, _ := json.Marshal(q)
	r.exec(t, `INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?,1,'system:default',?,?,?)
		ON CONFLICT(backend) DO UPDATE SET quote_json = excluded.quote_json, quote_at = excluded.quote_at`, provider.BackendRunPod, store.FormatTime(at), string(b), store.FormatTime(at))
}

// queueJob inserts one queued job that needs the given capabilities.
func (r *rpRig) queueJob(t *testing.T, idx int, id, needs string) {
	t.Helper()
	var batch, recipe string
	if err := r.db(t).R.QueryRow(`SELECT batch_id, recipe_id FROM jobs ORDER BY id LIMIT 1`).Scan(&batch, &recipe); err != nil {
		t.Fatal(err)
	}
	const at = "2026-01-01T00:00:00.000Z"
	r.exec(t, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at,needs_json)
		VALUES(?,?,?,'generate','yue2',?,?,'reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?,?)`, id, batch, idx, provider.BackendRunPod, recipe, at, at, at, needs)
}

func contractRow(t *testing.T, r *rpRig) provider.WorkerContractRow {
	t.Helper()
	row, ok, err := provider.LatestContract(t.Context(), r.db(t).R, provider.BackendRunPod)
	if err != nil || !ok {
		t.Fatalf("no contract row: %v", err)
	}
	return row
}

func TestCLI_ContractOpenClose(t *testing.T) {
	t.Parallel()
	r := newRPRig(t)
	librarytest.FixtureM5(t, r.db(t), librarytest.FixedNow)
	now := time.Now()

	// argument errors
	for _, tc := range []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"no subcommand", []string{"contract"}, 2, "status|open|close"},
		{"unknown subcommand", []string{"contract", "bogus"}, 2, "unknown subcommand"},
		{"open without a reason", []string{"contract", "open", "--caps", "v2"}, 2, "reason"},
		{"open without caps", []string{"contract", "open", "--reason", "r"}, 2, "--caps is required"},
		{"open an unknown cap", []string{"contract", "open", "--caps", "v2,warp", "--reason", "r"}, 2, "warp"},
		{"open without v2", []string{"contract", "open", "--caps", "ears", "--reason", "r"}, 2, "v2"},
		{"close without a reason", []string{"contract", "close"}, 2, "reason"},
		{"open with no quote", []string{"contract", "open", "--caps", "v2", "--reason", "r"}, 3, "no fresh quote"},
	} {
		if code, _, errs := rp(t, r.env, tc.args...); code != tc.code || !strings.Contains(errs, tc.msg) {
			t.Errorf("%s: %d %q, want %d containing %q", tc.name, code, errs, tc.code, tc.msg)
		}
	}
	// a stale quote is refused
	r.quote(t, cliDigestA, now.Add(-2*time.Hour))
	if code, _, errs := rp(t, r.env, "contract", "open", "--caps", "v2", "--reason", "r"); code != 3 || !strings.Contains(errs, "no fresh quote") {
		t.Errorf("stale quote: %d %s", code, errs)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM worker_contract_log`); n != 0 {
		t.Fatalf("a refused open wrote %d rows", n)
	}

	r.quote(t, cliDigestA, now)
	if code, out, errs := rp(t, r.env, "contract", "open", "--caps", "v2", "--reason", "canary v2-wire approved"); code != 0 || !strings.Contains(out, "v2") {
		t.Fatalf("open: %d %s %s", code, out, errs)
	}
	row := contractRow(t, r)
	if row.Via != "cli" || row.ChangedBy != "system:cli" || row.ImageDigest != cliDigestA || !slices.Equal(row.Caps, []string{"v2"}) || row.Reason != "canary v2-wire approved" {
		t.Fatalf("row = %+v", row)
	}
	if n, principal, transport, _ := r.auditOps(t, "cli:runpod-contract-open"); n != 1 || principal != "system:cli" || transport != "cli" {
		t.Errorf("open audit %d %s %s", n, principal, transport)
	}
	if code, _, errs := rp(t, r.env, "contract", "open", "--caps", "ears", "--reason", "wave 2"); code == 0 || !strings.Contains(errs, "v2") {
		t.Errorf("open ears without v2: %d %s", code, errs)
	}
	if code, out, errs := rp(t, r.env, "contract", "open", "--caps", "v2,ears", "--reason", "wave 2"); code != 0 {
		t.Fatalf("open ears: %d %s %s", code, out, errs)
	}
	if got := contractRow(t, r).Caps; !slices.Equal(got, []string{"ears", "v2"}) {
		t.Fatalf("caps = %v", got)
	}

	// status, text and JSON
	if code, out, _ := rp(t, r.env, "contract", "status"); code != 0 || !strings.Contains(out, "open capabilities: ears,v2") {
		t.Errorf("status: %d %s", code, out)
	}
	code, out, _ := rp(t, r.env, "contract", "status", "--json")
	var v contractView
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || !slices.Equal(v.Caps, []string{"ears", "v2"}) || v.ImageDigest != cliDigestA {
		t.Errorf("status json: %d %v %+v", code, err, v)
	}

	// a new image closes everything without a row
	r.quote(t, "sha256:"+strings.Repeat("b", 64), now)
	if _, out, _ := rp(t, r.env, "contract", "status"); !strings.Contains(out, "open capabilities: none") || !strings.Contains(out, "another image") {
		t.Errorf("status after an image change: %s", out)
	}
	r.quote(t, cliDigestA, now)

	// close refuses while queued jobs need the capability, listing them
	r.queueJob(t, 900, "job-needs-ears", `["ears","v2"]`)
	r.queueJob(t, 901, "job-plain", `[]`)
	code, _, errs := rp(t, r.env, "contract", "close", "--caps", "ears", "--reason", "ears broke")
	if code != 3 || !strings.Contains(errs, "job-needs-ears") || strings.Contains(errs, "job-plain") || !strings.Contains(errs, "--strand-queued") {
		t.Fatalf("close with queued jobs: %d %s", code, errs)
	}
	if got := contractRow(t, r).Caps; !slices.Equal(got, []string{"ears", "v2"}) {
		t.Fatalf("a refused close changed the gate: %v", got)
	}
	// closing a capability nobody queued for needs no flag
	r.exec(t, `UPDATE jobs SET state = 'cancelled', finished_at = updated_at WHERE id = 'job-needs-ears'`)
	if code, out, errs := rp(t, r.env, "contract", "close", "--caps", "ears", "--reason", "ears broke"); code != 0 || !strings.Contains(out, "closed ears") {
		t.Fatalf("close: %d %s %s", code, out, errs)
	}
	if got := contractRow(t, r).Caps; !slices.Equal(got, []string{"v2"}) {
		t.Fatalf("caps after close = %v", got)
	}
	if n, _, _, _ := r.auditOps(t, "cli:runpod-contract-close"); n != 1 {
		t.Errorf("close audit rows = %d", n)
	}
	if code, out, _ := rp(t, r.env, "contract", "close", "--caps", "ears", "--reason", "again"); code != 0 || !strings.Contains(out, "nothing changed") {
		t.Errorf("closing a closed cap: %d %s", code, out)
	}
	// --strand-queued closes everything even with a queued job that needs it
	r.queueJob(t, 902, "job-needs-v2", `["v2"]`)
	if code, _, _ := rp(t, r.env, "contract", "close", "--reason", "image bad"); code != 3 {
		t.Errorf("close all with a queued v2 job: %d", code)
	}
	if code, out, errs := rp(t, r.env, "contract", "close", "--reason", "image bad", "--strand-queued"); code != 0 || !strings.Contains(out, "1 queued job(s) stranded") {
		t.Fatalf("strand: %d %s %s", code, out, errs)
	}
	if got := contractRow(t, r).Caps; len(got) != 0 {
		t.Fatalf("caps after close all = %v", got)
	}
	if code, out, _ := rp(t, r.env, "contract", "close", "--reason", "x"); code != 0 || !strings.Contains(out, "no capability is open") {
		t.Errorf("close with nothing open: %d %s", code, out)
	}
}
