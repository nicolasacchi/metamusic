package backup

import (
	"context"
	"path/filepath"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestRestore_DisarmsProviders (C76.7, D-M3-31): a restored database comes up disarmed with reason
// `restored`, its live attempts are `unknown` (settled by their time bound), and the disarm is audited
// next to cli:restore.
func TestRestore_DisarmsProviders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "restore"})
	for _, q := range []string{
		`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES ('fake', 1, 'human:alice', '2026-10-06T12:00:00.000Z')`,
		`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES ('runpod-serverless', 1, 'human:alice', '2026-10-06T12:00:00.000Z')`,
	} {
		if _, err := e.st.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.W.Exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
		VALUES ('att-pending', ?, 1, 'fake', 'pending', '2026-10-06T12:00:00.000Z', '2026-10-06T15:00:00.000Z', 690000, '2026-10-06T12:00:00.000Z')`, seed.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, ref, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
		VALUES ('att-submitted', ?, 2, 'fake', 'submitted', 'r1', '2026-10-06T12:00:00.000Z', '2026-10-06T15:00:00.000Z', 690000, '2026-10-06T12:00:00.000Z')`, seed.JobID); err != nil {
		t.Fatal(err)
	}
	_, _, dir := e.run(t)
	into := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(into, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var armed, restored int
	if err := st.R.QueryRow(`SELECT COALESCE(SUM(armed), 0), COUNT(*) FROM provider_state WHERE reason = 'restored' AND changed_by = 'system:cli' AND restored_at IS NOT NULL`).Scan(&armed, &restored); err != nil || armed != 0 || restored != 2 {
		t.Errorf("provider_state: armed %d, restored rows %d (%v)", armed, restored, err)
	}
	var live, unknown int
	if err := st.R.QueryRow(`SELECT COALESCE(SUM(state IN ('pending','submitted')), 0), COALESCE(SUM(state = 'unknown'), 0) FROM job_attempts`).Scan(&live, &unknown); err != nil || live != 0 || unknown != 2 {
		t.Errorf("attempts: %d live, %d unknown (%v)", live, unknown, err)
	}
	var ops []string
	rows, err := st.R.Query(`SELECT op FROM audit_log WHERE op LIKE 'cli:restore%' ORDER BY op`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var op string
		_ = rows.Scan(&op)
		ops = append(ops, op)
	}
	if len(ops) != 2 || ops[0] != "cli:restore" || ops[1] != "cli:restore-disarm" {
		t.Errorf("audit ops %v", ops)
	}
	// the source database is untouched
	var srcArmed int
	if err := e.st.R.QueryRow(`SELECT SUM(armed) FROM provider_state`).Scan(&srcArmed); err != nil || srcArmed != 2 {
		t.Errorf("source armed %d (%v)", srcArmed, err)
	}
}
