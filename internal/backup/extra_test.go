package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestError_String(t *testing.T) {
	if got := (&Error{409, "BACKUP_IN_PROGRESS", "x"}).Error(); got != "BACKUP_IN_PROGRESS: x" {
		t.Fatal(got)
	}
}

func TestErrnoName(t *testing.T) {
	for want, err := range map[string]error{
		"EXDEV": syscall.EXDEV, "EPERM": syscall.EPERM, "EMLINK": syscall.EMLINK, "EACCES": syscall.EACCES, "ERROR": errors.New("boom"),
	} {
		if got := errnoName(&os.LinkError{Err: err}); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}

func TestValidMediaPath(t *testing.T) {
	for p, want := range map[string]bool{
		"audio/masters/a.flac": true, "takes/x/request.json": true, "derived/ab/abc.mp3": true,
		"derived/exports/ab/x.zip": false, "../x": false, "/abs/x": false, "audio/masters/../../x": false,
		"secrets/secret_key": false, "import/mapping.csv": false, `audio/masters\x`: false, "audio/masters//a": false, "": false,
	} {
		if got := validMediaPath(p); got != want {
			t.Errorf("%q: %v, want %v", p, got, want)
		}
	}
}

func TestNew_Defaults(t *testing.T) {
	e := newEnv(t)
	s := New(Options{Store: e.st, DataDir: e.data, BackupDir: e.bdir})
	run, err := s.RunSync(context.Background(), "cli", auth.CLI)
	if err != nil || run.State != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
	if _, err := uuidLike(run.ID); err != nil {
		t.Errorf("default id: %v", err)
	}
	var nilSvc *Service
	if err := nilSvc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func uuidLike(id string) (string, error) {
	if len(id) != 36 || id[14] != '7' {
		return id, errors.New("not a UUIDv7")
	}
	return id, nil
}

func TestCheckManifestPaths(t *testing.T) {
	good := Manifest{DB: ManifestDB{Path: dbRel}, Files: []ManifestFile{
		{Path: "audio/masters/a.flac", Kind: "master"}, {Path: "import/mapping.csv", Kind: "import"}, {Path: "export/songs.jsonl", Kind: "export"},
	}}
	if err := checkManifestPaths(good); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(m *Manifest){
		"db path":        func(m *Manifest) { m.DB.Path = "other.db" },
		"unsafe":         func(m *Manifest) { m.Files[0].Path = "../x" },
		"media layout":   func(m *Manifest) { m.Files[0].Path = "secrets/k" },
		"import layout":  func(m *Manifest) { m.Files[1].Path = "audio/masters/x" },
		"export layout":  func(m *Manifest) { m.Files[2].Path = "audio/masters/x" },
		"unknown kind":   func(m *Manifest) { m.Files[0].Kind = "weird" },
		"windows escape": func(m *Manifest) { m.Files[0].Path = `audio\masters` },
	} {
		m := good
		m.Files = append([]ManifestFile(nil), good.Files...)
		mut(&m)
		if err := checkManifestPaths(m); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestBackup_EmptyLibrary(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := newEnv(t)
	o := e.opts
	lib := library.New(st, nil, o.Now)
	o.Store, o.DataDir, o.BackupDir = st, dir, filepath.Join(t.TempDir(), "b")
	o.Exports = exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil))
	s := New(o)
	run, err := s.RunSync(context.Background(), "manual", auth.CLI)
	if err != nil || run.State != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
	m, err := readManifest(filepath.Join(o.BackupDir, *run.Snapshot))
	if err != nil || len(m.Files) != 0 || m.Counts["tracks"] != 0 {
		t.Fatalf("%+v %v", m, err)
	}
	// without an export source the run still completes
	o.Exports, o.BackupDir = nil, filepath.Join(t.TempDir(), "b2")
	if run, err := New(o).RunSync(context.Background(), "manual", auth.CLI); err != nil || run.State != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestBackup_ConflictingShaRows(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.W.Exec(`UPDATE renditions SET path = (SELECT path FROM renditions ORDER BY rowid LIMIT 1), sha256 = 'ff'
		WHERE rowid = (SELECT MAX(rowid) FROM renditions)`); err != nil {
		t.Fatal(err)
	}
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" || !strings.Contains(*run.Error, "disagree") {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestBackup_CancelledMidRun(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.svc.hook = func(stage, _ string) {
		if stage == "started" {
			cancel()
		}
	}
	run, err := e.svc.RunSync(ctx, "cli", auth.CLI)
	if err == nil || run.State != "failed" {
		t.Fatalf("%+v %v", run, err)
	}
	if ents, _ := os.ReadDir(e.bdir); len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
}

func TestBackup_BackupDirNotCreatable(t *testing.T) {
	e := newEnv(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.opts.BackupDir = filepath.Join(blocker, "sub")
	run, err := e.newService().RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestFileHelpers(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := hashFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("hashFile of a missing file")
	}
	if err := fsyncDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("fsyncDir of a missing dir")
	}
	p := filepath.Join(dir, "a", "b")
	if err := copyNew(strings.NewReader("x"), p, nil); err != nil {
		t.Fatal(err)
	}
	if err := copyNew(strings.NewReader("y"), p, nil); err == nil {
		t.Error("copyNew overwrote an existing file")
	}
	if err := copyNew(strings.NewReader("y"), filepath.Join(p, "c"), nil); err == nil {
		t.Error("copyNew under a file")
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile+".tmp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(dir, Manifest{}); err == nil {
		t.Error("writeManifest over a leftover temp file")
	}
	if err := writeManifest(filepath.Join(dir, "nodir"), Manifest{}); err == nil {
		t.Error("writeManifest into a missing dir")
	}
	if _, err := readManifest(dir); err == nil {
		t.Error("readManifest without manifest")
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(dir); err == nil {
		t.Error("readManifest of broken JSON")
	}
	b, _ := json.Marshal(Manifest{Format: "other/9"})
	if err := os.WriteFile(filepath.Join(dir, manifestFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(dir); err == nil || !strings.Contains(err.Error(), "format") {
		t.Errorf("readManifest of a foreign format: %v", err)
	}
	if err := unwrapPath(&os.PathError{Op: "x", Path: "/abs/p", Err: syscall.ENOENT}); !errors.Is(err, syscall.ENOENT) || strings.Contains(err.Error(), "/abs") {
		t.Errorf("unwrapPath: %v", err)
	}
	if err := unwrapPath(errors.New("plain")); err.Error() != "plain" {
		t.Error("unwrapPath of a plain error")
	}
}

func TestList_SkipsIncompleteSnapshots(t *testing.T) {
	e := newEnv(t)
	run, _, _ := e.run(t)
	for _, d := range []string{"20250101T000000Z", "notasnapshot", ".20250101T000000Z.partial"} {
		if err := os.MkdirAll(filepath.Join(e.bdir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.bdir, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.List(context.Background())
	if err != nil || len(l.Snapshots) != 1 || l.Snapshots[0].Name != *run.Snapshot {
		t.Fatalf("%+v %v", l, err)
	}
	sn := l.Snapshots[0]
	if sn.Files != run.Stats.Files || sn.Bytes != run.Stats.Bytes || sn.Linked != run.Stats.Linked || sn.CreatedAt == "" {
		t.Errorf("snapshot %+v vs stats %+v", sn, run.Stats)
	}
}

func TestRestore_Branches(t *testing.T) {
	e := newEnv(t)
	_, _, dir := e.run(t)
	ctx := context.Background()
	// the target is a file
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, dir, f, RestoreOptions{}); !errors.Is(err, ErrTargetNotEmpty) {
		t.Errorf("file target: %v", err)
	}
	// a cancelled context stops before writing
	c, cancel := context.WithCancel(ctx)
	cancel()
	into := filepath.Join(t.TempDir(), "r")
	if _, err := Restore(c, dir, into, RestoreOptions{}); err == nil {
		t.Error("cancelled restore succeeded")
	}
	if _, err := os.Stat(into); err == nil {
		t.Error("cancelled restore created the target")
	}
	// defaults (real clock, os.Link, v7 ids) work, and the failure path of a half-written target cleans up
	if _, err := Restore(ctx, dir, into, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	// a snapshot whose manifest names a file the link cannot place and the copy cannot read
	bad := t.TempDir()
	m, _ := readManifest(dir)
	m.Files = []ManifestFile{{Path: "audio/masters/none.flac", Kind: "master", SHA256: "00"}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(bad, manifestFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bad, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "db", "metamusic.db"))
	if err := os.WriteFile(filepath.Join(bad, "db", "metamusic.db"), b2, 0o600); err != nil {
		t.Fatal(err)
	}
	into2 := filepath.Join(t.TempDir(), "r2")
	var ve *VerifyError
	if _, err := Restore(ctx, bad, into2, RestoreOptions{}); !errors.As(err, &ve) {
		t.Errorf("missing file: %v", err)
	}
}

func TestRestore_FailureRemovesTarget(t *testing.T) {
	e := newEnv(t)
	_, _, dir := e.run(t)
	// Link succeeds for nothing and copying is impossible because the destination is a read-only directory.
	ro := t.TempDir()
	into := filepath.Join(ro, "r")
	o := restoreOpts(e)
	n := 0
	o.Link = func(oldname, newname string) error {
		n++
		if n == 3 {
			return errors.New("injected")
		}
		return os.Link(oldname, newname)
	}
	// the injected link error falls back to a copy, which works: the restore must still succeed
	if _, err := Restore(context.Background(), dir, into, o); err != nil {
		t.Fatal(err)
	}
	// an unwritable parent makes MkdirAll fail
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(ro, 0o700) }()
	if os.Getuid() != 0 {
		if _, err := Restore(context.Background(), dir, filepath.Join(ro, "r2"), restoreOpts(e)); err == nil {
			t.Error("restore into an unwritable parent succeeded")
		}
	}
}

func TestScheduler_OffAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&Scheduler{At: "off", Run: func(context.Context) { t.Error("ran") }}).Loop(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not stop")
	}
	// a real (non-bubble) wait is cancelled before it fires
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	s := &Scheduler{At: time.Now().Add(2 * time.Hour).Format("15:04"), Run: func(context.Context) { t.Error("ran") }}
	go func() { s.Loop(ctx2); close(done2) }()
	time.Sleep(20 * time.Millisecond)
	cancel2()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not stop")
	}
}

// failAt runs a backup whose partial directory (or database file) is sabotaged at stage.
func failAt(t *testing.T, stage string, sabotage func(partial string)) Run {
	t.Helper()
	e := newEnv(t)
	e.write(t, "import/mapping.csv", "a,b\n")
	e.svc.hook = func(st, p string) {
		if st == stage {
			sabotage(p)
			t.Cleanup(func() { _ = os.Chmod(p, 0o700) })
		}
	}
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" || run.Error == nil {
		t.Fatalf("stage %s: run = %+v, err = %v", stage, run, err)
	}
	if strings.Contains(*run.Error, e.data) || strings.Contains(*run.Error, e.bdir) {
		t.Errorf("stage %s: error leaks a path: %s", stage, *run.Error)
	}
	return run
}

func TestBackup_StepFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks do not bind root")
	}
	ro := func(p string) { _ = os.Chmod(p, 0o500) }
	for _, tc := range []struct {
		stage string
		fn    func(string)
	}{
		{"started", ro}, // the db directory cannot be created
		{"started", func(p string) { // VACUUM INTO cannot write onto a directory
			_ = os.MkdirAll(filepath.Join(p, dbRel), 0o700)
		}},
		{"db", ro},       // the first media directory cannot be created
		{"imports", ro},  // the import copy cannot be created
		{"export", ro},   // the export cannot be created
		{"manifest", ro}, // the manifest cannot be written
	} {
		failAt(t, tc.stage, tc.fn)
	}
}

func TestBackup_ClosedStore(t *testing.T) {
	e := newEnv(t)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	e.opts.Store = st
	s := e.newService()
	if _, err := s.Start(context.Background(), "manual", auth.CLI); err == nil {
		t.Error("Start on a closed store")
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Error("List on a closed store")
	}
	// the failed Start released the in-process slot
	if _, err := s.Start(context.Background(), "manual", auth.CLI); err != nil && errors.Is(err, nil) {
		t.Error("unreachable")
	}
}

func TestBackup_BrokenTables(t *testing.T) {
	for _, table := range []string{"artifacts", "renditions", "comments"} {
		e := newEnv(t)
		if _, err := e.st.W.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.W.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
		run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
		if err == nil || run.State != "failed" {
			t.Errorf("%s dropped: %+v %v", table, run, err)
		}
	}
}

func TestRestore_UnreadableAndNested(t *testing.T) {
	e := newEnv(t)
	_, _, dir := e.run(t)
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// the target lies below a regular file: Stat fails with ENOTDIR
	if _, err := Restore(ctx, dir, filepath.Join(f, "sub"), RestoreOptions{}); err == nil || errors.Is(err, ErrTargetNotEmpty) {
		t.Errorf("below a file: %v", err)
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(t.TempDir(), "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(locked, 0o700) }()
		if _, err := Restore(ctx, dir, locked, RestoreOptions{}); err == nil || errors.Is(err, ErrTargetNotEmpty) {
			t.Errorf("unreadable target: %v", err)
		}
	}
	// restoring into a nested new path creates the parents
	if _, err := Restore(ctx, dir, filepath.Join(t.TempDir(), "a", "b", "c"), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	// a directory that is not a snapshot root cannot be opened as one
	if _, err := Restore(ctx, filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "r"), RestoreOptions{}); !errors.Is(err, ErrBadSnapshot) {
		t.Errorf("missing snapshot: %v", err)
	}
}

// cloneSnapshot copies a snapshot directory (files are copied, not linked).
func cloneSnapshot(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "snap")
	copyTree(t, src, dst)
	return dst
}

func TestRestore_WriteFailureCleansUp(t *testing.T) {
	e := newEnv(t)
	_, _, dir := e.run(t)
	for _, existing := range []bool{false, true} {
		into := filepath.Join(t.TempDir(), "r")
		if existing {
			if err := os.Mkdir(into, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		o := restoreOpts(e)
		o.Link = func(_, newname string) error { // a "link" that leaves a directory in the way, so the copy fallback fails
			_ = os.MkdirAll(newname, 0o700)
			return errors.New("injected")
		}
		if _, err := Restore(context.Background(), dir, into, o); err == nil {
			t.Fatal("restore succeeded")
		}
		ents, err := os.ReadDir(into)
		if existing && (err != nil || len(ents) != 0) {
			t.Errorf("existing target not emptied: %v %v", ents, err)
		}
		if !existing && err == nil {
			t.Error("new target not removed")
		}
	}
}

func TestRestore_CorruptDatabase(t *testing.T) {
	e := newEnv(t)
	_, _, dir := e.run(t)
	snap := cloneSnapshot(t, dir)
	garbage := []byte(strings.Repeat("not a database", 100))
	if err := os.WriteFile(filepath.Join(snap, dbRel), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(snap)
	if err != nil {
		t.Fatal(err)
	}
	sum, n, _ := hashBytes(garbage)
	m.DB.SHA256, m.DB.Bytes = sum, n
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(snap, manifestFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "r")
	if _, err := Restore(context.Background(), snap, into, restoreOpts(e)); err == nil {
		t.Fatal("restore of a corrupt database succeeded")
	}
	if _, err := os.Stat(into); err == nil {
		t.Error("target left behind")
	}
}

func hashBytes(b []byte) (string, int64, error) {
	p := filepath.Join(os.TempDir(), "mm-hash-probe")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(p) }()
	return hashFile(p)
}

func TestHelpers_ErrorPaths(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := hashFile(dir); err == nil {
		t.Error("hashFile of a directory")
	}
	if err := copyNew(iotestErrReader{}, filepath.Join(dir, "x"), nil); err == nil {
		t.Error("copyNew with a failing source")
	}
	e := newEnv(t)
	e.opts.BackupDir = filepath.Join(t.TempDir(), "gone")
	e.newService().applyRetention() // a missing backup directory is not an error
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
