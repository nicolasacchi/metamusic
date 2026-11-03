package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// enginesMigration is the position of 0015_engines.sql (the single schema-number constant of these tests; renumber here
// and in upgrade_e2_test.go if another milestone's migration merged first).
const enginesMigration = 15

var ws = regexp.MustCompile(`\s+`)

func normSQL(s string) string {
	s = strings.ReplaceAll(s, `"jobs"`, "jobs")
	return strings.TrimSpace(ws.ReplaceAllString(s, " "))
}

// masterAt returns name -> normalised sql of every schema object of a database migrated to version.
func masterAt(t *testing.T, version int) map[string]string {
	t.Helper()
	raw, err := OpenRaw(filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := MigrateTo(context.Background(), raw, version); err != nil {
		t.Fatal(err)
	}
	return masterOf(t, raw)
}

func masterOf(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type||':'||name, COALESCE(sql,'') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = normSQL(v)
	}
	return out
}

func TestSchema_E2_Tables(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	for _, tb := range []string{"engine_rtf", "engine_adoption_log"} {
		if n := count(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, tb); n != 1 {
			t.Errorf("table %s missing", tb)
		}
	}
	if n := count(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='view' AND name='engine_adoption'`); n != 1 {
		t.Error("view engine_adoption missing")
	}
	if v, _ := s.UserVersion(context.Background()); v != MigrationCount() || v < enginesMigration {
		t.Errorf("version %d, migrations %d", v, MigrationCount())
	}
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.R.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestMigration_JobsRebuild_OnlyOneCheckChanged diffs the schema just before the engines migration with the schema just
// after it: the jobs table differs in exactly the widened CHECK, every other object is byte-equal after whitespace
// normalisation (indexes and triggers on jobs included), and the only new objects are the E2 ones.
func TestMigration_JobsRebuild_OnlyOneCheckChanged(t *testing.T) {
	t.Parallel()
	before, after := masterAt(t, enginesMigration-1), masterAt(t, enginesMigration)
	wantJobs := strings.Replace(before["table:jobs"], "expected_audio_s BETWEEN 1 AND 360", "expected_audio_s BETWEEN 1 AND 600", 1)
	if wantJobs == before["table:jobs"] || wantJobs != after["table:jobs"] {
		t.Errorf("jobs DDL differs in more than the one CHECK:\n want %s\n  got %s", wantJobs, after["table:jobs"])
	}
	for k, v := range before {
		if k == "table:jobs" {
			continue
		}
		if after[k] != v {
			t.Errorf("%s changed:\n before %s\n  after %s", k, v, after[k])
		}
	}
	added := map[string]bool{}
	for k := range after {
		if _, ok := before[k]; !ok {
			added[k] = true
		}
	}
	for _, k := range []string{"table:engine_rtf", "trigger:engine_rtf_no_delete", "table:engine_adoption_log",
		"index:engine_adoption_log_engine", "trigger:engine_adoption_log_no_update", "trigger:engine_adoption_log_no_delete",
		"view:engine_adoption", "table:sqlite_sequence"} {
		if !added[k] {
			t.Errorf("%s not added", k)
		}
		delete(added, k)
	}
	for k := range added {
		if !strings.HasPrefix(k, "index:sqlite_autoindex_engine_") {
			t.Errorf("unexpected new object %s", k)
		}
	}
	if strings.Contains(strings.Join(mapValues(after), "\n"), "jobs_new") {
		t.Error("sqlite_master mentions jobs_new")
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// TestMigration_JobsRebuild_ExplicitColumns: the generated block names every column of the jobs table in order and never
// SELECTs *; and it is the generator's current output.
func TestMigration_JobsRebuild_ExplicitColumns(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("migrations/0015_engines.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.HasPrefix(src, "-- mm:foreign_keys=off\n") {
		t.Error("the directive is not the exact first line")
	}
	i, j := strings.Index(src, "-- BEGIN GENERATED jobs rebuild"), strings.Index(src, "-- END GENERATED jobs rebuild")
	if i < 0 || j < i {
		t.Fatal("generated block markers missing")
	}
	block := src[i:j]
	if regexp.MustCompile(`(?i)select\s+\*`).MatchString(block) {
		t.Error("the generated block uses SELECT *")
	}
	m := regexp.MustCompile(`INSERT INTO jobs_new \(([^)]*)\)\s+SELECT ([^;]*?) FROM jobs;`).FindStringSubmatch(block)
	if m == nil || m[1] != m[2] {
		t.Fatalf("INSERT ... SELECT missing or the two column lists differ: %v", m)
	}
	s, _ := open(t)
	rows, err := s.R.Query(`SELECT name FROM pragma_table_info('jobs') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if got := strings.Join(cols, ", "); got != m[1] {
		t.Errorf("columns of the head jobs table:\n %s\nin the migration:\n %s", got, m[1])
	}
}

func e2Job(s *Store, t *testing.T, id string, take int, audioS int) error {
	t.Helper()
	return exec(t, s, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES(?,'b1',?,'generate','acestep15','fake','r-m3a','reroll_seed','{}','T',?,50,120,10,20,'queued',?,?,?)`,
		id, take, audioS, m3ts, m3ts, m3ts)
}

func TestMigration_JobsAccepts600(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	if err := e2Job(s, t, "j600", 1, 600); err != nil {
		t.Errorf("600 s refused: %v", err)
	}
	if err := e2Job(s, t, "j360", 2, 360); err != nil {
		t.Errorf("360 s refused: %v", err)
	}
	if err := e2Job(s, t, "j601", 3, 601); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("601 s accepted: %v", err)
	}
	if err := e2Job(s, t, "j0", 4, 0); err == nil {
		t.Error("0 s accepted")
	}
}

func TestEngineAdoption_SeededYue2(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	var engine, state, via, by, at string
	var reason sql.NullString
	if err := s.R.QueryRow(`SELECT engine,state,via,changed_by,changed_at,reason FROM engine_adoption`).Scan(&engine, &state, &via, &by, &at, &reason); err != nil {
		t.Fatal(err)
	}
	if engine != "yue2" || state != "adopted" || via != "seed" || by != "system:default" || at != "1970-01-01T00:00:00.000Z" || reason.Valid {
		t.Errorf("seed row %s %s %s %s %s %v", engine, state, via, by, at, reason)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM engine_adoption_log`); n != 1 {
		t.Errorf("log rows = %d, want only the yue2 seed", n)
	}
}

func TestEngineAdoptionLog_AppendOnly(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	expectErr(t, exec(t, s, `UPDATE engine_adoption_log SET state='not_adopted'`), "append-only")
	expectErr(t, exec(t, s, `DELETE FROM engine_adoption_log`), "no physical delete")
}

func adoptRow(t *testing.T, s *Store, state, via, reason string) error {
	t.Helper()
	var r, trial any
	if reason != "" {
		r = reason
	}
	if via == "trial" {
		trial = "trial-1"
	}
	return exec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,trial_id,env_ids,changed_by,changed_at,reason) VALUES('acestep15',?,?,?,'["env1"]','human:h',?,?)`,
		state, via, trial, m3ts, r)
}

func TestEngineAdoption_ReadoptionKeepsHistory(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	for _, st := range []string{"adopted", "not_adopted", "adopted"} {
		mustExec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,env_ids,changed_by,changed_at,reason) VALUES('acestep15',?,'cli','[]','human:h',?,'why')`, st, m3ts)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM engine_adoption_log WHERE engine='acestep15'`); n != 3 {
		t.Errorf("log rows = %d, want 3", n)
	}
	var state string
	if err := s.R.QueryRow(`SELECT state FROM engine_adoption WHERE engine='acestep15'`).Scan(&state); err != nil || state != "adopted" {
		t.Errorf("view state %q %v", state, err)
	}
	mustExec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,env_ids,changed_by,changed_at,reason) VALUES('acestep15','not_adopted','cli','[]','human:h',?,'later')`, m3ts)
	if err := s.R.QueryRow(`SELECT state FROM engine_adoption WHERE engine='acestep15'`).Scan(&state); err != nil || state != "not_adopted" {
		t.Errorf("view state %q %v", state, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM engine_adoption`); n != 2 {
		t.Errorf("view rows = %d, want one per engine", n)
	}
}

func TestEngineAdoptionLog_Checks(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	expectErr(t, adoptRow(t, s, "adopted", "cli", ""), "CHECK")                                                                                                         // cli needs a reason
	expectErr(t, exec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,env_ids,changed_by,changed_at) VALUES('x','adopted','trial','[]','h',?)`, m3ts), "CHECK") // trial without trial id
	if err := adoptRow(t, s, "adopted", "trial", ""); err != nil {
		t.Errorf("a trial row with its trial id: %v", err)
	}
	expectErr(t, adoptRow(t, s, "maybe", "cli", "why"), "CHECK")                                                                                                                    // state enum
	expectErr(t, adoptRow(t, s, "adopted", "other", "why"), "CHECK")                                                                                                                // via enum
	expectErr(t, exec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,env_ids,changed_by,changed_at,reason) VALUES('x','adopted','cli','{}','h',?,'why')`, m3ts), "CHECK")  // env_ids array
	expectErr(t, exec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,trial_id,env_ids,changed_by,changed_at) VALUES('x','adopted','seed','t','[]','h',?)`, m3ts), "CHECK") // trial id without via=trial
	if err := adoptRow(t, s, "adopted", "cli", "approved after review"); err != nil {
		t.Error(err)
	}
}

func TestEngineRTF_NoSeedRowsInMigration(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	if n := count(t, s, `SELECT COUNT(*) FROM engine_rtf`); n != 0 {
		t.Errorf("engine_rtf has %d rows after migration; seeds live in a committed file", n)
	}
	b, err := os.ReadFile("migrations/0015_engines.sql")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)INSERT INTO engine_rtf`).Match(b) {
		t.Error("the migration seeds engine_rtf")
	}
}

func TestEngineRTF_ProvenanceCheck(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	ins := func(by, canary any, rtf int) error {
		return exec(t, s, `INSERT INTO engine_rtf(engine,task_class,gpu_class,rtf_milli,fixed_s,samples,updated_at,updated_by,canary_id)
			VALUES('acestep15','turbo',?,?,10,1,?,?,?)`, "g"+itoa(rtf)+"-"+asStr(by)+asStr(canary), rtf, m3ts, by, canary)
	}
	if err := ins("job:j1", nil, 150); err != nil {
		t.Errorf("job row: %v", err)
	}
	if err := ins("canary:c1", "c1", 151); err != nil {
		t.Errorf("canary row: %v", err)
	}
	expectErr(t, ins(nil, nil, 152), "NOT NULL")
	expectErr(t, ins("canary:c1", nil, 153), "CHECK") // canary without canary_id
	expectErr(t, ins("job:j1", "c1", 154), "CHECK")   // canary_id without canary
	expectErr(t, ins("someone", nil, 155), "CHECK")   // unknown provenance
	expectErr(t, ins("job:", nil, 156), "CHECK")      // empty id
	expectErr(t, ins("job:j1", nil, 4), "CHECK")      // rtf below range
	expectErr(t, ins("job:j1", nil, 5001), "CHECK")   // rtf above range
	expectErr(t, exec(t, s, `DELETE FROM engine_rtf`), "no physical delete")
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "-"
}

// TestTriggers_E2: every no-delete, append-only and carried-over jobs trigger of the migration, one row each.
func TestTriggers_E2(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	mustExec(t, s, `INSERT INTO engine_rtf(engine,task_class,gpu_class,rtf_milli,fixed_s,samples,updated_at,updated_by) VALUES('acestep15','turbo','g',150,10,1,?,'job:j1')`, m3ts)
	mustExec(t, s, `INSERT INTO engine_adoption_log(engine,state,via,env_ids,changed_by,changed_at,reason) VALUES('acestep15','adopted','cli','[]','human:h',?,'why')`, m3ts)
	if err := e2Job(s, t, "jt", 1, 600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, q, want string }{
		{"engine_rtf delete", `DELETE FROM engine_rtf`, "engine_rtf: no physical delete"},
		{"adoption update", `UPDATE engine_adoption_log SET reason='x'`, "engine_adoption_log: append-only"},
		{"adoption delete", `DELETE FROM engine_adoption_log`, "engine_adoption_log: no physical delete"},
		{"jobs delete", `DELETE FROM jobs`, "jobs: no physical delete"},
		{"jobs initial state", `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
			VALUES('jg','b1',9,'generate','yue2','fake','r-m3a','reroll_seed','{}','T',100,50,120,10,20,'generating','` + m3ts + `','` + m3ts + `','` + m3ts + `')`, "illegal initial state"},
		{"jobs frozen", `UPDATE jobs SET expected_audio_s = 100 WHERE id='jt'`, "jobs: frozen column"},
		{"jobs transition", `UPDATE jobs SET state='completed' WHERE id='jt'`, "jobs: illegal transition"},
		{"jobs patch frozen", `UPDATE jobs SET patch_id='p' WHERE id='jt'`, "jobs: frozen column"},
	} {
		t.Run(c.name, func(t *testing.T) { expectErr(t, exec(t, s, c.q), c.want) })
	}
	// jobs_final and the provenance trigger survive the rebuild
	for _, tr := range []string{"jobs_final", "pd_jobs_au", "jobs_patch_frozen", "jobs_transition", "jobs_frozen", "jobs_initial_state", "jobs_no_delete"} {
		if n := count(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=? AND tbl_name='jobs'`, tr); n != 1 {
			t.Errorf("trigger %s missing after the rebuild", tr)
		}
	}
	for _, ix := range []string{"jobs_queue", "jobs_lease", "jobs_batch", "jobs_track", "jobs_source", "jobs_draft", "jobs_patch"} {
		if n := count(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='jobs'`, ix); n != 1 {
			t.Errorf("index %s missing after the rebuild", ix)
		}
	}
}
