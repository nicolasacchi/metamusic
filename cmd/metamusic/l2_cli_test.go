package main

import (
	"context"
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
