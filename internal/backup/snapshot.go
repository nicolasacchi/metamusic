package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

const (
	manifestFormat = "mm.backup/1"
	manifestFile   = "manifest.json"
	dbRel          = "db/metamusic.db"
)

// Manifest is manifest.json of a snapshot.
type Manifest struct {
	Format        string         `json:"format"`
	ID            string         `json:"id"`
	CreatedAt     string         `json:"created_at"`
	AppVersion    string         `json:"app_version"`
	SchemaVersion int            `json:"schema_version"`
	DB            ManifestDB     `json:"db"`
	Files         []ManifestFile `json:"files"`
	Counts        map[string]int `json:"counts"`
	Link          ManifestLink   `json:"link"`
}

// ManifestDB describes the database copy.
type ManifestDB struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Integrity string `json:"integrity"`
}

// ManifestFile is one file of a snapshot.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Kind   string `json:"kind"` // master | artifact | rendition | import | export
	Linked bool   `json:"linked"`
}

// ManifestLink counts how media files entered the snapshot.
type ManifestLink struct {
	Linked   int     `json:"linked"`
	Copied   int     `json:"copied"`
	Fallback *string `json:"fallback"`
}

// mediaRoots are the only top-level places the database may point into.
var mediaRoots = []string{"audio/masters/", "takes/", "derived/"}

func validMediaPath(p string) bool {
	if !filepath.IsLocal(p) || strings.Contains(p, `\`) || path.Clean(p) != p {
		return false
	}
	for _, r := range mediaRoots {
		if strings.HasPrefix(p, r) {
			return !strings.HasPrefix(p, "derived/exports/")
		}
	}
	return false
}

func errnoName(err error) string {
	for _, e := range []struct {
		n string
		e error
	}{{"EXDEV", syscall.EXDEV}, {"EPERM", syscall.EPERM}, {"EMLINK", syscall.EMLINK}, {"EACCES", syscall.EACCES}} {
		if errors.Is(err, e.e) {
			return e.n
		}
	}
	return "ERROR"
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p) //nolint:gosec // p is built from the snapshot layout
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is a snapshot directory
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// copyNew copies src into a new file dst (O_EXCL, 0600), fsyncs it and calls
// afterWrite before the caller verifies it.
func copyNew(src io.Reader, dst string, afterWrite func()) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is inside the private partial directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if afterWrite != nil {
		afterWrite()
	}
	return nil
}

type fileSet struct {
	files []ManifestFile
	link  ManifestLink
}

func (f *fileSet) add(m ManifestFile) { f.files = append(f.files, m) }

// snapshot writes one complete snapshot and returns its stats and name.
func (s *Service) snapshot(ctx context.Context, run Run) (st RunStats, name string, err error) {
	bd := s.o.BackupDir
	if err = os.MkdirAll(bd, 0o700); err != nil {
		return st, "", fmt.Errorf("creating the backup directory: %w", err)
	}
	var partial string
	for {
		name = SnapshotName(s.o.Now(), func(n string) bool {
			for _, p := range []string{n, "." + n + ".partial"} {
				if _, e := os.Lstat(filepath.Join(bd, p)); e == nil {
					return true
				}
			}
			return false
		})
		partial = filepath.Join(bd, "."+name+".partial")
		if err = os.Mkdir(partial, 0o700); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return st, "", fmt.Errorf("creating the partial snapshot: %w", err)
		}
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(partial)
		}
	}()
	defer s.heartbeat(ctx, run.ID)()
	s.callHook("started", partial)

	// 2. database copy
	dbPath := filepath.Join(partial, filepath.FromSlash(dbRel))
	if err = os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return st, "", err
	}
	if _, err = s.o.Store.W.ExecContext(ctx, `VACUUM INTO ?`, dbPath); err != nil {
		return st, "", fmt.Errorf("copying the database: %w", err)
	}
	cp, err := sql.Open("sqlite", "file:"+(&url.URL{Path: dbPath}).EscapedPath()+"?mode=ro&immutable=1")
	if err != nil {
		return st, "", err
	}
	defer func() { _ = cp.Close() }()
	var integrity string
	if err = cp.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return st, "", fmt.Errorf("checking the database copy: %w", err)
	}
	if integrity != "ok" {
		return st, "", errors.New("the database copy failed its integrity check")
	}
	var schema int
	if err = cp.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return st, "", err
	}
	dbSum, dbBytes, err := hashFile(dbPath)
	if err != nil {
		return st, "", err
	}

	// 3. media files named by the copy
	media, counts, err := mediaOf(ctx, cp)
	if err != nil {
		return st, "", err
	}
	s.callHook("db", partial)
	root, err := os.OpenRoot(s.o.DataDir)
	if err != nil {
		return st, "", err
	}
	defer func() { _ = root.Close() }()
	var fs fileSet
	for _, m := range media {
		if err = s.place(ctx, root, partial, m, &fs); err != nil {
			return st, "", err
		}
	}

	// 4. mutable import files, copied
	s.callHook("imports", partial)
	if err = s.copyImports(root, partial, &fs); err != nil {
		return st, "", err
	}
	// 5. songs.jsonl
	s.callHook("export", partial)
	if err = s.writeExport(ctx, partial, &fs); err != nil {
		return st, "", err
	}

	// 6. manifest, then atomic completion
	s.callHook("manifest", partial)
	sort.Slice(fs.files, func(i, j int) bool { return fs.files[i].Path < fs.files[j].Path })
	counts["masters"] = countKind(fs.files, "master")
	counts["artifacts"] = countKind(fs.files, "artifact")
	counts["renditions"] = countKind(fs.files, "rendition")
	man := Manifest{
		Format: manifestFormat, ID: run.ID, CreatedAt: s.stamp(), AppVersion: s.o.Version, SchemaVersion: schema,
		DB:    ManifestDB{Path: dbRel, SHA256: dbSum, Bytes: dbBytes, Integrity: integrity},
		Files: fs.files, Counts: counts, Link: fs.link,
	}
	if man.Files == nil {
		man.Files = []ManifestFile{}
	}
	if err = cp.Close(); err != nil {
		return st, "", err
	}
	if err = os.Chmod(dbPath, 0o444); err != nil { //nolint:gosec // snapshot files are read-only on purpose
		return st, "", err
	}
	if err = writeManifest(partial, man); err != nil {
		return st, "", err
	}
	if err = fsyncDir(partial); err != nil {
		return st, "", err
	}
	if err = os.Rename(partial, filepath.Join(bd, name)); err != nil {
		return st, "", err
	}
	_ = fsyncDir(bd)

	st = RunStats{Files: len(man.Files) + 1, Bytes: dbBytes, Linked: man.Link.Linked, Copied: man.Link.Copied}
	for _, f := range man.Files {
		st.Bytes += f.Bytes
	}
	return st, name, nil
}

func countKind(files []ManifestFile, kind string) int {
	n := 0
	for _, f := range files {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func writeManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := filepath.Join(dir, manifestFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the partial directory
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, manifestFile))
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestFile)) //nolint:gosec // the operator names the snapshot
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Format != manifestFormat {
		return m, fmt.Errorf("manifest.json: unknown format %q", m.Format)
	}
	return m, nil
}

type mediaFile struct{ path, sha, kind string }

// mediaOf lists the files the database copy points at, plus row counts.
func mediaOf(ctx context.Context, db *sql.DB) ([]mediaFile, map[string]int, error) {
	seen := map[string]mediaFile{}
	for _, q := range []struct{ kind, sql string }{
		{"master", `SELECT master_path, master_sha256 FROM tracks`},
		{"artifact", `SELECT path, sha256 FROM artifacts`},
		{"rendition", `SELECT path, sha256 FROM renditions`},
	} {
		rows, err := db.QueryContext(ctx, q.sql)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var m mediaFile
			m.kind = q.kind
			if err := rows.Scan(&m.path, &m.sha); err != nil {
				_ = rows.Close()
				return nil, nil, err
			}
			if !validMediaPath(m.path) {
				_ = rows.Close()
				return nil, nil, fmt.Errorf("the database names a %s outside the data layout: %q", m.kind, m.path)
			}
			if prev, ok := seen[m.path]; ok {
				if prev.sha != m.sha {
					_ = rows.Close()
					return nil, nil, fmt.Errorf("two rows disagree about the sha256 of %s", m.path)
				}
				continue
			}
			seen[m.path] = m
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	counts := map[string]int{}
	for _, c := range []struct{ key, table string }{{"tracks", "tracks"}, {"drafts", "recipe_drafts"}, {"comments", "comments"}} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+c.table).Scan(&n); err != nil { //nolint:gosec // constants above
			return nil, nil, err
		}
		counts[c.key] = n
	}
	out := make([]mediaFile, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, counts, nil
}

// place hardlinks one frozen file into the partial snapshot, or copies and
// verifies it when linking is impossible.
func (s *Service) place(ctx context.Context, root *os.Root, partial string, m mediaFile, fs *fileSet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	src := filepath.Join(s.o.DataDir, filepath.FromSlash(m.path))
	dst := filepath.Join(partial, filepath.FromSlash(m.path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	lerr := s.o.Link(src, dst)
	if lerr == nil {
		a, e1 := os.Stat(src)
		b, e2 := os.Stat(dst)
		if e1 != nil || e2 != nil || !os.SameFile(a, b) {
			return fmt.Errorf("linking %s did not share the file", m.path)
		}
		fs.link.Linked++
		fs.add(ManifestFile{Path: m.path, SHA256: m.sha, Bytes: a.Size(), Kind: m.kind, Linked: true})
		return nil
	}
	sum, n, err := s.copyFile(root, m.path, dst, m.sha, 0o444)
	if err != nil {
		return err
	}
	if fs.link.Fallback == nil {
		name := errnoName(lerr)
		fs.link.Fallback = &name
	}
	fs.link.Copied++
	fs.add(ManifestFile{Path: m.path, SHA256: sum, Bytes: n, Kind: m.kind})
	return nil
}

// copyFile copies rel (under root) to dst, re-reads the copy and, when want is
// set, requires its sha256 to match; then sets mode.
func (s *Service) copyFile(root *os.Root, rel, dst, want string, mode os.FileMode) (string, int64, error) {
	in, err := root.Open(rel)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", rel, unwrapPath(err))
	}
	defer func() { _ = in.Close() }()
	if err := copyNew(in, dst, func() { s.callHook("copied", dst) }); err != nil {
		return "", 0, fmt.Errorf("copying %s: %w", rel, unwrapPath(err))
	}
	sum, n, err := hashFile(dst)
	if err != nil {
		return "", 0, err
	}
	if want != "" && sum != want {
		return "", 0, fmt.Errorf("the copy of %s does not match its recorded sha256", rel)
	}
	if err := os.Chmod(dst, mode); err != nil {
		return "", 0, err
	}
	return sum, n, nil
}

// unwrapPath drops the (absolute) path an *fs.PathError carries.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// copyImports copies the mutable import files (never linked).
func (s *Service) copyImports(root *os.Root, partial string, fs *fileSet) error {
	rels := []string{"import/mapping.csv", "import/mapping.todo.csv"}
	if reports, err := globRoot(root, "import/reports", ".json"); err == nil {
		rels = append(rels, reports...)
	}
	for _, rel := range rels {
		st, err := root.Lstat(rel)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		sum, n, err := s.copyFile(root, rel, filepath.Join(partial, filepath.FromSlash(rel)), "", 0o600)
		if err != nil {
			return err
		}
		fs.add(ManifestFile{Path: rel, SHA256: sum, Bytes: n, Kind: "import"})
	}
	return nil
}

func globRoot(root *os.Root, dir, ext string) ([]string, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ext) {
			out = append(out, dir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// writeExport writes songs.jsonl of every track (all statuses) and its ABC files.
func (s *Service) writeExport(ctx context.Context, partial string, fs *fileSet) error {
	if s.o.Exports == nil {
		return nil
	}
	res, err := export.Build(ctx, s.o.Exports, export.Selection{Kind: "tracks",
		Tracks: export.TrackFilter{Status: []string{"draft", "to_relisten", "final", "discarded"}}})
	if errors.Is(err, export.ErrEmpty) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("exporting songs.jsonl: %w", err)
	}
	write := func(rel string, b []byte) error {
		if !validRel(rel) {
			return fmt.Errorf("refusing export path %q", rel)
		}
		dst := filepath.Join(partial, filepath.FromSlash(rel))
		if err := copyNew(strings.NewReader(string(b)), dst, nil); err != nil {
			return err
		}
		if err := os.Chmod(dst, 0o444); err != nil { //nolint:gosec // snapshot files are read-only on purpose
			return err
		}
		sum := sha256.Sum256(b)
		fs.add(ManifestFile{Path: rel, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(b)), Kind: "export"})
		return nil
	}
	if err := write("export/songs.jsonl", res.JSONL); err != nil {
		return err
	}
	for p, b := range res.ABCFiles {
		if err := write("export/"+p, b); err != nil {
			return err
		}
	}
	return nil
}

func validRel(p string) bool {
	return p != "" && !strings.Contains(p, `\`) && fsValid(p) && filepath.IsLocal(p)
}

func fsValid(p string) bool { return path.Clean(p) == p && !strings.HasPrefix(p, "/") }

// applyRetention removes snapshots and stale partials per Retain. Failures are
// logged; they never fail a finished backup.
func (s *Service) applyRetention() {
	ents, err := os.ReadDir(s.o.BackupDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	_, drop := Retain(names, s.o.Now(), retentionLoc())
	for _, n := range drop {
		if err := os.RemoveAll(filepath.Join(s.o.BackupDir, n)); err != nil {
			slog.Warn("backup: removing an expired snapshot failed", "snapshot", n, "err", redact.Error(err))
		}
	}
}
