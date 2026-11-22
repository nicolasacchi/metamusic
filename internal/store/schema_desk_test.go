package store

import (
	"strings"
	"testing"
)

// deskMigration is the position of 0026_desk.sql (M5b DESK-S1, C357.1); renumber it with the file when main moves.
const deskMigration = 26

const deskSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// deskPlan inserts a desk_plans row; mut edits the column list/values before the insert.
func deskPlan(t *testing.T, s *Store, id, source string, mut func(m map[string]any)) error {
	t.Helper()
	m := map[string]any{
		"id": id, "source": source, "engine": "yue2", "request_json": `{"id":"r","seed":1}`, "seed": 1, "cot": "full",
		"score_path": "plans/" + id + "/score.abc", "score_sha256": deskSHA, "score_bytes": 100, "dialect": "yue2", "bars": 8, "meter": "4/4",
		"aligned": 1, "sections_json": "[]", "expected_audio_s": 60, "title": "Plan", "principal_id": "human:alice", "principal_kind": "human",
		"principal_name": "alice", "created_at": m3ts,
	}
	switch source {
	case "job":
		m["job_id"], m["seed_index"] = "j1", 0
	case "take":
		m["track_id"] = "t1"
	case "edit":
		m["parent_plan_id"], m["edit_json"] = "p-take", `{"ops":[],"rationale":null}`
	}
	if mut != nil {
		mut(m)
	}
	var cols, ph []string
	var args []any
	for k, v := range m {
		if v == nil {
			continue
		}
		cols, ph, args = append(cols, k), append(ph, "?"), append(args, v)
	}
	return exec(t, s, `INSERT INTO desk_plans(`+strings.Join(cols, ",")+`) VALUES(`+strings.Join(ph, ",")+`)`, args...)
}

// TestMigration_Desk pins C357.1: the tables, indexes and triggers exist, and each refusal of the contract fires.
func TestMigration_Desk(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	if v, _ := s.UserVersion(t.Context()); v != MigrationCount() || v < deskMigration {
		t.Fatalf("version %d, migrations %d, desk %d", v, MigrationCount(), deskMigration)
	}
	for _, name := range []string{
		"desk_plans", "desk_plan_files", "desk_plan_verdicts", "desk_auditions", "desk_plans_job_seed", "desk_plans_take", "desk_plans_edit",
		"desk_plans_base", "desk_auditions_plan", "desk_auditions_principal", "jobs_desk_plan", "desk_plans_immutable", "desk_plans_no_delete",
		"desk_plan_files_immutable", "desk_plan_files_no_delete", "desk_plan_verdicts_no_delete", "desk_auditions_immutable", "desk_auditions_no_delete",
	} {
		if n := scalar0(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = '`+name+`'`); n != "1" {
			t.Errorf("%s missing from the schema", name)
		}
	}
	m3World(t, s)
	if err := kindJob(t, s, "jp", 20, "plan", `["plan","v2"]`, `{"plan":{}}`, nil); err != nil {
		t.Fatal(err)
	}
	if err := deskPlan(t, s, "p-take", "take", nil); err != nil {
		t.Fatalf("a take plan: %v", err)
	}
	if err := deskPlan(t, s, "p-job", "job", func(m map[string]any) { m["job_id"] = "jp" }); err != nil {
		t.Fatalf("a job plan: %v", err)
	}
	if err := deskPlan(t, s, "p-edit", "edit", nil); err != nil {
		t.Fatalf("an edit plan: %v", err)
	}
	for _, c := range []struct {
		name, source string
		mut          func(m map[string]any)
		want         string
	}{
		{"job plan without a job", "job", func(m map[string]any) { m["job_id"] = nil }, "CHECK"},
		{"job plan without a seed index", "job", func(m map[string]any) { m["job_id"] = "jp"; m["seed_index"] = nil }, "CHECK"},
		{"take plan with a job", "take", func(m map[string]any) { m["job_id"], m["seed_index"] = "jp", 1 }, "CHECK"},
		{"take plan without a take", "take", func(m map[string]any) { m["track_id"] = nil }, "CHECK"},
		{"edit plan without a parent", "edit", func(m map[string]any) { m["parent_plan_id"] = nil }, "CHECK"},
		{"edit plan without edit_json", "edit", func(m map[string]any) { m["edit_json"] = nil }, "CHECK"},
		{"edit plan of an unknown parent", "edit", func(m map[string]any) { m["parent_plan_id"] = "nope" }, "FOREIGN KEY"},
		{"score over 64 KiB", "edit", func(m map[string]any) { m["score_bytes"] = 65537; m["score_sha256"] = strings.Repeat("a", 64) }, "CHECK"},
		{"empty score", "edit", func(m map[string]any) { m["score_bytes"] = 0 }, "CHECK"},
		{"bad cot", "edit", func(m map[string]any) { m["cot"] = "off" }, "CHECK"},
		{"bad engine", "edit", func(m map[string]any) { m["engine"] = "acestep15" }, "CHECK"},
		{"bad dialect", "edit", func(m map[string]any) { m["dialect"] = "abc2" }, "CHECK"},
		{"short sha", "edit", func(m map[string]any) { m["score_sha256"] = "abc" }, "CHECK"},
		{"expected audio over 600 s", "edit", func(m map[string]any) { m["expected_audio_s"] = 601 }, "CHECK"},
		{"seed index over 15", "job", func(m map[string]any) { m["job_id"] = "jp"; m["seed_index"] = 16 }, "CHECK"},
		{"bad request json", "edit", func(m map[string]any) { m["request_json"] = "{" }, "CHECK"},
		{"bad principal kind", "edit", func(m map[string]any) { m["principal_kind"] = "robot" }, "CHECK"},
		{"a second take plan for one take", "take", nil, "UNIQUE"},
		{"a duplicate edit", "edit", nil, "UNIQUE"},
		{"a duplicate job seed", "job", func(m map[string]any) { m["job_id"] = "jp" }, "UNIQUE"},
	} {
		err := deskPlan(t, s, "x-"+strings.ReplaceAll(c.name, " ", "-"), c.source, c.mut)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	// a second, different edit of the same parent is allowed
	if err := deskPlan(t, s, "p-edit2", "edit", func(m map[string]any) { m["score_sha256"] = strings.Repeat("b", 64) }); err != nil {
		t.Errorf("a second distinct edit: %v", err)
	}
	expectErr(t, exec(t, s, `UPDATE desk_plans SET title = 'x' WHERE id = 'p-take'`), "desk_plans: immutable")
	expectErr(t, exec(t, s, `DELETE FROM desk_plans`), "desk_plans: no physical delete")

	file := func(plan, name string, bytes int) error {
		return exec(t, s, `INSERT INTO desk_plan_files(plan_id,name,sha256,bytes,path,created_at) VALUES(?,?,?,?,?,?)`, plan, name, deskSHA, bytes, "plans/"+plan+"/"+name, m3ts)
	}
	if err := file("p-job", "plan.json", 10); err != nil {
		t.Fatal(err)
	}
	expectErr(t, file("p-job", "plan.json", 10), "UNIQUE")
	expectErr(t, file("p-job", "big", 2097153), "CHECK")
	expectErr(t, file("p-job", "zero", 0), "CHECK")
	expectErr(t, file("nope", "x", 1), "FOREIGN KEY")
	expectErr(t, exec(t, s, `UPDATE desk_plan_files SET bytes = 5`), "desk_plan_files: immutable")
	expectErr(t, exec(t, s, `DELETE FROM desk_plan_files`), "desk_plan_files: no physical delete")

	verdict := func(rater string, v, note any) error {
		return exec(t, s, `INSERT INTO desk_plan_verdicts(plan_id,rater,verdict,note,decided_by,decided_at) VALUES('p-take',?,?,?,'human:alice',?)`, rater, v, note, m3ts)
	}
	if err := verdict("owner", "liked", "nice"); err != nil {
		t.Fatal(err)
	}
	if err := verdict("critic", nil, nil); err != nil {
		t.Fatalf("a cleared verdict: %v", err)
	}
	expectErr(t, verdict("owner", "liked", nil), "UNIQUE")
	expectErr(t, verdict("human", "liked", nil), "CHECK")
	expectErr(t, verdict("owner", "meh", nil), "CHECK")
	mustExec(t, s, `UPDATE desk_plan_verdicts SET verdict = 'rejected', note = 'no' WHERE rater = 'owner'`)
	expectErr(t, exec(t, s, `UPDATE desk_plan_verdicts SET note = '' WHERE rater = 'owner'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE desk_plan_verdicts SET note = ? WHERE rater = 'owner'`, strings.Repeat("n", 2001)), "CHECK")
	expectErr(t, exec(t, s, `DELETE FROM desk_plan_verdicts`), "desk_plan_verdicts: no physical delete")

	aud := func(id, spec, mp3 string, secs float64, chords string) error {
		return exec(t, s, `INSERT INTO desk_auditions(id,plan_id,spec_hash,soundfont,soundfont_sha256,synth_version,chords,mp3_path,mp3_sha256,mp3_bytes,pcm_sha256,seconds,cpu_ms,report_json,principal_id,created_at)
			VALUES(?,'p-take',?,'gm',?,'2.3',?,'derived/auditions/ab/x.mp3',?,10,?,?,5,'{}','human:alice',?)`, id, spec, deskSHA, chords, mp3, deskSHA, secs, m3ts)
	}
	h1, h2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	if err := aud("a1", h1, h1, 12.5, "block"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, aud("a2", h1, h2, 12.5, "block"), "UNIQUE")
	expectErr(t, aud("a2", h2, h1, 12.5, "block"), "UNIQUE")
	expectErr(t, aud("a2", "short", h2, 12.5, "block"), "CHECK")
	expectErr(t, aud("a2", h2, h2, 0, "block"), "CHECK")
	expectErr(t, aud("a2", h2, h2, 1, "triad"), "CHECK")
	expectErr(t, exec(t, s, `UPDATE desk_auditions SET seconds = 1`), "desk_auditions: immutable")
	expectErr(t, exec(t, s, `DELETE FROM desk_auditions`), "desk_auditions: no physical delete")
}
