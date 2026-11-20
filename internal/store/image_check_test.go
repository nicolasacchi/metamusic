package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMigrate_ImageEqualsFromZero: a database restored from the per-process image (what MigrateTo gives every other test) is
// the same schema, version and settings as one migrated from zero by the real applier, for every version.
func TestMigrate_ImageEqualsFromZero(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, n := range []int{1, MigrationCount() / 2, MigrationCount()} {
		real, err := OpenRaw(filepath.Join(t.TempDir(), "real.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateTo(ctx, real, n); err != nil { // the unwrapped applier: from zero, every migration
			t.Fatal(err)
		}
		img, err := OpenRaw(filepath.Join(t.TempDir(), "img.db"))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := restoreFreshFromImage(ctx, img, n); err != nil || !ok {
			t.Fatalf("restore to %d: ok=%v err=%v", n, ok, err)
		}
		for _, q := range []string{`PRAGMA user_version`, `PRAGMA journal_mode`, `PRAGMA page_size`, `PRAGMA foreign_keys`} {
			if a, b := scalar0(t, real, q), scalar0(t, img, q); a != b {
				t.Errorf("v%d %s: from zero %q, image %q", n, q, a, b)
			}
		}
		if a, b := masterOf(t, real), masterOf(t, img); !reflect.DeepEqual(a, b) {
			t.Errorf("v%d: schema from zero and from the image differ", n)
		}
		if a, b := scalar0(t, real, `SELECT COUNT(*) FROM pragma_foreign_key_check`), scalar0(t, img, `SELECT COUNT(*) FROM pragma_foreign_key_check`); a != b {
			t.Errorf("v%d foreign_key_check %s vs %s", n, a, b)
		}
		if a, b := rowsOf(t, real), rowsOf(t, img); !reflect.DeepEqual(a, b) {
			t.Errorf("v%d: the rows the migrations seed differ between from zero and the image", n)
		}
		real.Close()
		img.Close()
	}
}

func scalar0(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRowContext(context.Background(), q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

// rowsOf dumps every row of every table (migrations seed a few), keyed by table.
func rowsOf(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	ctx := context.Background()
	names, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	names.Close()
	out := map[string][]string{}
	for _, tb := range tables {
		rs, err := db.QueryContext(ctx, `SELECT * FROM "`+tb+`"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rs.Columns()
		for rs.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = fmt.Sprint(v)
			}
			out[tb] = append(out[tb], strings.Join(parts, "|"))
		}
		rs.Close()
	}
	return out
}
