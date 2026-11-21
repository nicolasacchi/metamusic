package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// workerContractMigration is the position of 0021_worker_contract.sql (G1 C322.3); renumber here if main moved.
const workerContractMigration = 21

// TestMigration_WorkerContract: a database at 0020 with jobs migrates to 0021 additively: the new table is empty, every other table
// is byte-identical, the log is append-only and its view answers the latest row per backend.
func TestMigration_WorkerContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, workerContractMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM jobs`); n == "0" {
		t.Fatal("the fixture has no jobs")
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'worker_contract_log'`); n != "0" {
		t.Fatalf("0020 already has the table")
	}
	if err := store.MigrateTo(ctx, prev.W, workerContractMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != workerContractMigration {
		t.Fatalf("user_version = %d", v)
	}
	after := dbSnapshot(t, prev.R)
	if n := len(after["worker_contract_log"]); n != 0 {
		t.Fatalf("the new table has %d rows", n)
	}
	delete(after, "worker_contract_log")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
	exec := func(q string, a ...any) error { _, err := prev.W.ExecContext(ctx, q, a...); return err }
	ins := `INSERT INTO worker_contract_log(backend,image_digest,caps_json,via,changed_by,changed_at,reason) VALUES (?,?,?,'cli','system:cli','2026-01-01T00:00:00.000Z','r')`
	if err := exec(ins, "b1", "sha256:a", `["v2"]`); err != nil {
		t.Fatal(err)
	}
	if err := exec(ins, "b1", "sha256:a", `[]`); err != nil {
		t.Fatal(err)
	}
	if err := exec(ins, "b2", "sha256:b", `["v2","ears"]`); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, prev.R, `SELECT group_concat(backend || ':' || caps_json, ' ') FROM (SELECT * FROM worker_contract ORDER BY backend)`); got != `b1:[] b2:["v2","ears"]` {
		t.Errorf("view = %s", got)
	}
	for name, err := range map[string]error{
		"caps not an array": exec(ins, "b3", "sha256:c", `{}`),
		"caps not json":     exec(ins, "b3", "sha256:c", `nope`),
		"update":            exec(`UPDATE worker_contract_log SET reason = 'x'`),
		"delete":            exec(`DELETE FROM worker_contract_log`),
		"auto no attempt":   exec(`INSERT INTO worker_contract_log(backend,image_digest,caps_json,via,changed_by,changed_at,reason) VALUES ('b','d','[]','auto','s','t','r')`),
		"cli with attempt":  exec(`INSERT INTO worker_contract_log(backend,image_digest,caps_json,via,attempt_id,changed_by,changed_at,reason) VALUES ('b','d','[]','cli','x','s','t','r')`),
		"unknown via":       exec(`INSERT INTO worker_contract_log(backend,image_digest,caps_json,via,changed_by,changed_at,reason) VALUES ('b','d','[]','api','s','t','r')`),
	} {
		if err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
