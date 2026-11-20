package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// jobKindsMigration mirrors the constant of the internal schema_g1_jobs_test.go: the position of 0020_job_kinds.sql.
const jobKindsMigration = 20

// jobStatePaths is a legal walk from the initial state to each job state (job_transitions).
var jobStatePaths = map[string][]string{
	"awaiting_confirmation": {},
	"queued":                {},
	"submitting":            {"submitting"},
	"starting":              {"submitting", "starting"},
	"generating":            {"submitting", "starting", "generating"},
	"transferring":          {"submitting", "starting", "generating", "transferring"},
	"ingesting":             {"submitting", "starting", "generating", "transferring", "ingesting"},
	"completed":             {"submitting", "starting", "generating", "transferring", "ingesting", "completed"},
	"cancelling":            {"queued", "cancelling"},
	"cancelled":             {"queued", "cancelling", "cancelled"},
	"failing":               {"submitting", "starting", "failing"},
	"failed":                {"submitting", "starting", "failing", "failed"},
	"expired":               {"expired"},
}

// TestMigration_JobKinds: a database at 0019 holding jobs in every state (a song PR job, a stem job, a cancelled and an expired
// one among them) migrates to 0020 with every pre-existing column byte-identical, the three defaults, every index and trigger
// present, and the new checks live.
func TestMigration_JobKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, jobKindsMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	var batch, recipe string
	if err := prev.R.QueryRowContext(ctx, `SELECT batch_id, recipe_id FROM jobs ORDER BY id LIMIT 1`).Scan(&batch, &recipe); err != nil {
		t.Fatal(err)
	}
	const at = "2026-01-01T00:00:00.000Z"
	if _, err := prev.W.ExecContext(ctx, `INSERT INTO patches(id,base_track_id,base_recipe_hash,operator,ops_json,compiled_json,scope_json,diff_json,rationale,principal_kind,principal_id,principal_name,created_at)
		VALUES('p-kind',?,'h','lyric_edit','[]','{}','{}','{}','why','agent','a','a',?)`, librarytest.TrackLuna, at); err != nil {
		t.Fatal(err)
	}
	n := 2000
	for _, state := range []string{"awaiting_confirmation", "queued", "submitting", "starting", "generating", "transferring", "ingesting", "completed",
		"cancelling", "cancelled", "failing", "failed", "expired"} {
		n++
		id := "j-kind-" + state
		initial, path := "queued", jobStatePaths[state]
		if state == "awaiting_confirmation" || state == "expired" {
			initial = "awaiting_confirmation"
		}
		extra, args := "", []any{id, batch, n, recipe, initial, at, at, at}
		switch n % 3 { // a song PR job, a stem job, a plain one
		case 0:
			extra = ",patch_id"
			args = append(args, "p-kind")
		}
		op := "reroll_seed"
		if n%3 == 1 {
			op = "extract_stems"
		}
		if _, err := prev.W.ExecContext(ctx, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at`+extra+`)
			VALUES(?,?,?,'generate','yue2','fake',?,'`+op+`','{"k":"v"}','T',216,96,186,10,20,?,?,?,?`+strings.Repeat(",?", len(args)-8)+`)`, args...); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		for _, to := range path {
			q := `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`
			qargs := []any{to, at, id}
			switch to {
			case "completed":
				q, qargs = `UPDATE jobs SET state='completed', outcome='clean', track_id=?, finished_at=?, updated_at=? WHERE id=?`, []any{librarytest.TrackLuna, at, at, id}
			case "cancelled", "failed", "expired":
				q, qargs = fmt.Sprintf(`UPDATE jobs SET state='%s', finished_at=?, updated_at=? WHERE id=?`, to), []any{at, at, id}
			}
			if _, err := prev.W.ExecContext(ctx, q, qargs...); err != nil {
				t.Fatalf("%s -> %s: %v", state, to, err)
			}
		}
	}
	oldCols := scalar(t, prev.R, `SELECT group_concat(name, ', ') FROM (SELECT name FROM pragma_table_info('jobs') ORDER BY cid)`)
	before := scalar(t, prev.R, `SELECT group_concat(r, char(10)) FROM (SELECT `+concatCols(oldCols)+` AS r FROM jobs ORDER BY id)`)
	if c := scalar(t, prev.R, `SELECT COUNT(DISTINCT state) FROM jobs`); c != "13" {
		t.Fatalf("fixture holds %s states, want 13", c)
	}
	nJobs := scalar(t, prev.R, `SELECT COUNT(*) FROM jobs`)
	prev.W.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < jobKindsMigration {
		t.Fatalf("version=%d, migrations=%d", v, store.MigrationCount())
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM jobs`); got != nJobs {
		t.Errorf("%s jobs after, %s before", got, nJobs)
	}
	after := scalar(t, s.R, `SELECT group_concat(r, char(10)) FROM (SELECT `+concatCols(oldCols)+` AS r FROM jobs ORDER BY id)`)
	if before != after {
		t.Errorf("rows changed by the upgrade:\n before:\n%s\n after:\n%s", before, after)
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM jobs WHERE kind = 'generate' AND needs_json = '[]' AND spec_json = '{}'`); got != nJobs {
		t.Errorf("defaults applied to %s of %s jobs", got, nJobs)
	}
	if got := scalar(t, s.R, `SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_table_info('jobs') ORDER BY cid DESC LIMIT 2)`); got != "spec_json,needs_json" {
		t.Errorf("the new columns are not last: %s", got)
	}
	for _, ix := range []string{"jobs_queue", "jobs_lease", "jobs_batch", "jobs_track", "jobs_source", "jobs_draft", "jobs_patch"} {
		if scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='jobs'`, ix) != "1" {
			t.Errorf("index %s missing", ix)
		}
	}
	for _, tr := range []string{"jobs_no_delete", "jobs_initial_state", "jobs_frozen", "jobs_transition", "jobs_final", "pd_jobs_au", "jobs_patch_frozen"} {
		if scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=? AND tbl_name='jobs'`, tr) != "1" {
			t.Errorf("trigger %s missing", tr)
		}
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%jobs_new%'`); n != "0" {
		t.Errorf("%s schema objects still mention jobs_new", n)
	}
	// the refusals are live on the migrated table
	ins := func(id string, take int, kind string) error {
		_, err := s.W.ExecContext(ctx, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
			VALUES(?,?,?,?,'yue2','fake',?,'reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?)`, id, batch, take, kind, recipe, at, at, at)
		return err
	}
	if err := ins("j-up-plan", 3001, "plan"); err != nil {
		t.Errorf("plan job refused: %v", err)
	}
	for _, bad := range []string{"render", "analyze"} { // unknown kind; analyze without a source
		if err := ins("j-up-"+bad, 3002, bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("kind %q accepted: %v", bad, err)
		}
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE jobs SET kind = 'plan' WHERE id = 'j-up-plan'`); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Errorf("kind is not frozen: %v", err)
	}
}

// concatCols is a SQL expression joining every named column with | (NULL shown as the text NULL).
func concatCols(cols string) string {
	var parts []string
	for _, c := range strings.Split(cols, ", ") {
		parts = append(parts, fmt.Sprintf("'%s=' || quote(%s)", c, c))
	}
	return strings.Join(parts, " || '|' || ")
}
