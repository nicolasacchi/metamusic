package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

const fkBase = `CREATE TABLE parent (id INTEGER PRIMARY KEY, label TEXT NOT NULL);
CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO parent VALUES (1,'a'),(2,'b');
INSERT INTO child VALUES (10,1),(11,2);`

// rebuildParent is the SQLite-documented rebuild: create new, copy, drop old, rename.
const rebuildParent = `CREATE TABLE parent_new (id INTEGER PRIMARY KEY, label TEXT NOT NULL CHECK (length(label) < 10));
INSERT INTO parent_new SELECT id, label FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
`

func dirRawDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	db, err := store.OpenRaw(filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, context.Background()
}

func dirApply(ctx context.Context, t *testing.T, db *sql.DB, ms ...store.TestMigration) error {
	t.Helper()
	return store.ApplyMigrations(ctx, db, ms)
}

func dirScalar(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func dirBase(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if err := dirApply(ctx, t, db, store.TestMigration{N: 1, Name: "0001_base.sql", SQL: fkBase}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrate_DirectiveFKOff(t *testing.T) {
	db, ctx := dirRawDB(t)
	dirBase(ctx, t, db)
	// Without the directive the DROP of a referenced table cascades/fails under enforcement.
	err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_rebuild.sql", SQL: "-- mm:foreign_keys=off\n" + rebuildParent})
	if err != nil {
		t.Fatal(err)
	}
	if n := dirScalar(t, db, `SELECT count(*) FROM child`); n != 2 {
		t.Fatalf("children = %d, want 2", n)
	}
	if n := dirScalar(t, db, `SELECT count(*) FROM parent`); n != 2 {
		t.Fatalf("parents = %d, want 2", n)
	}
	if v := dirScalar(t, db, `PRAGMA user_version`); v != 2 {
		t.Fatalf("user_version = %d", v)
	}
	if n := dirScalar(t, db, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Fatalf("foreign_key_check rows = %d", n)
	}
}

func TestMigrate_DirectiveForeignKeyCheckFails(t *testing.T) {
	db, ctx := dirRawDB(t)
	dirBase(ctx, t, db)
	bad := "-- mm:foreign_keys=off\n" + rebuildParent + "INSERT INTO child VALUES (12, 999);\n"
	err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_bad.sql", SQL: bad})
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") || !strings.Contains(err.Error(), "child") {
		t.Fatalf("err = %v, want foreign_key_check error naming child", err)
	}
	if v := dirScalar(t, db, `PRAGMA user_version`); v != 1 {
		t.Fatalf("user_version = %d, want 1", v)
	}
	if n := dirScalar(t, db, `SELECT count(*) FROM child`); n != 2 {
		t.Fatalf("children = %d, want 2 (rolled back)", n)
	}
	if n := dirScalar(t, db, `SELECT count(*) FROM sqlite_master WHERE name='parent_new'`); n != 0 {
		t.Fatal("parent_new survived rollback")
	}
	if fk := dirScalar(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("foreign_keys = %d after failed migration, want 1", fk)
	}
}

func TestMigrate_DirectiveRestoresPragma(t *testing.T) {
	db, ctx := dirRawDB(t)
	dirBase(ctx, t, db)
	if fk := dirScalar(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("precondition foreign_keys = %d", fk)
	}
	if err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_ok.sql", SQL: "-- mm:foreign_keys=off\n" + rebuildParent}); err != nil {
		t.Fatal(err)
	}
	if fk := dirScalar(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("foreign_keys = %d after success, want 1", fk)
	}
	if _, err := db.Exec(`INSERT INTO child VALUES (99, 12345)`); err == nil {
		t.Fatal("FK enforcement is not active after directive migration")
	}
	// SQL error inside the script also restores the pragma.
	if err := dirApply(ctx, t, db, store.TestMigration{N: 3, Name: "0003_err.sql", SQL: "-- mm:foreign_keys=off\nSELECT * FROM nope;"}); err == nil {
		t.Fatal("want error")
	}
	if fk := dirScalar(t, db, `PRAGMA foreign_keys`); fk != 1 {
		t.Fatalf("foreign_keys = %d after script error, want 1", fk)
	}
}

func TestMigrate_NoDirectiveUnchanged(t *testing.T) {
	db, ctx := dirRawDB(t)
	dirBase(ctx, t, db)
	// Enforcement stays on inside a plain migration: a violating insert fails the migration.
	err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_plain.sql", SQL: "INSERT INTO child VALUES (12, 999);"})
	if err == nil {
		t.Fatal("plain migration ran with FK enforcement off")
	}
	if v := dirScalar(t, db, `PRAGMA user_version`); v != 1 {
		t.Fatalf("user_version = %d", v)
	}
	if err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_plain.sql", SQL: "CREATE TABLE extra (id INTEGER);"}); err != nil {
		t.Fatal(err)
	}
	if v := dirScalar(t, db, `PRAGMA user_version`); v != 2 {
		t.Fatalf("user_version = %d", v)
	}
}

func TestMigrate_DirectiveOnlyFirstLine(t *testing.T) {
	for name, sqlText := range map[string]string{
		"second line":   "-- header\n-- mm:foreign_keys=off\n" + rebuildParent,
		"leading blank": "\n-- mm:foreign_keys=off\n" + rebuildParent,
		"trailing text": "-- mm:foreign_keys=off please\n" + rebuildParent,
		"inline":        rebuildParent + "-- mm:foreign_keys=off\n",
	} {
		t.Run(name, func(t *testing.T) {
			db, ctx := dirRawDB(t)
			dirBase(ctx, t, db)
			// Directive not honoured: the rebuild runs with enforcement on, so DROP of a referenced
			// table with children fails the implicit delete check at commit.
			if err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_x.sql", SQL: sqlText}); err == nil {
				t.Fatal("directive honoured outside the first line")
			}
		})
	}
}

func TestMigrate_RenameGuard(t *testing.T) {
	db, ctx := dirRawDB(t)
	dirBase(ctx, t, db)
	// Wrong order: renaming the old table first makes SQLite rewrite child's REFERENCES to parent_new...
	// here the script leaves a view that still names a _new table.
	sqlText := "-- mm:foreign_keys=off\n" + rebuildParent +
		"CREATE TABLE ghost (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent_new(id));\n"
	err := dirApply(ctx, t, db, store.TestMigration{N: 2, Name: "0002_guard.sql", SQL: sqlText})
	if err == nil || !strings.Contains(err.Error(), "rename guard") || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want rename guard naming ghost", err)
	}
	if v := dirScalar(t, db, `PRAGMA user_version`); v != 1 {
		t.Fatalf("user_version = %d", v)
	}
}
