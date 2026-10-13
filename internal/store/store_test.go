package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mm.db")
	s, err := Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestOpenMigratesAndReopens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	want := len(ms)
	s, p := open(t)
	if v, err := s.UserVersion(ctx); err != nil || v != want {
		t.Fatalf("version=%d err=%v", v, err)
	}
	for _, tbl := range []string{"settings", "api_tokens", "human_account", "human_sessions"} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('k','v')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, _ := s2.UserVersion(ctx); v != want {
		t.Fatalf("version after reopen=%d", v)
	}
	var val string
	if err := s2.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='k'`).Scan(&val); err != nil || val != "v" {
		t.Fatalf("data lost: %v %q", err, val)
	}
}

func TestPragmas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	check := func(label string, q string, want any) {
		var got any
		for _, d := range []struct {
			n string
			f func() error
		}{
			{"W", func() error { return s.W.QueryRowContext(ctx, q).Scan(&got) }},
			{"R", func() error { return s.R.QueryRowContext(ctx, q).Scan(&got) }},
		} {
			if err := d.f(); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s %s: got %v want %v", d.n, label, got, want)
			}
		}
	}
	check("journal_mode", `PRAGMA journal_mode`, "wal")
	check("foreign_keys", `PRAGMA foreign_keys`, int64(1))
	check("busy_timeout", `PRAGMA busy_timeout`, int64(5000))
	check("synchronous", `PRAGMA synchronous`, int64(1))
	if got := s.W.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer max conns=%d", got)
	}
}

func TestConcurrentReadsWhileWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := s.W.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('k', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, i); err != nil {
				errs <- err
				return
			}
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				var n int
				if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM settings`).Scan(&n); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
