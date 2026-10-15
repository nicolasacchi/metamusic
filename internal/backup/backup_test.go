package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (f *fakeNotifier) Notify(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

func (f *fakeNotifier) got() []notify.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.Message(nil), f.msgs...)
}

type env struct {
	data, bdir string
	st         *store.Store
	opts       Options
	svc        *Service
	ntf        *fakeNotifier
	now        *atomic.Pointer[time.Time]
}

var (
	tmplOnce sync.Once
	tmplDir  string
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	code := m.Run()
	if tmplDir != "" {
		_ = os.RemoveAll(tmplDir)
	}
	os.Exit(code)
}

// fixtureTemplate builds the M2a fixture library with its files once per test binary.
func fixtureTemplate(t *testing.T) string {
	t.Helper()
	tmplOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-backup-fixture-")
		if err != nil {
			t.Fatal(err)
		}
		tmplDir = dir
		lt.WriteFixtureDB(t, filepath.Join(dir, "metamusic.db"))
	})
	if tmplDir == "" {
		t.Fatal("fixture template missing")
	}
	return tmplDir
}

// copyTree copies the regular files of src into dst (the database and media of the template).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func exdev(oldname, newname string) error {
	return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EXDEV}
}

// newEnv builds a data directory with the M2a fixture library and its files.
func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{data: filepath.Join(root, "data"), bdir: filepath.Join(root, "backup"), ntf: &fakeNotifier{}, now: &atomic.Pointer[time.Time]{}}
	fixed := lt.FixedNow
	e.now.Store(&fixed)
	if err := os.MkdirAll(e.data, 0o750); err != nil {
		t.Fatal(err)
	}
	copyTree(t, fixtureTemplate(t), e.data)
	st, err := store.Open(context.Background(), filepath.Join(e.data, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	now := func() time.Time { return *e.now.Load() }
	lib := library.New(st, nil, now)
	var n atomic.Uint64
	e.opts = Options{
		Store: st, DataDir: e.data, BackupDir: e.bdir,
		Exports: exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil)),
		Now:     now, NewID: func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xb000+n.Add(1)) },
		Version: "v-test", Notify: e.ntf,
	}
	e.svc = e.newService()
	return e
}

func (e *env) newService() *Service {
	return New(e.opts)
}

func (e *env) run(t *testing.T) (Run, Manifest, string) {
	t.Helper()
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err != nil {
		t.Fatalf("RunSync: %v", err)
	}
	if run.State != "completed" || run.Snapshot == nil {
		t.Fatalf("run = %+v", run)
	}
	dir := filepath.Join(e.bdir, *run.Snapshot)
	m, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return run, m, dir
}

func (e *env) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(e.data, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mediaKinds(m Manifest) (n int) {
	for _, f := range m.Files {
		if f.Kind == "master" || f.Kind == "artifact" || f.Kind == "rendition" {
			n++
		}
	}
	return n
}

func TestBackup_ManifestComplete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "import/mapping.csv", "a,b\n")
	e.write(t, "import/mapping.todo.csv", "c,d\n")
	e.write(t, "import/reports/r1.json", `{"r":1}`)
	e.write(t, "import/reports/notes.txt", "no")
	e.write(t, "import/other.txt", "no")
	e.write(t, "incoming/x.part", "no")
	e.write(t, "inbox/song.flac", "no")
	e.write(t, "secrets/secret_key", "no")
	e.write(t, "derived/exports/ab/abcdef.zip", "no")
	_, m, dir := e.run(t)

	if m.Format != "mm.backup/1" || m.AppVersion != "v-test" || m.DB.Integrity != "ok" || m.SchemaVersion < 3 {
		t.Fatalf("manifest header: %+v", m)
	}
	have := map[string]ManifestFile{}
	for _, f := range m.Files {
		have[f.Path] = f
		if strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") {
			t.Errorf("non-relative path %q", f.Path)
		}
		for _, bad := range []string{"secrets", "incoming", "inbox", "derived/exports", "import/other", "import/reports/notes"} {
			if strings.HasPrefix(f.Path, bad) {
				t.Errorf("forbidden entry %q", f.Path)
			}
		}
		sum, n, err := hashFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || sum != f.SHA256 || n != f.Bytes {
			t.Errorf("%s: sha/size mismatch (%v)", f.Path, err)
		}
	}
	for i := 1; i < len(m.Files); i++ {
		if m.Files[i-1].Path >= m.Files[i].Path {
			t.Fatalf("files not sorted at %q", m.Files[i].Path)
		}
	}
	// every master, artifact and rendition of the live database is listed with its sha
	for _, q := range []string{`SELECT master_path, master_sha256 FROM tracks`, `SELECT path, sha256 FROM artifacts`, `SELECT path, sha256 FROM renditions`} {
		rows, err := e.st.R.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var p, sha string
			if err := rows.Scan(&p, &sha); err != nil {
				t.Fatal(err)
			}
			if f, ok := have[p]; !ok || f.SHA256 != sha {
				t.Errorf("%s missing or wrong sha in the manifest", p)
			}
		}
		_ = rows.Close()
	}
	for _, p := range []string{"import/mapping.csv", "import/mapping.todo.csv", "import/reports/r1.json"} {
		f, ok := have[p]
		if !ok || f.Kind != "import" || f.Linked {
			t.Errorf("%s: %+v", p, f)
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v %v", p, st, err)
		}
	}
	// the database copy and songs.jsonl
	if sum, _, err := hashFile(filepath.Join(dir, "db", "metamusic.db")); err != nil || sum != m.DB.SHA256 {
		t.Errorf("db sha: %v", err)
	}
	jl, err := os.ReadFile(filepath.Join(dir, "export", "songs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if rep := yue2.ValidateBatch(jl, os.DirFS(filepath.Join(dir, "export"))); !rep.OK {
		t.Errorf("songs.jsonl does not validate: %+v", rep.Issues)
	}
	if f := have["export/songs.jsonl"]; f.Kind != "export" || f.Linked {
		t.Errorf("songs.jsonl entry %+v", f)
	}
	if m.Counts["tracks"] == 0 || m.Counts["comments"] == 0 || m.Counts["masters"] != m.Counts["tracks"] {
		t.Errorf("counts %v", m.Counts)
	}
	// the snapshot opens as a database with the same tracks
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "db", "metamusic.db")+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&n); err != nil || n != m.Counts["tracks"] {
		t.Errorf("tracks in snapshot db = %d (%v)", n, err)
	}
}

func TestBackup_HardlinksSameInode(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	run, m, dir := e.run(t)
	media := mediaKinds(m)
	if media == 0 || m.Link.Linked != media || m.Link.Copied != 0 || m.Link.Fallback != nil {
		t.Fatalf("link = %+v, media files %d", m.Link, media)
	}
	for _, f := range m.Files {
		if f.Kind == "import" || f.Kind == "export" {
			continue
		}
		a, err1 := os.Stat(filepath.Join(e.data, filepath.FromSlash(f.Path)))
		b, err2 := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err1 != nil || err2 != nil || !os.SameFile(a, b) || !f.Linked {
			t.Errorf("%s: not the same inode (%v %v)", f.Path, err1, err2)
		}
	}
	if run.Stats == nil || run.Stats.Linked != media || run.Stats.Files != len(m.Files)+1 || run.Stats.Bytes <= 0 {
		t.Errorf("stats = %+v", run.Stats)
	}
}

func TestBackup_EXDEVFallbackCopiesAndVerifies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.opts.Link = exdev
	e.svc = e.newService()
	_, m, dir := e.run(t)
	media := mediaKinds(m)
	if m.Link.Copied != media || m.Link.Linked != 0 || m.Link.Fallback == nil || *m.Link.Fallback != "EXDEV" {
		t.Fatalf("link = %+v (media %d)", m.Link, media)
	}
	for _, f := range m.Files {
		if f.Kind == "import" || f.Kind == "export" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		st, err := os.Stat(p)
		live, _ := os.Stat(filepath.Join(e.data, filepath.FromSlash(f.Path)))
		if err != nil || st.Mode().Perm() != 0o444 || os.SameFile(st, live) || f.Linked {
			t.Errorf("%s: %v mode %v", f.Path, err, st)
		}
		if sum, _, err := hashFile(p); err != nil || sum != f.SHA256 {
			t.Errorf("%s: sha %v", f.Path, err)
		}
	}
}

func TestBackup_CopyCorruptionDetected(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.opts.Link = exdev
	e.svc = e.newService()
	flipped := false
	e.svc.hook = func(stage, p string) {
		if stage == "copied" && !flipped && strings.Contains(filepath.ToSlash(p), "/audio/masters/") {
			flipped = true
			b, err := os.ReadFile(p)
			if err != nil {
				t.Error(err)
				return
			}
			b[0] ^= 0xff
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" || run.Error == nil {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	ents, _ := os.ReadDir(e.bdir)
	if len(ents) != 0 {
		t.Errorf("backup dir not clean: %v", ents)
	}
	msgs := e.ntf.got()
	if len(msgs) != 1 || msgs[0].Priority != 4 {
		t.Fatalf("notifications = %+v", msgs)
	}
	for _, s := range []string{msgs[0].Title, msgs[0].Body, strings.Join(msgs[0].Tags, " "), msgs[0].Click} {
		if strings.Contains(s, e.data) || strings.Contains(s, e.bdir) || strings.Contains(strings.ToLower(s), "secret") {
			t.Errorf("notification leaks: %q", s)
		}
	}
	if strings.Contains(*run.Error, e.data) || strings.Contains(*run.Error, e.bdir) {
		t.Errorf("run error carries an absolute path: %s", *run.Error)
	}
}

func TestBackup_SnapshotSurvivesLiveMutation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, m, dir := e.run(t)
	var master ManifestFile
	for _, f := range m.Files {
		if f.Kind == "master" {
			master = f
			break
		}
	}
	var trackID, title string
	if err := e.st.R.QueryRow(`SELECT id, title FROM tracks WHERE master_path = ?`, master.Path).Scan(&trackID, &title); err != nil {
		t.Fatal(err)
	}
	ts := lt.FixedNow.Format(time.RFC3339)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO comments(id, track_id, body, kind, state, source, principal_kind, principal_id, principal_name, created_at, updated_at)
			VALUES ('c-late', ?, 'late note', 'note', 'open', 'api', 'human', 'human:x', 'x', ?, ?)`, []any{trackID, ts, ts}},
		{`UPDATE tracks SET title = 'Retitled' WHERE id = ?`, []any{trackID}},
		{`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
			VALUES (?, 'human', 5, 'human', 'human:x', 'x', ?, ?) ON CONFLICT(track_id, source) DO UPDATE SET stars = 5`, []any{trackID, ts, ts}},
	} {
		if _, err := e.st.W.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(e.data, filepath.FromSlash(master.Path))); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "db", "metamusic.db")+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var got string
	if err := db.QueryRow(`SELECT title FROM tracks WHERE id = ?`, trackID).Scan(&got); err != nil || got != title {
		t.Errorf("snapshot title = %q (%v), want %q", got, err, title)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments WHERE id = 'c-late'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("late comment visible in the snapshot (%d, %v)", n, err)
	}
	var stars sql.NullInt64
	if err := db.QueryRow(`SELECT stars FROM ratings WHERE track_id = ? AND source = 'human'`, trackID).Scan(&stars); err == nil && stars.Int64 == 5 {
		t.Error("late rating visible in the snapshot")
	}
	sum, _, err := hashFile(filepath.Join(dir, filepath.FromSlash(master.Path)))
	if err != nil || sum != master.SHA256 {
		t.Errorf("snapshot master: %v", err)
	}
}

func TestBackup_InProgressRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gate := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	e.svc.hook = func(stage, _ string) {
		if stage == "started" {
			once.Do(func() { close(reached) })
			<-gate
		}
	}
	first, err := e.svc.Start(context.Background(), "manual", auth.CLI)
	if err != nil || first.State != "running" || first.Snapshot != nil || first.Stats != nil {
		t.Fatalf("first = %+v, %v", first, err)
	}
	<-reached
	_, err = e.svc.Start(context.Background(), "manual", auth.CLI)
	var be *Error
	if !errors.As(err, &be) || be.Code != "BACKUP_IN_PROGRESS" || be.Status != 409 {
		t.Fatalf("second Start: %v", err)
	}
	close(gate)
	if err := e.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Snapshots) != 1 || l.Runs[0].ID != first.ID || l.Runs[0].State != "completed" {
		t.Fatalf("listing = %+v", l)
	}
}

func TestBackup_CrossProcessGuard(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	other := e.newService() // as the CLI `backup` next to a running `serve`
	gate := make(chan struct{})
	reached := make(chan struct{})
	e.svc.hook = func(stage, _ string) {
		if stage == "started" {
			close(reached)
			<-gate
		}
	}
	if _, err := e.svc.Start(context.Background(), "manual", auth.CLI); err != nil {
		t.Fatal(err)
	}
	<-reached
	_, err := other.RunSync(context.Background(), "cli", auth.CLI)
	var be *Error
	if !errors.As(err, &be) || be.Code != "BACKUP_IN_PROGRESS" {
		t.Fatalf("other.RunSync: %v", err)
	}
	close(gate)
	if err := e.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := other.RunSync(context.Background(), "cli", auth.CLI); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
}

func TestBackup_StaleRunMarkedFailed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := lt.FixedNow.Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := e.st.W.Exec(`INSERT INTO backup_runs(id, cause, principal_id, state, started_at, heartbeat_at)
		VALUES ('stale-1', 'manual', 'system:cli', 'running', ?, ?)`, old, old); err != nil {
		t.Fatal(err)
	}
	run, _, _ := e.run(t)
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var stale *Run
	for i := range l.Runs {
		if l.Runs[i].ID == "stale-1" {
			stale = &l.Runs[i]
		}
	}
	if stale == nil || stale.State != "failed" || stale.Error == nil || *stale.Error != "interrupted" {
		t.Fatalf("stale run = %+v", stale)
	}
	if run.State != "completed" {
		t.Fatalf("new run = %+v", run)
	}
}

func TestBackup_FreshRunningRowBlocks(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	at := lt.FixedNow.Add(-30 * time.Second).Format(time.RFC3339)
	if _, err := e.st.W.Exec(`INSERT INTO backup_runs(id, cause, principal_id, state, started_at, heartbeat_at)
		VALUES ('live-1', 'manual', 'system:cli', 'running', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Start(context.Background(), "manual", auth.CLI)
	var be *Error
	if !errors.As(err, &be) || be.Code != "BACKUP_IN_PROGRESS" {
		t.Fatalf("err = %v", err)
	}
}

func TestBackup_SameSecondNamesDistinct(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r1, _, _ := e.run(t) // complete, manifest read
	r2, err := e.svc.RunSync(context.Background(), "scheduled", auth.CLI)
	if err != nil || r2.State != "completed" {
		t.Fatalf("second run %+v: %v", r2, err)
	}
	base := lt.FixedNow.UTC().Format("20060102T150405Z")
	if *r1.Snapshot != base || *r2.Snapshot != base+"-2" {
		t.Fatalf("names %q %q", *r1.Snapshot, *r2.Snapshot)
	}
	// retention keeps the newer of two snapshots of one day
	if _, err := readManifest(filepath.Join(e.bdir, *r2.Snapshot)); err != nil {
		t.Errorf("second snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.bdir, *r1.Snapshot)); err == nil {
		t.Error("the older same-day snapshot should have been retired")
	}
}

func TestBackup_CloseWaits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gate := make(chan struct{})
	reached := make(chan struct{})
	e.svc.hook = func(stage, _ string) {
		if stage == "started" {
			close(reached)
			<-gate
		}
	}
	if _, err := e.svc.Start(context.Background(), "manual", auth.CLI); err != nil {
		t.Fatal(err)
	}
	<-reached
	closed := make(chan error, 1)
	go func() { closed <- e.svc.Close(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("Close returned while the run was in flight")
	case <-time.After(150 * time.Millisecond):
	}
	// a Start after Close began is refused
	_, err := e.svc.Start(context.Background(), "manual", auth.CLI)
	var be *Error
	if !errors.As(err, &be) || be.Status != 503 || be.Code != "UNAVAILABLE" {
		t.Fatalf("Start during Close: %v", err)
	}
	close(gate)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	var state string
	if err := e.st.R.QueryRow(`SELECT state FROM backup_runs WHERE cause='manual' AND id <> ? ORDER BY started_at DESC LIMIT 1`, lt.BackupRunID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("state = %q (%v)", state, err)
	}
	if _, err := e.svc.RunSync(context.Background(), "cli", auth.CLI); err == nil {
		t.Fatal("RunSync after Close must fail")
	}
}

func TestBackup_CloseDeadlineAbortsRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gate := make(chan struct{})
	reached := make(chan struct{})
	e.svc.hook = func(stage, _ string) {
		if stage == "started" {
			close(reached)
			<-gate
		}
	}
	if _, err := e.svc.Start(context.Background(), "manual", auth.CLI); err != nil {
		t.Fatal(err)
	}
	<-reached
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	go func() { time.Sleep(200 * time.Millisecond); close(gate) }()
	if err := e.svc.Close(ctx); err == nil {
		t.Fatal("Close past its deadline must report it")
	}
	var state string
	if err := e.st.R.QueryRow(`SELECT state FROM backup_runs WHERE id <> ? ORDER BY started_at DESC, id DESC LIMIT 1`, lt.BackupRunID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("state = %q (%v)", state, err)
	}
}

func TestBackup_ExcludesExportsAndProbe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "derived/exports/ab/abcdef.jsonl", "x")
	e.write(t, "derived/probe/probe-v1.flac", "x")
	_, m, dir := e.run(t)
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "derived/exports") || strings.HasPrefix(f.Path, "derived/probe") {
			t.Errorf("unexpected %s", f.Path)
		}
	}
	for _, p := range []string{"derived/exports", "derived/probe"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s exists in the snapshot", p)
		}
	}
}

// TestBackup_ExcludesCoverAndDownloadCaches: the cover and tagged-download caches are rebuilt on demand and never
// enter a snapshot (only files the database points at are copied).
func TestBackup_ExcludesCoverAndDownloadCaches(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "derived/covers/ab/abcdef.png", "x")
	e.write(t, "derived/dl/cd/cdef01.flac", "x")
	_, m, dir := e.run(t)
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, "derived/covers") || strings.HasPrefix(f.Path, "derived/dl") {
			t.Errorf("unexpected %s", f.Path)
		}
	}
	for _, p := range []string{"derived/covers", "derived/dl"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s exists in the snapshot", p)
		}
	}
}

func TestBackup_MissingLiveFileFailsRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var p string
	if err := e.st.R.QueryRow(`SELECT master_path FROM tracks LIMIT 1`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.data, p)); err != nil {
		t.Fatal(err)
	}
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" || !strings.Contains(*run.Error, p) {
		t.Fatalf("run = %+v err = %v", run, err)
	}
}

func TestBackup_RejectsPathsOutsideLayout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.st.W.Exec(`DROP TRIGGER renditions_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE renditions SET path = '../escape' WHERE rowid = (SELECT MIN(rowid) FROM renditions)`); err != nil {
		t.Fatal(err)
	}
	run, err := e.svc.RunSync(context.Background(), "manual", auth.CLI)
	if err == nil || run.State != "failed" {
		t.Fatalf("run = %+v err = %v", run, err)
	}
}

func TestBackup_RetentionAfterRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	loc := time.Local
	// an old partial and a non-snapshot directory beside a stale snapshot chain
	old := lt.FixedNow.Add(-48 * time.Hour).In(loc)
	oldPartial := filepath.Join(e.bdir, "."+old.UTC().Format(nameLayout)+".partial")
	if err := os.MkdirAll(oldPartial, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.bdir, "keepme"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	if _, err := os.Stat(oldPartial); err == nil {
		t.Error("old partial not removed")
	}
	if _, err := os.Stat(filepath.Join(e.bdir, "keepme")); err != nil {
		t.Error("non-snapshot directory touched")
	}
}

func TestBackup_ListEmptyAndBadCause(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.svc.Start(context.Background(), "weird", auth.CLI); err == nil {
		t.Fatal("bad cause accepted")
	}
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if l.Snapshots == nil || l.Runs == nil || l.BackupDirLabel != "backup" || l.Schedule.At != "off" || l.Schedule.NextRunAt != nil {
		t.Fatalf("listing = %+v", l)
	}
	b, _ := json.Marshal(l)
	if strings.Contains(string(b), e.bdir) {
		t.Error("listing leaks the backup directory")
	}
}

func TestScheduleFor(t *testing.T) {
	t.Parallel()
	if s := ScheduleFor("off", lt.FixedNow, time.UTC); s.At != "off" || s.NextRunAt != nil {
		t.Fatalf("%+v", s)
	}
	s := ScheduleFor("03:30", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), time.UTC)
	if s.NextRunAt == nil || *s.NextRunAt != "2026-10-07T03:30:00Z" {
		t.Fatalf("%+v", s)
	}
}

func TestBackup_FailureNotifies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.opts.Link = exdev
	e.svc = e.newService()
	flipped := false
	e.svc.hook = func(stage, p string) {
		if stage == "copied" && !flipped && strings.Contains(filepath.ToSlash(p), "/audio/masters/") {
			flipped = true
			if b, err := os.ReadFile(p); err == nil {
				b[0] ^= 0xff
				_ = os.WriteFile(p, b, 0o600)
			}
		}
	}
	if _, err := e.svc.RunSync(context.Background(), "manual", auth.CLI); err == nil {
		t.Fatal("run succeeded")
	}
	msgs := e.ntf.got()
	if len(msgs) != 1 {
		t.Fatalf("want one failure notification, got %+v", msgs)
	}
	for _, s := range []string{msgs[0].Title, msgs[0].Body, msgs[0].Click, strings.Join(msgs[0].Tags, " ")} {
		if strings.Contains(s, e.data) || strings.Contains(s, e.bdir) || strings.Contains(strings.ToLower(s), "secret") {
			t.Errorf("notification leaks: %q", s)
		}
	}
}
