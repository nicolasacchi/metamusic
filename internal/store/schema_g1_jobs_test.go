package store

import (
	"strings"
	"testing"
)

// jobKindsMigration is the position of 0020_job_kinds.sql (G1 C323.1); renumber here and in upgrade_g1_jobs_test.go if main
// moved.
const jobKindsMigration = 20

// kindJob inserts a queued job with the given kind, needs, spec, source and (kind-specific) shape.
func kindJob(t *testing.T, s *Store, id string, take int, kind, needs, spec string, source any) error {
	t.Helper()
	return exec(t, s, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at,needs_json,spec_json)
		VALUES(?,'b1',?,?,'yue2','fake','r-m3a',?,'reroll_seed','{}','T',100,50,120,10,20,'queued',?,?,?,?,?)`,
		id, take, kind, source, m3ts, m3ts, m3ts, needs, spec)
}

// TestJobs_KindCheck_Table: the refusals of C323.1 (the migration's own half; the upgrade half is TestMigration_JobKinds).
func TestJobs_KindCheck_Table(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	if v, _ := s.UserVersion(t.Context()); v < jobKindsMigration {
		t.Fatalf("schema version %d predates the job kinds migration %d", v, jobKindsMigration)
	}
	m3World(t, s)
	for _, c := range []struct {
		name              string
		kind, needs, spec string
		source            any
		want              string // "" = accepted
	}{
		{"generate", "generate", "[]", "{}", nil, ""},
		{"analyze with source", "analyze", `["v2","ears"]`, `{"ears":{}}`, "t1", ""},
		{"plan", "plan", `["plan","v2"]`, `{"plan":{}}`, nil, ""},
		{"unknown kind", "render", "[]", "{}", nil, "CHECK"},
		{"empty kind", "", "[]", "{}", nil, "CHECK"},
		{"analyze without source", "analyze", "[]", "{}", nil, "CHECK"},
		{"needs not an array", "generate", `{"a":1}`, "{}", nil, "CHECK"},
		{"needs not json", "generate", `v2`, "{}", nil, "CHECK"},
		{"spec not an object", "generate", "[]", `[1]`, nil, "CHECK"},
		{"spec not json", "generate", "[]", `x`, nil, "CHECK"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := kindJob(t, s, "jk-"+strings.ReplaceAll(c.name, " ", "-"), 10+len(c.name), c.kind, c.needs, c.spec, c.source)
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			expectErr(t, err, c.want)
		})
	}
	// defaults: a job inserted the old way (no needs or spec) reads [] and {}
	mustExec(t, s, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES('jd','b1',99,'generate','yue2','fake','r-m3a','reroll_seed','{}','T',100,50,120,10,20,'queued',?,?,?)`, m3ts, m3ts, m3ts)
	var n, sp string
	if err := s.R.QueryRow(`SELECT needs_json, spec_json FROM jobs WHERE id='jd'`).Scan(&n, &sp); err != nil || n != "[]" || sp != "{}" {
		t.Errorf("defaults = %q %q %v", n, sp, err)
	}
}

// TestJobs_KindCheck_CompletionShapes walks a plan, a generate and an analyze job to completed: a plan job completes without a
// track, a generate job does not, and a plan job never holds one.
func TestJobs_KindCheck_CompletionShapes(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	walk := func(id string) {
		for _, st := range []string{"submitting", "starting", "generating", "transferring", "ingesting"} {
			mustExec(t, s, `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`, st, m3ts, id)
		}
	}
	if err := kindJob(t, s, "jp", 20, "plan", `["plan"]`, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if err := kindJob(t, s, "jg", 21, "generate", "[]", "{}", nil); err != nil {
		t.Fatal(err)
	}
	walk("jp")
	walk("jg")
	complete := func(id string, track any) error {
		return exec(t, s, `UPDATE jobs SET state='completed', outcome='clean', track_id=?, finished_at=?, updated_at=? WHERE id=?`, track, m3ts, m3ts, id)
	}
	if err := complete("jp", nil); err != nil {
		t.Errorf("a plan job could not complete without a track: %v", err)
	}
	expectErr(t, complete("jg", nil), "CHECK")
	// a plan job never carries a track
	if err := kindJob(t, s, "jp2", 22, "plan", `["plan"]`, "{}", nil); err != nil {
		t.Fatal(err)
	}
	walk("jp2")
	expectErr(t, complete("jp2", "t1"), "CHECK")
}

// TestJobs_KindFrozen: kind, needs_json and spec_json never change once the job exists.
func TestJobs_KindFrozen(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	for _, col := range []string{"kind = 'plan'", "needs_json = '[\"v2\"]'", "spec_json = '{\"a\":1}'"} {
		expectErr(t, exec(t, s, `UPDATE jobs SET `+col+` WHERE id = 'j1'`), "jobs: frozen column")
	}
}

// TestGenJobsDDL_CheckBothProfiles: every committed jobs block is the generator's current output for its profile.
func TestGenJobsDDL_CheckBothProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles scripts/gen-jobs-ddl.go")
	}
	t.Parallel()
	runGen(t, "-check-all")
}
