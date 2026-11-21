package jobs

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const gateDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// setCaps appends a CLI row to the gate log of the rig's fake backend (the capabilities the operator has opened).
func (r *rig) setCaps(digest string, caps ...string) {
	r.t.Helper()
	if err := provider.AppendContract(r.t.Context(), r.st.W, provider.WorkerContractRow{Backend: provider.BackendFake, ImageDigest: digest, Caps: caps,
		Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: r.clk.Now(), Reason: "test"}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) latestContract() provider.WorkerContractRow {
	r.t.Helper()
	row, ok, err := provider.LatestContract(r.t.Context(), r.st.R, provider.BackendFake)
	if err != nil || !ok {
		r.t.Fatalf("latest contract: ok=%v err=%v", ok, err)
	}
	return row
}

func TestClaim_NeedsSubsetOfOpenCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		open  []string // nil: no log row at all (the fake then has every capability)
		needs string
		want  bool // claimed
	}{
		{"plain job, closed gate", []string{}, `[]`, true},
		{"plain job, no row", nil, `[]`, true},
		{"needs v2, no row (fake has them all)", nil, `["v2"]`, true},
		{"needs v2, closed", []string{}, `["v2"]`, false},
		{"needs v2, v2 open", []string{"v2"}, `["v2"]`, true},
		{"needs v2+ears, only v2 open", []string{"v2"}, `["ears","v2"]`, false},
		{"needs v2+ears, both open", []string{"ears", "v2"}, `["ears","v2"]`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if tc.open != nil {
				r.setCaps(gateDigest, tc.open...)
			}
			spec := `{}`
			if strings.Contains(tc.needs, "ears") {
				spec = `{"ears":{"budget_s":60,"stages":["asr"]}}`
			}
			job := r.seedBatch(batchSpec{needs: tc.needs, specJSON: spec}).Jobs[0]
			r.step()
			got := r.state(job) != StateQueued
			if got != tc.want {
				t.Fatalf("job state %s, claimed = %v, want %v", r.state(job), got, tc.want)
			}
			if !tc.want && len(r.atts(job)) != 0 {
				t.Fatalf("a job with closed needs got an attempt")
			}
		})
	}
}

// TestClaim_ClosedJobStaysClaimableForOthers: a closed need does not block a plain job queued behind it.
func TestClaim_ClosedJobDoesNotBlockPlainJobs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest)
	needy := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	plain := r.seedBatch(batchSpec{}).Jobs[0]
	r.step()
	r.wantState(needy, StateQueued)
	if got := r.state(plain); got == StateQueued {
		t.Fatalf("the plain job behind a closed one was not claimed")
	}
}

// TestMigration_WorkerContract_PlainYue2Unchanged: a database migrated from 0021 has no contract row, and a plain yue2 /1 job is
// claimed, sent as /1 and kept running exactly as before (production has no open contract).
func TestMigration_WorkerContract_PlainYue2Unchanged(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "s.db")
	pre, err := store.Open(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	// 0022 is purely additive, so removing what it created and the version stamp is a database exactly as 0021 left it (0024, the mandates, and 0023, the upload
	// fallback, are removed first: both are additive too)
	for _, q := range []string{
		`DROP TRIGGER estimates_mandate_frozen`, `DROP TRIGGER batches_mandate_frozen`, `DROP INDEX batches_mandate`,
		`ALTER TABLE estimates DROP COLUMN mandate_id`, `ALTER TABLE batches DROP COLUMN mandate_id`, `DROP TABLE mandates`,
		`DROP TRIGGER uploads_via_on_insert`, `DROP TRIGGER uploads_via_set_once`, `DROP TRIGGER uploads_via_when_received`,
		`DROP TABLE fallback_objects`, `DROP TABLE upload_drills`, `ALTER TABLE uploads DROP COLUMN via`,
		`DROP VIEW worker_contract`, `DROP TABLE worker_contract_log`, `PRAGMA user_version = 21`,
	} {
		if _, err := pre.W.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := pre.Close(); err != nil {
		t.Fatal(err)
	}
	r := newRigAt(t, p, t.TempDir(), true) // store.Open migrates 0021 to the latest
	if n := r.count(`SELECT COUNT(*) FROM worker_contract_log`); n != 0 {
		t.Fatalf("%d contract rows after the migration, want 0", n)
	}
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	if spec.Wire == provider.ContractV2 {
		t.Fatalf("a plain job was sent as /2")
	}
	// with the fake backend closed explicitly, a plain job still passes (needs [] always does)
	r.setCaps(gateDigest)
	job2 := r.seedBatch(batchSpec{}).Jobs[0]
	r.step()
	if r.state(job2) == StateQueued {
		t.Fatalf("a plain job was held by a closed gate")
	}
	if n := r.count(`SELECT COUNT(*) FROM worker_contract_log WHERE via = 'auto'`); n != 0 {
		t.Fatalf("a plain job wrote %d auto rows", n)
	}
}

// failWith drives a claimed job with needs to a Failed terminal status carrying a worker error text.
func failWith(t *testing.T, needs, spec, digest, text string, open ...string) (*rig, string) {
	t.Helper()
	r := newRig(t)
	r.setCaps(gateDigest, open...)
	job := r.seedBatch(batchSpec{needs: needs, specJSON: spec}).Jobs[0]
	r.submitOne(job)
	if digest != "" {
		r.exec(`UPDATE job_attempts SET image_digest = ? WHERE job_id = ?`, digest, job)
	}
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: text})
	r.advance(31 * time.Second)
	r.step()
	return r, job
}

func TestSettle_ContractVersionAutoCloses(t *testing.T) {
	t.Parallel()
	for _, worker := range []string{"CONTRACT_VERSION", "BAD_INPUT"} {
		t.Run(worker, func(t *testing.T) {
			t.Parallel()
			r, job := failWith(t, `["v2"]`, `{}`, gateDigest, worker+": no", "v2")
			r.wantState(job, StateFailed)
			r.wantCode(job, CodeWorkerContract)
			row := r.latestContract()
			if row.Via != provider.ContractViaAuto || len(row.Caps) != 0 || row.ChangedBy != "system:runner" || row.AttemptID != r.att(job, 1).ID || row.ImageDigest != gateDigest {
				t.Fatalf("row = %+v", row)
			}
			var titles []string
			for _, m := range r.notes.all() {
				titles = append(titles, m.Title)
			}
			if !slices.Contains(titles, "Worker capabilities closed: fake") {
				t.Fatalf("no notification: %v", titles)
			}
		})
	}
}

// A /1 job's BAD_INPUT is an ordinary worker error and never touches the gate.
func TestSettle_PlainJobBadInputLeavesGate(t *testing.T) {
	t.Parallel()
	r, job := failWith(t, `[]`, `{}`, gateDigest, "BAD_INPUT: x", "v2")
	r.wantState(job, StateFailed)
	if c := r.job(job).ErrorCode; c == CodeWorkerContract || c == CodeWorkerOutdated {
		t.Fatalf("a /1 job ended %s", c)
	}
	if n := r.count(`SELECT COUNT(*) FROM worker_contract_log WHERE via = 'auto'`); n != 0 {
		t.Fatalf("auto rows = %d", n)
	}
}

func TestSettle_KindUnavailableRemovesCap(t *testing.T) {
	t.Parallel()
	r, job := failWith(t, `["ears","v2"]`, `{"ears":{"budget_s":60,"stages":["asr"]}}`, gateDigest, "KIND_UNAVAILABLE: the ears stage is not available", "ears", "v2")
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerOutdated)
	row := r.latestContract()
	if row.Via != provider.ContractViaAuto || !slices.Equal(row.Caps, []string{"v2"}) {
		t.Fatalf("row = %+v, want auto with caps [v2]", row)
	}
	if ErrorMessage(CodeWorkerOutdated) == "" {
		t.Fatal("no fixed message for WORKER_OUTDATED")
	}
}

func TestSettle_StaleDigestChangesNothing(t *testing.T) {
	t.Parallel()
	other := "sha256:" + strings.Repeat("2", 64)
	r, job := failWith(t, `["v2"]`, `{}`, other, "CONTRACT_VERSION: no", "v2")
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerContract)
	if n := r.count(`SELECT COUNT(*) FROM worker_contract_log`); n != 1 {
		t.Fatalf("log rows = %d, want only the operator's", n)
	}
	if row := r.latestContract(); row.Via != provider.ContractViaCLI || !slices.Equal(row.Caps, []string{"v2"}) {
		t.Fatalf("an attempt on a stale image changed the gate: %+v", row)
	}
}

func TestFailQueued_WorkerOutdatedAfterAnHour(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest) // closed at t0
	b := r.seedBatch(batchSpec{needs: `["v2"]`})
	job := b.Jobs[0]
	plain := r.seedBatch(batchSpec{}).Jobs[0]
	r.advance(59 * time.Minute)
	r.step()
	r.wantState(job, StateQueued)
	if r.ledger(b.Batch, "release", "never_ran") != 0 {
		t.Fatal("released before the hour")
	}
	r.advance(2 * time.Minute)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerOutdated)
	if r.ledger(b.Batch, "release", "never_ran") != r.ledger(b.Batch, "reserve", "take") {
		t.Fatal("reservation not released")
	}
	if got := r.state(plain); got == StateFailed {
		t.Fatalf("a plain job failed with the needy one")
	}
}

func TestFailQueued_OpenCapsKeepJobQueued(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	r.exec(`UPDATE provider_state SET armed = 0`) // armed off: nothing claims, but needs are open so nothing is outdated
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.advance(3 * time.Hour)
	r.step()
	r.wantState(job, StateQueued)
}

// A capability closed a moment ago gives the queued job its own hour before it fails.
func TestFailQueued_RecentCloseGivesAnHour(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.exec(`UPDATE provider_state SET armed = 0`)
	r.advance(3 * time.Hour)
	r.setCaps(gateDigest) // closed now
	r.step()
	r.wantState(job, StateQueued)
	r.advance(61 * time.Minute)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerOutdated)
}

func TestGateOutcome_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		worker, code string
		ok           bool
	}{
		{"CONTRACT_VERSION", CodeWorkerContract, true}, {"BAD_INPUT", CodeWorkerContract, true}, {"KIND_UNAVAILABLE", CodeWorkerOutdated, true},
		{"DEADLINE", "", false}, {"TASK_UNAVAILABLE", "", false}, {"", "", false},
	} {
		if code, ok := gateOutcome(tc.worker); code != tc.code || ok != tc.ok {
			t.Errorf("gateOutcome(%q) = %q %v", tc.worker, code, ok)
		}
	}
}

// The worker's refusal may reach the runner only as the error output the transfer handler recorded; the gate closes from that too.
func TestSettle_RefusalReportedByEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.submitOne(job)
	r.exec(`INSERT INTO job_events(job_id, attempt_id, at, event, code, detail_json) VALUES (?, ?, ?, 'status', 'WORKER_ERROR', ?)`,
		job, r.att(job, 1).ID, "2026-10-15T20:34:00.000Z", `{"reason":"worker reported an error","worker_error":"CONTRACT_VERSION"}`)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000)})
	r.advance(31 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerContract)
	if row := r.latestContract(); row.Via != provider.ContractViaAuto || len(row.Caps) != 0 {
		t.Fatalf("row = %+v", row)
	}
}

func TestSettle_AutoCloseEdges(t *testing.T) {
	t.Parallel()
	t.Run("closed after the claim, a second refusal adds no row", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.setCaps(gateDigest, "v2")
		job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
		r.submitOne(job)
		r.setCaps(gateDigest) // the operator closed it meanwhile
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "CONTRACT_VERSION: no"})
		r.advance(31 * time.Second)
		r.step()
		r.wantCode(job, CodeWorkerContract)
		if n := r.count(`SELECT COUNT(*) FROM worker_contract_log`); n != 2 {
			t.Fatalf("log rows = %d, want the two operator rows", n)
		}
	})
	t.Run("a simulated backend with no row closes from every capability", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
		r.submitOne(job)
		r.exec(`UPDATE job_attempts SET image_digest = ? WHERE job_id = ?`, gateDigest, job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "BAD_INPUT: x"})
		r.advance(31 * time.Second)
		r.step()
		r.wantCode(job, CodeWorkerContract)
		if row := r.latestContract(); row.Via != provider.ContractViaAuto || len(row.Caps) != 0 || row.ImageDigest != gateDigest {
			t.Fatalf("row = %+v", row)
		}
	})
	t.Run("an unknown attempt image closes the gate under the gate's own digest", func(t *testing.T) {
		t.Parallel()
		r, job := failWith(t, `["v2"]`, `{}`, "", "CONTRACT_VERSION: no", "v2")
		r.wantCode(job, CodeWorkerContract)
		if row := r.latestContract(); row.Via != provider.ContractViaAuto || row.ImageDigest != gateDigest || len(row.Caps) != 0 {
			t.Fatalf("row = %+v", row)
		}
	})
	t.Run("a worker that lists its caps loses only what it lacks", func(t *testing.T) {
		t.Parallel()
		out := `{"contract":"mm.worker/2","take_id":"t","attempt_id":"a","status":"error","error":{"code":"KIND_UNAVAILABLE","message":"m"},"artifacts":{},` +
			`"audio_seconds":null,"truncated":null,"timings":{},"env":{},"kind":"render","caps":["v2"]}`
		r := newRig(t)
		r.setCaps(gateDigest, "ears", "plan", "v2")
		job := r.seedBatch(batchSpec{needs: `["ears","v2"]`, specJSON: `{"ears":{"budget_s":60,"stages":["asr"]}}`}).Jobs[0]
		r.submitOne(job)
		r.exec(`UPDATE job_attempts SET image_digest = ? WHERE job_id = ?`, gateDigest, job)
		r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), Output: []byte(out)})
		r.advance(31 * time.Second)
		r.step()
		r.wantCode(job, CodeWorkerOutdated)
		if row := r.latestContract(); !slices.Equal(row.Caps, []string{"v2"}) {
			t.Fatalf("row = %+v, want only v2 left", row)
		}
	})
}

// TestProviderStatus_WorkerBlock: the status read model carries the gate (C322.6).
func TestProviderStatus_WorkerBlock(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	adm := r.admin(provider.ModeFake, true)
	st, err := adm.Status(t.Context(), humanP)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.Worker.Caps, provider.Caps) || st.Worker.Via != "" || st.Worker.ChangedAt != nil {
		t.Fatalf("no row: %+v", st.Worker)
	}
	r.setCaps(gateDigest, "v2")
	st, err = adm.Status(t.Context(), humanP)
	if err != nil {
		t.Fatal(err)
	}
	if w := st.Worker; !slices.Equal(w.Caps, []string{"v2"}) || w.Via != "cli" || w.ImageDigest != gateDigest || w.ChangedAt == nil {
		t.Fatalf("with a row: %+v", w)
	}
}

// A /2 job that fails with no worker code at all is an ordinary lost worker; the gate is not touched.
func TestSettle_UncodedFailureLeavesGate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000)})
	r.advance(31 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeWorkerLost)
	if row := r.latestContract(); row.Via != provider.ContractViaCLI {
		t.Fatalf("the gate changed: %+v", row)
	}
}

// An unreadable gate log stops the step (the transaction rolls back and nothing is decided on a guess) instead of failing the job.
func TestSettle_UnreadableGateLogStopsTheStep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "CONTRACT_VERSION: no"})
	r.advance(31 * time.Second)
	r.exec(`DROP VIEW worker_contract`)
	r.exec(`ALTER TABLE worker_contract_log RENAME TO worker_contract_log_x`)
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("the step succeeded with an unreadable gate log")
	}
	if got := r.state(job); got == StateFailed {
		t.Fatalf("the job failed on a guess")
	}
}

func TestFailQueued_UnreadableGateLogStopsTheStep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest)
	r.seedBatch(batchSpec{needs: `["v2"]`})
	r.advance(2 * time.Hour)
	r.exec(`DROP VIEW worker_contract`)
	r.exec(`ALTER TABLE worker_contract_log RENAME TO worker_contract_log_x`)
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("the step succeeded with an unreadable gate log")
	}
}

// failedContractAttempt seeds a /2 job whose worker answered CONTRACT_VERSION, advanced to the point where the next Step settles it.
func failedContractAttempt(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t)
	r.setCaps(gateDigest, "v2")
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "CONTRACT_VERSION: no"})
	r.advance(31 * time.Second)
	return r, job
}

func TestSettle_AutoCloseWriteFailuresStopTheStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, ddl string }{
		{"log append", `CREATE TRIGGER no_log BEFORE INSERT ON worker_contract_log BEGIN SELECT RAISE(ABORT, 'no log'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, job := failedContractAttempt(t)
			r.exec(tc.ddl)
			if _, err := r.run.Step(t.Context()); err == nil {
				t.Fatal("the step succeeded although the auto-close could not be written")
			}
			if got := r.state(job); got == StateFailed {
				t.Fatalf("the job failed without its gate update")
			}
		})
	}
}

func TestFailQueued_UnreadableProviderStateStopsTheStep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest)
	r.seedBatch(batchSpec{needs: `["v2"]`})
	r.advance(2 * time.Hour)
	r.exec(`ALTER TABLE provider_state RENAME TO provider_state_x`)
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("the step succeeded with unreadable provider state")
	}
}
