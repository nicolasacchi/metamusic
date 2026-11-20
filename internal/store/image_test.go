package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

// Applying the migrations from zero costs about 15 s per database under -race (every ALTER TABLE ... RENAME re-parses the
// whole schema, and checkptr/tsan instrument all of it), and the package opens a database per test, so the suite outgrew its
// shard budget with every migration. The migrations themselves are still run for real, but once per process: versionImage(n)
// builds the image of a database migrated to n by taking the image of n-1 and applying migration n to it (so a full
// chain costs one from-zero migration however many tests ask), and MigrateTo restores that image into a fresh database.
// TestMigrate_ImageEqualsFromZero pins that an image is the same schema as a from-zero migration.
var (
	imgMu   sync.Mutex
	imgDir  string
	imgPath = map[int]string{} // version -> path of a checkpointed single-file database migrated to that version
)

// versionImage returns the path of a database file migrated to version n; the caller must treat it as read-only.
func versionImage(ctx context.Context, n int) (string, error) {
	imgMu.Lock()
	defer imgMu.Unlock()
	return versionImageLocked(ctx, n)
}

func versionImageLocked(ctx context.Context, n int) (string, error) {
	if p, ok := imgPath[n]; ok {
		return p, nil
	}
	if imgDir == "" {
		d, err := os.MkdirTemp("", "mm-store-images-")
		if err != nil {
			return "", err
		}
		imgDir = d
	}
	p := filepath.Join(imgDir, fmt.Sprintf("v%02d.db", n))
	if n > 1 {
		prev, err := versionImageLocked(ctx, n-1)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(prev)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return "", err
		}
	}
	w, err := openRawPath(p)
	if err != nil {
		return "", err
	}
	defer w.Close()
	if err := migrateTo(ctx, w, n); err != nil {
		return "", err
	}
	if _, err := w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", err
	}
	imgPath[n] = p
	return p, nil
}

func openRawPath(p string) (*sql.DB, error) {
	w, err := sql.Open("sqlite", dsn(p, true))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	return w, nil
}

// restoreFreshFromImage restores the image of version n into db when db holds nothing yet (user_version 0, no schema).
func restoreFreshFromImage(ctx context.Context, db *sql.DB, n int) (bool, error) {
	var cur, objects int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&cur); err != nil {
		return false, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&objects); err != nil {
		return false, err
	}
	ms, err := loadMigrations()
	if err != nil {
		return false, err
	}
	if cur != 0 || objects != 0 || n > len(ms) {
		return false, nil
	}
	src, err := versionImage(ctx, n)
	if err != nil {
		return false, err
	}
	c, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer c.Close()
	err = c.Raw(func(dc any) error {
		r, ok := dc.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("driver connection %T cannot restore", dc)
		}
		// Each restore reads the shared image through its own connection: a private copy keeps concurrent readers off one WAL.
		cp, err := os.CreateTemp(imgDir, "restore-*.db")
		if err != nil {
			return err
		}
		cp.Close()
		defer os.Remove(cp.Name())
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cp.Name(), b, 0o600); err != nil {
			return err
		}
		bk, err := r.NewRestore(cp.Name())
		if err != nil {
			return err
		}
		if _, err := bk.Step(-1); err != nil {
			bk.Finish()
			return err
		}
		return bk.Finish()
	})
	return err == nil, err
}

// TestMain lets store.Open seed brand-new databases from the migrated template (template.go); the schema it holds is the one
// the real applier produces (TestMigrate_ImageEqualsFromZero), and databases that already exist are still migrated for real.
func TestMain(m *testing.M) {
	UseMigratedTemplateForTests()
	code := m.Run()
	if imgDir != "" {
		_ = os.RemoveAll(imgDir)
	}
	os.Exit(code)
}
