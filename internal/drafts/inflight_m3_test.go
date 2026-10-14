package drafts_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/drafts"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestUpdateDraft_InFlight409: a draft that a non-terminal job renders cannot be edited or archived
// (C81.1); once the job is terminal the draft is editable again. The check is SQL only.
func TestUpdateDraft_InFlight409(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "inflight"})
	if _, err := e.st.W.Exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
		SELECT 'job-draft', batch_id, 5, kind, engine, backend, recipe_id, ?, 'from_draft', '{}', 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd,
		       'queued', next_attempt_at, queued_at, created_at, updated_at FROM jobs WHERE id = ?`, lt.Draft1, seed.JobID); err != nil {
		t.Fatal(err)
	}
	patch := drafts.DraftPatch{Revision: 1, Label: str("renamed")}
	de := asErr(t, mustErr(e.svc.Update(ctx, human, lt.Draft1, patch)), 409, "DRAFT_IN_FLIGHT")
	if de.Msg == "" {
		t.Error("no message")
	}
	archived := "archived"
	asErr(t, mustErr(e.svc.Update(ctx, human, lt.Draft1, drafts.DraftPatch{Revision: 1, Status: &archived})), 409, "DRAFT_IN_FLIGHT")
	if n := count(t, e, `SELECT revision FROM recipe_drafts WHERE id = ?`, lt.Draft1); n != 1 {
		t.Errorf("a refused update changed the draft: revision %d", n)
	}
	// another draft is not affected
	if _, err := e.svc.Update(ctx, human, lt.Draft2, drafts.DraftPatch{Revision: 1, Label: str("free")}); err != nil {
		t.Errorf("another draft: %v", err)
	}
	// the job ends: the draft is editable again
	for _, to := range []string{"submitting", "starting", "failing", "failed"} {
		if _, err := e.st.W.Exec(`UPDATE jobs SET state = ?, error_code = CASE WHEN ? = 'failed' THEN 'DEADLINE' END, finished_at = CASE WHEN ? = 'failed' THEN created_at END,
			lease_owner = CASE WHEN ? IN ('failed') THEN NULL ELSE 'x' END, lease_expires_at = CASE WHEN ? IN ('failed') THEN NULL ELSE '2030-01-01T00:00:00.000Z' END WHERE id = 'job-draft'`,
			to, to, to, to, to); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	up, err := e.svc.Update(ctx, human, lt.Draft1, patch)
	if err != nil || up.Revision != 2 {
		t.Errorf("after the job ended: %+v %v", up, err)
	}
}
