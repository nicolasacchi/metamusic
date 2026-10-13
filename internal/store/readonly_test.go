package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func dirList(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// closedWAL builds a cleanly closed v2 database with one setting.
func closedWAL(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "mm.db")
	s, err := Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO settings(key,value) VALUES('k','v')`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenReadOnly_MissingDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := OpenReadOnly(context.Background(), filepath.Join(dir, "mm.db"))
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err=%v", err)
	}
	if got := dirList(t, dir); len(got) != 0 {
		t.Fatalf("directory not empty: %v", got)
	}
}

func TestOpenReadOnly_ClosedWALCreatesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := closedWAL(t)
	dir := filepath.Dir(p)
	before := dirList(t, dir)
	bytesBefore, _ := os.ReadFile(p)
	s, err := OpenReadOnly(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='k'`).Scan(&v); err != nil || v != "v" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if after := dirList(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("listing changed: %v -> %v", before, after)
	}
	if bytesAfter, _ := os.ReadFile(p); !bytes.Equal(bytesBefore, bytesAfter) {
		t.Fatal("database bytes changed")
	}
}

func TestOpenReadOnly_LiveWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w, p := open(t)
	mustExec(t, w, `INSERT INTO settings(key,value) VALUES('k','v')`)
	before := dirList(t, filepath.Dir(p))
	s, err := OpenReadOnly(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='k'`).Scan(&v); err != nil || v != "v" {
		t.Fatalf("committed row not visible: %q %v", v, err)
	}
	mustExec(t, w, `INSERT INTO settings(key,value) VALUES('k2','v2')`)
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='k2'`).Scan(&v); err != nil || v != "v2" {
		t.Fatalf("later commit not visible: %q %v", v, err)
	}
	s.Close()
	after := dirList(t, filepath.Dir(p))
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("read-only open created files: %v -> %v", before, after)
	}
}

func TestOpenReadOnly_SchemaMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p1 := filepath.Join(t.TempDir(), "v1.db")
	w, err := OpenRaw(p1)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateTo(ctx, w, 1); err != nil {
		t.Fatal(err)
	}
	w.Close()
	_, err = OpenReadOnly(ctx, p1)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("err=%v", err)
	}
	ms, lerr := loadMigrations()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if !strings.Contains(err.Error(), "version 1") || !strings.Contains(err.Error(), fmt.Sprintf("expects %d", len(ms))) {
		t.Fatalf("message must name both versions: %v", err)
	}
}

func TestOpenReadOnly_WritesFail(t *testing.T) {
	t.Parallel()
	for _, live := range []bool{false, true} {
		var p string
		if live {
			var w *Store
			w, p = open(t)
			_ = w
		} else {
			p = closedWAL(t)
		}
		s, err := OpenReadOnly(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.W.ExecContext(context.Background(), `INSERT INTO settings(key,value) VALUES('x','y')`)
		if err == nil {
			t.Fatalf("live=%v: write succeeded", live)
		}
		s.Close()
	}
}
