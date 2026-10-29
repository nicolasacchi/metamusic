package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l2Env is a data directory holding the L2 fixture (two Records, a Song with a Pick history) and its Song and slot ids.
func l2Env(t *testing.T) (map[string]string, librarytest.L2Info) {
	t.Helper()
	env := testEnv(t, "local")
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	fx := librarytest.FixtureL2(t, st, librarytest.FixedNow)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return env, fx
}

func l2Exec(t *testing.T, env map[string]string, queries ...string) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, q := range queries {
		if _, err := st.W.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// TestCLI_LibraryVerify_L2: a planted L2 fault makes `library verify` exit 1 and print its line; a warning is printed only.
func TestCLI_LibraryVerify_L2(t *testing.T) {
	t.Parallel()
	env, fx := l2Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "consistent") {
		t.Fatalf("clean verify: %d %q %q", code, out, errs)
	}
	// a warning: a Record published with no playable slot is printed, the exit stays 0
	l2Exec(t, env, `INSERT INTO records(id,title,cover_seed,status,created_by,created_at,updated_by,updated_at,published_by,published_at,first_published_at)
		VALUES('warn-rec','Synthetic Empty',`+coverSeedSQL("warn-rec")+`,'draft','human:op','2026-10-06T00:00:00.000Z','human:op','2026-10-06T00:00:00.000Z',NULL,NULL,NULL)`,
		`INSERT INTO record_changes(id,record_id,seq,at,kind,reason,actor,actor_kind,actor_name) VALUES('warn-created','warn-rec',1,'2026-10-06T00:00:00.000Z','created','initial','human:op','human','op')`,
		`UPDATE records SET status='published', published_by='human:op', published_at='2026-10-06T00:00:00.000Z', first_published_at='2026-10-06T00:00:00.000Z' WHERE id='warn-rec'`)
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "published_empty warn-rec") {
		t.Fatalf("warning verify: %d %q %q", code, out, errs)
	}
	// an error: a Record whose cover_seed does not match
	l2Exec(t, env, `UPDATE records SET cover_seed = cover_seed + 1 WHERE id = '`+fx.Records["R2"]+`'`)
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(out, "cover_seed "+fx.Records["R2"]) || strings.Contains(out, "consistent") {
		t.Fatalf("error verify: %d %q %q", code, out, errs)
	}
}

// coverSeedSQL is the CoverSeed of a Record with no re-rolls, computed by the package under test's own function.
func coverSeedSQL(id string) string { return strconv.FormatInt(records.CoverSeed(id, 0), 10) }

// TestCLI_LibraryReconcile_RunsSyncer: `--dry-run` reports the slots with pending events and writes nothing; a real
// reconcile runs the Syncer after the provenance Settle.
func TestCLI_LibraryReconcile_RunsSyncer(t *testing.T) {
	t.Parallel()
	env, fx := l2Env(t)
	// a Pick moves under slot R2.1 (Song S1): a new event on its Song
	l2Exec(t, env,
		`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES('cli-event','`+fx.Songs["S1"]+`',2,'2026-10-07T00:00:00.000Z','`+fx.Takes["s1a"]+`',NULL,'newest','none','emptied','system:provenance','system','provenance','{}')`,
		`UPDATE song_picks SET take_id = NULL, source = 'none', last_event_id = 'cli-event' WHERE song_id = '`+fx.Songs["S1"]+`'`)
	rows := dbRow(t, env, `SELECT COUNT(*) FROM record_changes`)
	code, out, errs := do(t, env, "", "library", "reconcile", "--dry-run")
	if code != 0 || !strings.Contains(out, "records: 1 slots with pending Pick events") || !strings.Contains(out, "nothing written") {
		t.Fatalf("dry-run: %d %q %q", code, out, errs)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM record_changes`); n != rows {
		t.Errorf("a dry run wrote changelog rows: %d -> %d", rows, n)
	}
	code, out, errs = do(t, env, "", "library", "reconcile")
	if code != 0 || !strings.Contains(out, "records synced: 1 slots") {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	// the real Reconciler ran first and may have moved the Pick again; the slot has Syncer rows either way
	if n := dbRow(t, env, `SELECT COUNT(*) FROM record_changes WHERE pick_event_id IS NOT NULL AND slot_id = '`+fx.Slots["R2.1"]+`'`); n < 1 {
		t.Errorf("syncer rows of the slot = %d", n)
	}
	code, out, _ = do(t, env, "", "library", "reconcile", "--dry-run")
	if code != 0 || !strings.Contains(out, "records: 0 slots with pending Pick events") {
		t.Errorf("after: %d %q", code, out)
	}
}

// TestCLI_LibraryVerifyFiles_Tapes: `library verify --files` checks that every ready tape's file exists with its recorded size
// and prints ids and relative paths only; without --files the file system is not looked at.
func TestCLI_LibraryVerifyFiles_Tapes(t *testing.T) {
	t.Parallel()
	env, fx := l2Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	sha := strings.Repeat("a", 64)
	plant := func(id, key, rel string, bytes int) {
		l2Exec(t, env, `INSERT INTO tapes(id,record_id,record_revision,key,state,params_json,plan_json,contains_lossy,lossy_sources,total_samples,bytes,sha256,path,filename,principal_id,created_at,finished_at)
			VALUES('`+id+`','`+fx.Records["R1"]+`',1,'`+key+`','ready','{}','{}',0,0,48000,`+strconv.Itoa(bytes)+`,'`+sha+`','`+rel+`','tape.flac','human:op','2026-10-06T00:00:00.000Z','2026-10-06T00:01:00.000Z')`)
	}
	good, short := "derived/tapes/aa/"+sha+".flac", "derived/tapes/bb/short.flac"
	for rel, data := range map[string]string{good: "0123456789", short: "01234"} {
		p := filepath.Join(env["MM_DATA_DIR"], filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	plant("tape-ok", strings.Repeat("1", 64), good, 10)
	plant("tape-short", strings.Repeat("2", 64), short, 10)
	plant("tape-gone", strings.Repeat("3", 64), "derived/tapes/cc/gone.flac", 10)
	plant("tape-escape", strings.Repeat("4", 64), "derived/tapes/../../metamusic.db", 10)
	if code, out, errs := do(t, env, "", "library", "verify"); code != 0 {
		t.Fatalf("without --files: %d %q %q", code, out, errs)
	}
	code, out, errs := do(t, env, "", "library", "verify", "--files")
	if code != 1 {
		t.Fatalf("--files: exit %d %q %q", code, out, errs)
	}
	for _, want := range []string{"tape_file_size tape-short ", "tape_file_missing tape-gone derived/tapes/cc/gone.flac", "tape_file_path tape-escape "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tape-ok") || strings.Contains(out, env["MM_DATA_DIR"]) {
		t.Errorf("a good tape or an absolute path is printed:\n%s", out)
	}
}

// TestCLI_LibraryVerify_CrateFilter: the CLI installs the Crate filter vocabulary, so a stored smart filter that no longer parses
// is a warning line (exit stays 0), the same as under serve.
func TestCLI_LibraryVerify_CrateFilter(t *testing.T) {
	t.Parallel()
	env, _ := l2Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	l2Exec(t, env, `INSERT INTO crates(id,name,kind,subject,filter_json,created_by,created_at,updated_by,updated_at)
		VALUES('crate-bad','Synthetic Stale','smart','song','{"no_such_key":true}','human:op','2026-10-06T00:00:00.000Z','human:op','2026-10-06T00:00:00.000Z')`)
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "crate_filter_invalid crate-bad") {
		t.Fatalf("verify: %d %q %q", code, out, errs)
	}
}
