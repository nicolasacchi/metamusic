package transform_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/transform"
)

func (r *rig) writeFile(rel string, b []byte) {
	r.t.Helper()
	if err := r.data.MkdirAll(filepath.ToSlash(filepath.Dir(rel)), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := r.data.WriteFile(rel, b, 0o444); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) age(rel string, by time.Duration) {
	r.t.Helper()
	old := r.clk.now().Add(-by)
	if err := os.Chtimes(filepath.Join(r.dir, rel), old, old); err != nil {
		r.t.Fatal(err)
	}
}

// TestSweep_Startup: after a crash, Recover re-queues the expired row and Sweep removes what the crash left: stale work dirs,
// the master and take files a crash between ingest and commit claimed (named by the journal, never guessed), and the take dir
// of a transform that has no tracks row. A live attempt's dir, a done transform's take and directories that are not a
// transform's are left alone.
func TestSweep_Startup(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()

	crashed := r.queue(qspec{state: "running", tag: "crashed"})
	r.clk.add(transform.LeaseTTL + time.Second) // its lease has expired ...
	live := r.queue(qspec{state: "running", tag: "live"})
	queuedLeft := r.queue(qspec{tag: "queued-with-leftovers"})

	// the crashed attempt got as far as the freeze: master, take files and the journal exist, no tracks row
	master := "audio/masters/synthetic__0__crash.flac"
	r.writeFile(master, []byte("m"))
	r.writeFile("takes/"+crashed+"/transform.json", []byte("{}"))
	r.writeFile("takes/"+crashed+"/request.json", []byte("{}"))
	journal, _ := json.Marshal([]string{master, "takes/" + crashed + "/transform.json", "takes/" + crashed + "/request.json", "../escape", "audio/masters/a/b"})
	r.writeFile("incoming/transform-"+crashed+"/claimed.json", journal)
	r.writeFile("incoming/transform-"+crashed+"/in-0.f32", []byte("x"))
	r.writeFile("incoming/transform-"+live+"/job.json", []byte("{}"))
	r.writeFile("incoming/transform-"+queuedLeft+"/job.json", []byte("{}"))
	r.writeFile("incoming/transform-unknown-id/job.json", []byte("{}"))
	r.writeFile("incoming/verify-young/x", []byte("x"))
	r.writeFile("incoming/verify-old/x", []byte("x"))
	r.age("incoming/verify-old", 2*transform.WorkSweepAge)
	r.writeFile("incoming/ingest-someone/x", []byte("x")) // M1's own work dir: not ours
	// a transform with a take dir but no tracks row and no journal (the crash was before the journal was written)
	nojournal := r.queue(qspec{tag: "nojournal"})
	r.writeFile("takes/"+nojournal+"/request.json", []byte("{}"))
	// untouchable: a done transform's real take, a stranger's take dir, an unreferenced master of someone else
	r.writeFile("takes/"+r.fx["B"]+"/x", []byte("keep"))
	r.writeFile("takes/not-a-transform/x", []byte("keep"))
	r.writeFile("audio/masters/someone-else.flac", []byte("keep"))

	if err := r.svc.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.row(crashed).State; got != "queued" {
		t.Errorf("crashed row: %s", got)
	}
	if got := r.row(live).State; got != "running" {
		t.Errorf("live row: %s", got)
	}
	gone := []string{master, "takes/" + crashed, "incoming/transform-" + crashed, "incoming/transform-" + queuedLeft,
		"incoming/transform-unknown-id", "incoming/verify-old", "takes/" + nojournal}
	for _, p := range gone {
		if r.exists(p) {
			t.Errorf("%s survived the sweep", p)
		}
	}
	kept := []string{"incoming/transform-" + live + "/job.json", "incoming/verify-young/x", "incoming/ingest-someone/x",
		"takes/" + r.fx["B"] + "/x", "takes/not-a-transform/x", "audio/masters/someone-else.flac"}
	for _, p := range kept {
		if !r.exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	// a re-run is clean and the crashed row completes with exactly one Take
	if err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE transforms SET state = 'failed', error_code = 'X', cpu_ms = 0, finished_at = ?, owner = NULL, lease_expires_at = NULL WHERE id = ?`,
		"2026-11-03T12:00:00.000Z", live) // the live attempt ends, so the next claim is free
	if !r.runOne() { // FIFO: crashed was created first
		t.Fatal("nothing claimed")
	}
	if got := r.row(crashed).State; got != "done" {
		t.Errorf("crashed row after retry: %s", got)
	}
	if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, crashed); n != 1 {
		t.Errorf("tracks rows = %d", n)
	}
}

// TestSweep_NeverTouchesATakeWithARow: a take that was committed keeps its directory even if its work dir is still around (a
// crash after the commit, before the work dir was removed).
func TestSweep_NeverTouchesATakeWithARow(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	r.writeFile("incoming/transform-"+id+"/job.json", []byte("{}"))
	files, _ := os.ReadDir(filepath.Join(r.dir, "takes", id))
	if len(files) == 0 {
		t.Fatal("no take files to protect")
	}
	if err := r.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the stale work dir of a committed take survived")
	}
	after, _ := os.ReadDir(filepath.Join(r.dir, "takes", id))
	if len(after) != len(files) {
		t.Errorf("take files %d -> %d: the sweep touched a committed take", len(files), len(after))
	}
	var master string
	_ = r.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&master)
	if !r.exists(master) {
		t.Error("the master of a committed take was removed")
	}
}
