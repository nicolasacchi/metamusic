// Package archivetest imports the synthetic M1 archives (the acceptance and e2e
// profiles) with real ingest once per test binary and hands every test its own
// copy of the result (C42). It is TEST-ONLY: it imports "testing" and is listed
// in TestNoTestHelpersInBinary. It lives apart from librarytest because the
// importer's own tests import librarytest, and librarytest importing the
// importer would be an import cycle.
package archivetest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/importer"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// Principal is the import principal of the synthetic archives (as `seed-synthetic`).
const Principal = "seed-synthetic"

// Template is one imported archive: a data directory holding metamusic.db and
// the media files it references.
type Template struct {
	dir     string
	profile string
}

type built struct {
	once sync.Once
	dir  string
	err  error
	runs int
}

var (
	mu       sync.Mutex
	profiles = map[string]*built{}
	roots    []string
)

// SyntheticArchive imports the synthetic archive of profile ("acceptance", "e2e" or
// "reference"; the last is imported with both reference flags) into a template directory, once per profile and test binary; later
// calls return the same template. It skips under -short and when the media
// tools are missing (like every test that decodes audio).
func SyntheticArchive(tb testing.TB, profile string) Template {
	tb.Helper()
	switch profile {
	case string(synth.ProfileAcceptance), string(synth.ProfileE2E), string(synth.ProfileReference):
	default:
		tb.Fatalf("archivetest: unknown profile %q (want acceptance, e2e or reference)", profile)
	}
	testtools.Require(tb, "ffmpeg", "flac", "metaflac")
	mu.Lock()
	b := profiles[profile]
	if b == nil {
		b = &built{}
		profiles[profile] = b
	}
	mu.Unlock()
	b.once.Do(func() {
		b.dir, b.err = importArchive(synth.Profile(profile))
		mu.Lock()
		b.runs++
		mu.Unlock()
	})
	if b.err != nil {
		tb.Fatalf("archivetest: importing the %s archive: %v", profile, b.err)
	}
	return Template{dir: b.dir, profile: profile}
}

// ImportCount reports how many times the profile was imported in this binary
// (0 before the first SyntheticArchive call, then always 1).
func ImportCount(profile string) int {
	mu.Lock()
	defer mu.Unlock()
	if b := profiles[profile]; b != nil {
		return b.runs
	}
	return 0
}

// Cleanup removes the template directories; call it from TestMain after m.Run.
func Cleanup() {
	mu.Lock()
	defer mu.Unlock()
	for _, d := range roots {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err == nil && de.IsDir() {
				_ = os.Chmod(p, 0o755) //nolint:gosec // test cleanup: directories must be writable to be removed
			}
			return nil
		})
		_ = os.RemoveAll(d)
	}
	roots = nil
}

// ImportInbox imports a purpose-built inbox directory (recipes and takes a test wrote itself, for
// signals the synthetic profiles do not hold) with real ingest and returns its template. The
// template directory is removed by Cleanup.
func ImportInbox(tb testing.TB, inboxDir string) Template {
	tb.Helper()
	testtools.Require(tb, "ffmpeg", "flac", "metaflac")
	root, err := os.MkdirTemp("", "mm-archivetest-custom-")
	if err != nil {
		tb.Fatal(err)
	}
	mu.Lock()
	roots = append(roots, root)
	mu.Unlock()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := importInto(context.Background(), inboxDir, dataDir, false); err != nil {
		tb.Fatalf("archivetest: importing %s: %v", inboxDir, err)
	}
	return Template{dir: dataDir, profile: "custom"}
}

func importArchive(profile synth.Profile) (string, error) {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "mm-archivetest-"+string(profile)+"-")
	if err != nil {
		return "", err
	}
	mu.Lock()
	roots = append(roots, root)
	mu.Unlock()
	inboxDir, dataDir := filepath.Join(root, "inbox"), filepath.Join(root, "data")
	for _, d := range []string{inboxDir, dataDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return "", err
		}
	}
	if _, err := synth.Archive(ctx, testtools.Tools(), inboxDir, profile, 1); err != nil {
		return "", err
	}
	if err := importInto(ctx, inboxDir, dataDir, profile == synth.ProfileReference); err != nil {
		return "", err
	}
	return dataDir, nil
}

// importInto imports inboxDir into the (existing, empty) data directory dataDir; references sets
// both reference flags of the importer.
func importInto(ctx context.Context, inboxDir, dataDir string, references bool) error {
	tools := testtools.Tools()
	data, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	st, err := store.Open(ctx, filepath.Join(dataDir, "metamusic.db"))
	if err != nil {
		return err
	}
	lib := library.New(st, data, time.Now)
	an, err := ffmpeg.New(ctx, tools)
	if err != nil {
		_ = st.Close()
		return err
	}
	ro, err := inbox.Open(inboxDir)
	if err != nil {
		_ = st.Close()
		return err
	}
	im, err := importer.New(st, lib, data, ingest.New(data, dataDir, tools, an, time.Now), importer.Options{
		Inbox: ro, InboxLabel: "synthetic", MinAge: 0, Jobs: 2, Principal: Principal, Now: time.Now,
		ReferenceUnmatched: references, ReferenceLossy: references,
	})
	if err != nil {
		_ = st.Close()
		return err
	}
	inv, err := im.Inventory(ctx)
	if err != nil {
		_ = st.Close()
		return err
	}
	res, err := im.Apply(ctx, inv, importer.ApplyOptions{Force: true})
	if err != nil {
		_ = st.Close()
		return err
	}
	// The acceptance profile deliberately holds one unparsable line (reported as an
	// error); no take may fail.
	if res.State != "completed" || res.TracksFailed > 0 {
		_ = st.Close()
		return fmt.Errorf("import run %s ended %s with %d failed takes", res.RunID, res.State, res.TracksFailed)
	}
	// Closing the last connection checkpoints the WAL: the template is one .db file.
	if err := st.Close(); err != nil {
		return err
	}
	return nil
}

// Copy gives a test its own data directory: a copy of the database and of the
// mutable files, hardlinks of the immutable media (copied across devices).
func (t Template) Copy(tb testing.TB) string {
	tb.Helper()
	dst := filepath.Join(tb.TempDir(), "data")
	t.CopyTo(tb, dst)
	return dst
}

// CopyTo is Copy into a directory the caller chose (created if missing).
func (t Template) CopyTo(tb testing.TB, dst string) {
	tb.Helper()
	if t.dir == "" {
		tb.Fatal("archivetest: zero Template")
	}
	err := filepath.WalkDir(t.dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(t.dir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if de.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // test data directory
		}
		if !de.Type().IsRegular() {
			return nil
		}
		if immutable(rel) {
			if err := os.Link(p, target); err == nil { //nolint:gosec // G122: the template is a private temp tree no other process writes to
				return nil
			}
		}
		return copyFile(p, target)
	})
	if err != nil {
		tb.Fatalf("archivetest: copying the template: %v", err)
	}
}

// immutable reports whether a DataDir-relative file is content-addressed or
// frozen (masters, renditions, kept result files): safe to share by hardlink.
func immutable(rel string) bool {
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "audio/") || strings.HasPrefix(rel, "derived/") || strings.HasPrefix(rel, "takes/")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- a path under the template directory
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // test data file under the caller's directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
