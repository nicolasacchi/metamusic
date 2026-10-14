package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Restore errors. The CLI maps ErrTargetNotEmpty, ErrNewerSchema and
// ErrBadSnapshot to exit 2 and *VerifyError to exit 1.
var (
	ErrTargetNotEmpty = errors.New("the target directory exists and is not empty")
	ErrNewerSchema    = errors.New("the snapshot's database schema is newer than this binary")
	ErrBadSnapshot    = errors.New("not a usable snapshot")
)

// VerifyError lists snapshot files that are missing or whose sha256 differs.
type VerifyError struct{ Paths []string }

func (e *VerifyError) Error() string {
	return fmt.Sprintf("snapshot verification failed for %d file(s): %s", len(e.Paths), strings.Join(e.Paths, ", "))
}

// RestoreOptions tunes Restore.
type RestoreOptions struct {
	Link    func(oldname, newname string) error
	Now     func() time.Time
	NewID   func() string
	Version string
}

// RestoreReport is the outcome of Restore.
type RestoreReport struct {
	Files, Linked, Copied int
	SchemaFrom, SchemaTo  int
	Counts                map[string]int
}

// binarySchema is the schema version this binary creates (the number of
// embedded migrations), found by migrating a scratch database.
func binarySchema(ctx context.Context) (int, error) {
	dir, err := os.MkdirTemp("", "mm-schema-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := store.Open(ctx, filepath.Join(dir, "s.db"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()
	return st.UserVersion(ctx)
}

// Restore rebuilds a data directory from a snapshot (CLI only).
func Restore(ctx context.Context, from, into string, o RestoreOptions) (rep RestoreReport, err error) {
	if o.Link == nil {
		o.Link = os.Link
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	// 1. the target must not exist or be empty
	existed := false
	if st, serr := os.Stat(into); serr == nil {
		if !st.IsDir() {
			return rep, fmt.Errorf("%w: %s is not a directory", ErrTargetNotEmpty, filepath.Base(into))
		}
		ents, rerr := os.ReadDir(into)
		if rerr != nil {
			return rep, rerr
		}
		if len(ents) > 0 {
			return rep, ErrTargetNotEmpty
		}
		existed = true
	} else if !errors.Is(serr, os.ErrNotExist) {
		return rep, serr
	}

	// 2. manifest, schema and checksums, before anything is written
	man, err := readManifest(from)
	if err != nil {
		return rep, fmt.Errorf("%w: %w", ErrBadSnapshot, err)
	}
	if err := checkManifestPaths(man); err != nil {
		return rep, fmt.Errorf("%w: %w", ErrBadSnapshot, err)
	}
	want, err := binarySchema(ctx)
	if err != nil {
		return rep, err
	}
	if man.SchemaVersion > want {
		return rep, fmt.Errorf("%w (snapshot %d, binary %d)", ErrNewerSchema, man.SchemaVersion, want)
	}
	root, err := os.OpenRoot(from)
	if err != nil {
		return rep, fmt.Errorf("%w: %w", ErrBadSnapshot, unwrapPath(err))
	}
	defer func() { _ = root.Close() }()
	if bad := verifySnapshot(ctx, root, man); len(bad) > 0 {
		return rep, &VerifyError{Paths: bad}
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}

	// 3. write
	if err := os.MkdirAll(into, 0o750); err != nil {
		return rep, err
	}
	defer func() {
		if err == nil {
			return
		}
		if existed {
			ents, _ := os.ReadDir(into)
			for _, e := range ents {
				_ = os.RemoveAll(filepath.Join(into, e.Name()))
			}
		} else {
			_ = os.RemoveAll(into)
		}
	}()
	dbDst := filepath.Join(into, "metamusic.db")
	if err := copyVerified(root, man.DB.Path, dbDst, man.DB.SHA256, 0o600); err != nil {
		return rep, err
	}
	for _, f := range man.Files {
		if f.Kind == "export" {
			continue // regenerable; not part of the data directory
		}
		dst := filepath.Join(into, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return rep, err
		}
		rep.Files++
		if f.Kind == "import" {
			if err := copyVerified(root, f.Path, dst, f.SHA256, 0o600); err != nil {
				return rep, err
			}
			rep.Copied++
			continue
		}
		if o.Link(filepath.Join(from, filepath.FromSlash(f.Path)), dst) == nil {
			rep.Linked++
			continue
		}
		if err := copyVerified(root, f.Path, dst, f.SHA256, 0o444); err != nil {
			return rep, err
		}
		rep.Copied++
	}
	for _, d := range []string{"import", "import/reports"} {
		if _, serr := os.Stat(filepath.Join(into, filepath.FromSlash(d))); serr == nil {
			if err := os.Chmod(filepath.Join(into, filepath.FromSlash(d)), 0o700); err != nil { //nolint:gosec // private directory, 0700
				return rep, err
			}
		}
	}

	// 4. open (migrates an older schema), check, mark the run, audit
	st, err := store.Open(ctx, dbDst)
	if err != nil {
		return rep, err
	}
	defer func() { _ = st.Close() }()
	var integrity string
	if err := st.R.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return rep, errors.New("the restored database failed its integrity check")
	}
	if rep.SchemaTo, err = st.UserVersion(ctx); err != nil {
		return rep, err
	}
	rep.SchemaFrom = man.SchemaVersion
	rep.Counts = man.Counts
	now := o.Now().UTC().Format(time.RFC3339)
	stats := fmt.Sprintf(`{"files":%d,"bytes":%d,"linked":%d,"copied":%d}`, len(man.Files)+1, manifestBytes(man), man.Link.Linked, man.Link.Copied)
	if _, err := st.W.ExecContext(ctx, `UPDATE backup_runs SET state='completed', snapshot=?, finished_at=?, stats_json=?
		WHERE id=? AND state='running'`, filepath.Base(filepath.Clean(from)), now, stats, man.ID); err != nil {
		return rep, err
	}
	if err := DisarmAfterRestore(ctx, st, o.Now(), o.NewID); err != nil {
		return rep, err
	}
	sum := sha256.Sum256([]byte(man.ID))
	if err := audit.NewStore(st, o.NewID).Insert(ctx, audit.Entry{
		At: o.Now(), Principal: auth.CLI, Transport: reqinfo.Transport("cli"), Client: "metamusic",
		Op: "cli:restore", Method: "CLI", ArgsSHA256: hex.EncodeToString(sum[:]), Status: 200,
	}); err != nil {
		return rep, err
	}
	return rep, nil
}

func manifestBytes(m Manifest) int64 {
	n := m.DB.Bytes
	for _, f := range m.Files {
		n += f.Bytes
	}
	return n
}

// checkManifestPaths refuses manifests that would write outside the target.
func checkManifestPaths(m Manifest) error {
	if m.DB.Path != dbRel {
		return fmt.Errorf("unexpected database path %q", m.DB.Path)
	}
	for _, f := range m.Files {
		if !validRel(f.Path) {
			return fmt.Errorf("unsafe path %q", f.Path)
		}
		switch f.Kind {
		case "master", "artifact", "rendition":
			if !validMediaPath(f.Path) {
				return fmt.Errorf("unexpected media path %q", f.Path)
			}
		case "import":
			if !strings.HasPrefix(f.Path, "import/") {
				return fmt.Errorf("unexpected import path %q", f.Path)
			}
		case "export":
			if !strings.HasPrefix(f.Path, "export/") {
				return fmt.Errorf("unexpected export path %q", f.Path)
			}
		default:
			return fmt.Errorf("unknown file kind %q", f.Kind)
		}
	}
	return nil
}

// verifySnapshot hashes the database and every file against the manifest and
// returns the paths that are missing or differ.
func verifySnapshot(ctx context.Context, root *os.Root, m Manifest) []string {
	var bad []string
	check := func(rel, want string) {
		if ctx.Err() != nil {
			return
		}
		f, err := root.Open(rel)
		if err != nil {
			bad = append(bad, rel)
			return
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != want {
			bad = append(bad, rel)
		}
	}
	check(m.DB.Path, m.DB.SHA256)
	for _, f := range m.Files {
		check(f.Path, f.SHA256)
	}
	sort.Strings(bad)
	return bad
}

// copyVerified copies rel of root to dst and checks the copy against want.
func copyVerified(root *os.Root, rel, dst, want string, mode os.FileMode) error {
	in, err := root.Open(rel)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path.Clean(rel), unwrapPath(err))
	}
	defer func() { _ = in.Close() }()
	if err := copyNew(in, dst, nil); err != nil {
		return fmt.Errorf("writing %s: %w", rel, unwrapPath(err))
	}
	sum, _, err := hashFile(dst)
	if err != nil {
		return err
	}
	if sum != want {
		return fmt.Errorf("the copy of %s does not match its recorded sha256", rel)
	}
	return os.Chmod(dst, mode)
}

// DisarmAfterRestore is the C76.7 step of a restore (D-M3-31), in one transaction: every provider_state
// row is disarmed with reason `restored` and restored_at = now, and every `pending` or `submitted`
// attempt becomes `unknown`, settled by its time bound and never assumed lost or finished. Arming is then
// refused until one billing reconciliation covers the restore (jobs.ArmCheck). The audit row
// `cli:restore-disarm` is written in the same transaction.
func DisarmAfterRestore(ctx context.Context, st *store.Store, now time.Time, newID func() string) error {
	if newID == nil {
		newID = func() string { return uuid.NewV7().String() }
	}
	ts := store.FormatTime(now)
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE provider_state SET armed = 0, reason = 'restored', changed_by = 'system:cli', changed_at = ?, restored_at = ?`, ts, ts)
	if err != nil {
		return fmt.Errorf("backup: disarm providers: %w", err)
	}
	disarmed, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `UPDATE job_attempts SET state = 'unknown' WHERE state IN ('pending','submitted')`)
	if err != nil {
		return fmt.Errorf("backup: unknown attempts: %w", err)
	}
	attempts, _ := res.RowsAffected()
	e, err := audit.SystemEntry("cli:restore-disarm", now, map[string]any{"providers": disarmed, "attempts": attempts}, 0, 0, false)
	if err != nil {
		return err
	}
	e.ID = newID()
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}
