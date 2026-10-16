package tagged_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	svc    *tagged.Service
	st     *store.Store
	data   *os.Root
	dir    string
	clk    *clock
	frames map[string][]byte // track id -> the frames written into its real master
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// newEnv opens the M2c fixture and replaces the masters of the twins (root + child) and of the reference FLAC with
// header-valid FLAC files, updating the frozen master columns the way an ingest would have written them.
func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.FixtureM2c(t, st, lt.FixedNow)
	dir := t.TempDir()
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clk := &clock{t: lt.FixedNow}
	lib := library.New(st, root, clk.now)
	for rel, b := range coverTemplate(t) {
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := root.WriteFile(rel, b, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{svc: tagged.New(root, lib, cover.New(root, lib, clk.now), clk.now), st: st, data: root, dir: dir, clk: clk, frames: map[string][]byte{}}
	if _, err := st.W.Exec(`DROP TRIGGER tracks_frozen`); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac} {
		frames := bytes.Repeat([]byte{0xff, 0xf8, byte(i + 1)}, 3000)
		e.setMaster(t, id, flactest.Build(flactest.Spec{
			TotalSamples: 96000, MD5: [16]byte{byte(i + 1), 2, 3}, Vendor: "ingest", SeekPoints: 4, PaddingBytes: 512,
			Comments: []string{"TITLE=ingest title", "REPLAYGAIN_TRACK_GAIN=-2.50 dB", "REPLAYGAIN_TRACK_PEAK=0.800000", "MM_TAKE_ID=" + id},
			Frames:   frames,
		}), frames)
	}
	return e
}

func (e *env) setMaster(t *testing.T, id string, b, frames []byte) {
	t.Helper()
	var p string
	if err := e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if err := e.data.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.data.WriteFile(p, b, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := e.data.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE tracks SET master_sha256 = ?, master_bytes = ?, frames_sha256 = ? WHERE id = ?`, sum(b), len(b), sum(frames), id); err != nil {
		t.Fatal(err)
	}
	e.frames[id] = frames
}

func (e *env) title(t *testing.T, id, title string) {
	t.Helper()
	if _, err := e.st.W.Exec(`UPDATE tracks SET title = ? WHERE id = ?`, title, id); err != nil {
		t.Fatal(err)
	}
}

func (e *env) files(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(e.dir, "derived", "dl"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (e *env) read(t *testing.T, d tagged.Download) ([]byte, flacmeta.Info) {
	t.Helper()
	b, err := e.data.ReadFile(d.Object.RelPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return b, info
}

func comments(info flacmeta.Info) map[string]string {
	m := map[string]string{}
	for _, c := range info.Comments {
		k, v, _ := strings.Cut(c, "=")
		m[k] = v
	}
	return m
}

func TestTagged_FramesIdentical(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d, err := e.svc.Ensure(context.Background(), lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	b, info := e.read(t, d)
	if got := sum(b[info.FramesOffset:]); got != sum(e.frames[lt.TrackTwinB]) {
		t.Errorf("frames sha %s", got)
	}
	var frames string
	if err := e.st.R.QueryRow(`SELECT frames_sha256 FROM tracks WHERE id = ?`, lt.TrackTwinB).Scan(&frames); err != nil || frames != sum(b[info.FramesOffset:]) {
		t.Errorf("tracks.frames_sha256 %s (%v)", frames, err)
	}
	if info.StreamInfo.MD5[0] != 2 { // twin B is index 1
		t.Errorf("STREAMINFO MD5 %x", info.StreamInfo.MD5)
	}
	if info.Count(flacmeta.BlockVorbisComment) != 1 || info.Count(flacmeta.BlockPicture) != 1 || info.Count(flacmeta.BlockSeekTable) != 1 || info.Count(flacmeta.BlockPadding) != 0 {
		t.Errorf("blocks %+v", info.Blocks)
	}
	if d.Bytes != int64(len(b)) || d.Object.ContentType != "audio/flac" || d.Object.ETag != d.Key || d.Key == "" {
		t.Errorf("download %+v", d)
	}
	if d.Object.RelPath != "derived/dl/"+d.Key[:2]+"/"+d.Key+".flac" {
		t.Errorf("rel path %s", d.Object.RelPath)
	}
	if d.SHA256 != sum(b) {
		t.Errorf("SHA256 %s, want %s", d.SHA256, sum(b))
	}
	if st, _ := e.data.Stat(d.Object.RelPath); st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v", st.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(entries) != 0 {
		t.Errorf("incoming not cleaned: %v", entries)
	}
}

func TestTagged_Tags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d, err := e.svc.Ensure(context.Background(), lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	_, info := e.read(t, d)
	c := comments(info)
	var recipe, req string
	var seed int64
	if err := e.st.R.QueryRow(`SELECT r.id, r.request_id, r.seed FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, lt.TrackTwinB).Scan(&recipe, &req, &seed); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TITLE": "Twin B", "ARTIST": "metamusic", "ALBUM": "Twin A", "TRACKNUMBER": "2", "TRACKTOTAL": "2", "GENRE": "rock",
		"REPLAYGAIN_TRACK_GAIN": "-2.50 dB", "REPLAYGAIN_TRACK_PEAK": "0.800000",
		"MM_TAKE_ID": lt.TrackTwinB, "MM_RECIPE_ID": recipe, "MM_REQUEST_ID": req, "MM_SEED": "104", "MM_STATUS": "inbox",
	}
	if seed != 104 {
		t.Fatalf("seed %d", seed)
	}
	for k, v := range want {
		if k == "MM_STATUS" {
			continue // checked below against the database
		}
		if c[k] != v {
			t.Errorf("%s = %q, want %q", k, c[k], v)
		}
	}
	var status string
	_ = e.st.R.QueryRow(`SELECT status FROM tracks WHERE id = ?`, lt.TrackTwinB).Scan(&status)
	if c["MM_STATUS"] != status || !strings.HasPrefix(c["DATE"], "2026-") || len(c["DATE"]) != 10 {
		t.Errorf("status %q date %q", c["MM_STATUS"], c["DATE"])
	}
	if d.Filename != "twin_b__104.flac" || d.Tags.Title != "Twin B" || d.Tags.Album != "Twin A" || d.Tags.TrackNumber != 2 || d.Tags.TrackTotal != 2 {
		t.Errorf("download %+v", d)
	}
	root, err := e.svc.Ensure(context.Background(), lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	if root.Tags.TrackNumber != 1 || root.Tags.TrackTotal != 2 || root.Tags.Album != "Twin A" {
		t.Errorf("root tags %+v", root.Tags)
	}
}

func TestTagged_CurrentTitle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d1, err := e.svc.Ensure(ctx, lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	e.title(t, lt.TrackTwinB, "Neu")
	e.title(t, lt.TrackTwinA, "Neue Wurzel")
	d2, err := e.svc.Ensure(ctx, lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Key == d1.Key {
		t.Fatal("a title change must produce a new key")
	}
	_, info := e.read(t, d2)
	c := comments(info)
	if c["TITLE"] != "Neu" || c["ALBUM"] != "Neue Wurzel" || d2.Filename != "neu__104.flac" {
		t.Errorf("title %q album %q file %q", c["TITLE"], c["ALBUM"], d2.Filename)
	}
	if n := len(e.files(t)); n != 2 {
		t.Errorf("%d files, want the old and the new copy", n)
	}
	// a title with nothing slug-able still gets a file name
	e.title(t, lt.TrackTwinB, "★★")
	if d4, err := e.svc.Ensure(ctx, lt.TrackTwinB); err != nil || d4.Filename != "track__104.flac" {
		t.Errorf("symbol-only title: %q %v", d4.Filename, err)
	}
	e.title(t, lt.TrackTwinB, "Neu")
	// a status change is a new key as well (MM_STATUS is part of the tags)
	if _, err := e.st.W.Exec(`UPDATE tracks SET status = 'final' WHERE id = ?`, lt.TrackTwinB); err != nil {
		t.Fatal(err)
	}
	d3, err := e.svc.Ensure(ctx, lt.TrackTwinB)
	if err != nil || d3.Key == d2.Key {
		t.Fatalf("status change key %s vs %s (%v)", d3.Key, d2.Key, err)
	}
}

func TestTagged_InputAddressed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d1, err := e.svc.Ensure(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	st1, _ := e.data.Stat(d1.Object.RelPath)
	e.clk.add(time.Hour)
	d2, err := e.svc.Ensure(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := e.data.Stat(d2.Object.RelPath)
	if d1.Key != d2.Key || !st1.ModTime().Equal(st2.ModTime()) || len(e.files(t)) != 1 {
		t.Errorf("second Ensure rewrote: %s vs %s, %v vs %v, %d files", d1.Key, d2.Key, st1.ModTime(), st2.ModTime(), len(e.files(t)))
	}
	if d1.SHA256 != d2.SHA256 || d2.SHA256 == "" || d1.Bytes != d2.Bytes {
		t.Errorf("sha/bytes differ: %+v vs %+v", d1, d2)
	}
	// a fresh service (no in-memory sha cache) reports the same sha for the file already on disk
	lib := library.New(e.st, e.data, e.clk.now)
	fresh := tagged.New(e.data, lib, cover.New(e.data, lib, e.clk.now), e.clk.now)
	d3, err := fresh.Ensure(ctx, lt.TrackTwinA)
	if err != nil || d3.SHA256 != d1.SHA256 || d3.Key != d1.Key {
		t.Errorf("fresh service: %+v %v", d3, err)
	}
}

func TestTagged_UnknownTrack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if _, err := e.svc.Ensure(context.Background(), "no-such-track"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("err %v, want ErrNotFound", err)
	}
	if _, err := e.svc.TagsOf(context.Background(), "no-such-track"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("TagsOf err %v, want ErrNotFound", err)
	}
}

func TestTagged_MissingMaster(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var p string
	_ = e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackTwinA).Scan(&p)
	if err := e.data.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(context.Background(), lt.TrackTwinA); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("err %v, want ErrMediaUnavailable", err)
	}
	// a master that is a directory is no master
	if err := e.data.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(context.Background(), lt.TrackTwinA); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("directory master: %v", err)
	}
	_ = e.data.Remove(p)
	// a master that is not a FLAC file
	if err := e.data.WriteFile(p, []byte("not flac at all"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(context.Background(), lt.TrackTwinA); err == nil {
		t.Error("a corrupt master produced a download")
	}
	if len(e.files(t)) != 0 {
		t.Error("a failed Ensure left a file behind")
	}
}

func TestTagged_MasterUntouched(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var p, sha string
	_ = e.st.R.QueryRow(`SELECT master_path, master_sha256 FROM tracks WHERE id = ?`, lt.TrackTwinA).Scan(&p, &sha)
	before, _ := e.data.Stat(p)
	if _, err := e.svc.Ensure(context.Background(), lt.TrackTwinA); err != nil {
		t.Fatal(err)
	}
	after, _ := e.data.Stat(p)
	b, _ := e.data.ReadFile(p)
	if after.Mode() != before.Mode() || after.Mode().Perm() != 0o444 || !after.ModTime().Equal(before.ModTime()) || sum(b) != sha {
		t.Errorf("master changed: %v -> %v, sha %s want %s", before.Mode(), after.Mode(), sum(b), sha)
	}
}

func TestTagged_ConcurrentSameKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	const n = 6
	var wg sync.WaitGroup
	got := make([]tagged.Download, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = e.svc.Ensure(context.Background(), lt.TrackTwinB)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || got[i].Key != got[0].Key || got[i].SHA256 != got[0].SHA256 || got[i].Key == "" {
			t.Fatalf("call %d: %+v %v", i, got[i], errs[i])
		}
	}
	if len(e.files(t)) != 1 {
		t.Errorf("%d files, want 1", len(e.files(t)))
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(entries) != 0 {
		t.Errorf("incoming not cleaned: %v", entries)
	}
}

// TestTagged_LossyMasterPassthrough: a lossy reference track downloads its master unchanged; nothing is written.
func TestTagged_LossyMasterPassthrough(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var p, sha string
	var size int64
	_ = e.st.R.QueryRow(`SELECT master_path, master_sha256, master_bytes FROM tracks WHERE id = ?`, lt.TrackLossy).Scan(&p, &sha, &size)
	before, _ := e.data.Stat(p)
	d, err := e.svc.Ensure(context.Background(), lt.TrackLossy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Object.RelPath != p || d.Object.ContentType != "audio/mpeg" || d.Object.ETag != sha || d.Key != sha || d.SHA256 != sha || d.Bytes != size {
		t.Errorf("download %+v", d)
	}
	if d.Filename != "synthetic_lossy__0.mp3" || d.Tags.Title != "" || d.Tags.MM != nil {
		t.Errorf("filename %q tags %+v", d.Filename, d.Tags)
	}
	if len(e.files(t)) != 0 {
		t.Error("a lossy download wrote under derived/dl")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "derived", "dl")); err == nil {
		t.Error("derived/dl was created")
	}
	after, _ := e.data.Stat(p)
	b, _ := e.data.ReadFile(p)
	if after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || sum(b) != sha {
		t.Error("lossy master changed")
	}
	// the key is not answered by Resolve: the library serves the master by its own sha
	if _, err := e.svc.Resolve(context.Background(), sha, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("Resolve answered a master sha: %v", err)
	}
	// TagsOf describes the track for the API
	tg, err := e.svc.TagsOf(context.Background(), lt.TrackLossy)
	if err != nil || tg.Title != lt.LossyTitle || tg.TrackNumber != 1 || tg.TrackTotal != 1 {
		t.Errorf("TagsOf %+v %v", tg, err)
	}
}

func TestTagged_ReferenceFLACTags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d, err := e.svc.Ensure(context.Background(), lt.TrackRefFlac)
	if err != nil {
		t.Fatal(err)
	}
	_, info := e.read(t, d)
	c := comments(info)
	if c["MM_REFERENCE"] != "1" {
		t.Errorf("MM_REFERENCE = %q", c["MM_REFERENCE"])
	}
	if _, ok := c["MM_SEED"]; ok {
		t.Error("a reference track carries no MM_SEED")
	}
	if c["TITLE"] != lt.RefFlacTitle || c["MM_TAKE_ID"] != lt.TrackRefFlac {
		t.Errorf("tags %v", c)
	}
	if d.Filename != "synthetic_reference__0.flac" {
		t.Errorf("filename %q", d.Filename)
	}
}

func TestTagged_Resolve(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d, err := e.svc.Ensure(ctx, lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := e.svc.Resolve(ctx, d.Key, "flac")
	if err != nil {
		t.Fatal(err)
	}
	if obj.RelPath != d.Object.RelPath || obj.ContentType != "audio/flac" || obj.ETag != d.Key || obj.DownloadName != d.Filename || obj.ModTime.IsZero() {
		t.Errorf("resolved %+v, ensured %+v", obj, d.Object)
	}
	for _, c := range []struct{ key, ext string }{
		{d.Key, "mp3"}, {d.Key, "png"}, {strings.ToUpper(d.Key), "flac"}, {"short", "flac"}, {sum([]byte("absent")), "flac"},
	} {
		if _, err := e.svc.Resolve(ctx, c.key, c.ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("Resolve(%q, %q) = %v, want ErrNotFound", c.key, c.ext, err)
		}
	}
	// a file that is not a readable FLAC still resolves, under a safe fallback name
	key := sum([]byte("garbage"))
	if err := e.data.MkdirAll("derived/dl/"+key[:2], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.data.WriteFile("derived/dl/"+key[:2]+"/"+key+".flac", []byte("garbage"), 0o444); err != nil {
		t.Fatal(err)
	}
	if o, err := e.svc.Resolve(ctx, key, "flac"); err != nil || o.DownloadName != "download.flac" {
		t.Errorf("garbage file: %+v %v", o, err)
	}
	// a directory or symlink in place of the file is not served
	key2 := sum([]byte("dir"))
	if err := e.data.MkdirAll("derived/dl/"+key2[:2]+"/"+key2+".flac", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Resolve(ctx, key2, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("directory: %v", err)
	}
}

func TestSweep_Downloads(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old, err := e.svc.Ensure(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.add(6 * 24 * time.Hour)
	young, err := e.svc.Ensure(ctx, lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.add(2 * 24 * time.Hour) // old is now 8 days, young 2 days
	// a non-cache file under derived/dl is left alone
	if err := e.data.WriteFile("derived/dl/README", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.Sweep(ctx, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want 1", n, err)
	}
	if _, err := e.data.Stat(old.Object.RelPath); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the 8-day-old copy survived")
	}
	if _, err := e.data.Stat(filepath.Dir(old.Object.RelPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the empty prefix directory survived")
	}
	if _, err := e.data.Stat(young.Object.RelPath); err != nil {
		t.Errorf("the young copy was swept: %v", err)
	}
	if _, err := e.data.Stat("derived/dl/README"); err != nil {
		t.Error("a foreign file was swept")
	}
	// the next request recreates the swept copy under the same key
	again, err := e.svc.Ensure(ctx, lt.TrackTwinA)
	if err != nil || again.Key != old.Key || again.SHA256 != old.SHA256 {
		t.Errorf("recreated %+v (%v), want key %s", again, err, old.Key)
	}
	// masters are never touched by the sweep
	var p string
	_ = e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackTwinA).Scan(&p)
	if _, err := e.data.Stat(p); err != nil {
		t.Errorf("master gone: %v", err)
	}
	// cancelled context and a missing cache directory
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.svc.Sweep(cctx, 0); err == nil {
		t.Error("cancelled sweep reported success")
	}
	empty := newEnv(t)
	if n, err := empty.svc.Sweep(ctx, 0); err != nil || n != 0 {
		t.Errorf("sweep of a missing directory: %d %v", n, err)
	}
}

// TestTagged_DecodesWithFlac (heavy): a real encoded master survives the retagging: flac --test verifies the frames
// against STREAMINFO's MD5.
func TestTagged_DecodesWithFlac(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "flac")
	e := newEnv(t)
	tools := testtools.Tools()
	// 1 s of deterministic 16-bit stereo noise, encoded by the real encoder
	raw := make([]byte, 44100*4)
	x := uint32(1)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}
	enc := filepath.Join(t.TempDir(), "m.flac")
	wav := []byte("RIFF")
	wav = binary.LittleEndian.AppendUint32(wav, uint32(36+len(raw)))
	wav = append(wav, "WAVEfmt "...)
	wav = binary.LittleEndian.AppendUint32(wav, 16)
	wav = binary.LittleEndian.AppendUint16(wav, 1) // PCM
	wav = binary.LittleEndian.AppendUint16(wav, 2) // channels
	wav = binary.LittleEndian.AppendUint32(wav, 44100)
	wav = binary.LittleEndian.AppendUint32(wav, 44100*4)
	wav = binary.LittleEndian.AppendUint16(wav, 4)
	wav = binary.LittleEndian.AppendUint16(wav, 16)
	wav = append(wav, "data"...)
	wav = binary.LittleEndian.AppendUint32(wav, uint32(len(raw)))
	wav = append(wav, raw...)
	cmd := exec.Command(tools.FLAC, "--silent", "-o", enc, "-")
	cmd.Stdin = bytes.NewReader(wav)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("flac encode: %v\n%s", err, out)
	}
	master, err := os.ReadFile(enc)
	if err != nil {
		t.Fatal(err)
	}
	info, err := flacmeta.Read(bytes.NewReader(master), int64(len(master)))
	if err != nil {
		t.Fatal(err)
	}
	e.setMaster(t, lt.TrackTwinA, master, master[info.FramesOffset:])
	d, err := e.svc.Ensure(context.Background(), lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tools.FLAC, "--test", filepath.Join(e.dir, d.Object.RelPath)).CombinedOutput()
	if err != nil {
		t.Fatalf("flac --test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Errorf("flac --test output: %s", out)
	}
	_, ninfo := e.read(t, d)
	if ninfo.StreamInfo.MD5 != info.StreamInfo.MD5 || ninfo.StreamInfo.TotalSamples != info.StreamInfo.TotalSamples {
		t.Error("STREAMINFO changed")
	}
}

// coverTemplate renders the covers the tests need once (a cover is a pure function of root, seed and size, so
// copying the files into each test's data root is the same as rendering them there) and keeps them in memory.
var (
	coverOnce  sync.Once
	coverFiles map[string][]byte
)

func coverTemplate(t *testing.T) map[string][]byte {
	t.Helper()
	coverOnce.Do(func() {
		st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "c.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		lt.FixtureM2c(t, st, lt.FixedNow)
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		now := func() time.Time { return lt.FixedNow }
		svc := cover.New(root, library.New(st, root, now), now)
		ids := []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac}
		objs := make([]serve.Object, len(ids))
		errs := make([]error, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				objs[i], _, errs[i] = svc.EnsureTrack(context.Background(), id, 512)
			}()
		}
		wg.Wait()
		coverFiles = map[string][]byte{}
		for i := range ids {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if coverFiles[objs[i].RelPath], err = root.ReadFile(objs[i].RelPath); err != nil {
				t.Fatal(err)
			}
		}
	})
	return coverFiles
}

func TestTagged_TagsOfMatchesDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	d, err := e.svc.Ensure(context.Background(), lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := e.svc.TagsOf(context.Background(), lt.TrackTwinB)
	if err != nil || tg.Title != d.Tags.Title || tg.TrackNumber != d.Tags.TrackNumber || tg.ReplayGainTrackGain != "-2.50 dB" || tg.Album != d.Tags.Album {
		t.Errorf("TagsOf %+v (%v) vs %+v", tg, err, d.Tags)
	}
	var p string
	_ = e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackTwinB).Scan(&p)
	_ = e.data.Remove(p)
	if _, err := e.svc.TagsOf(context.Background(), lt.TrackTwinB); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("missing master: %v", err)
	}
	_ = e.data.WriteFile(p, []byte("junk"), 0o444)
	if _, err := e.svc.TagsOf(context.Background(), lt.TrackTwinB); err == nil {
		t.Error("junk master accepted")
	}
}

func TestTagged_DefaultClock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	lib := library.New(e.st, e.data, nil)
	svc := tagged.New(e.data, lib, cover.New(e.data, lib, nil), nil)
	if n, err := svc.Sweep(context.Background(), time.Hour); err != nil || n != 0 {
		t.Errorf("sweep %d %v", n, err)
	}
}

// TestTagged_FilesystemFailures: the cache directories being unusable surfaces as errors, never as a partial file.
func TestTagged_FilesystemFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct{ name, blocker string }{
		{"cover cache", "derived/covers"}, {"download cache", "derived/dl"}, {"incoming", "incoming"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if c.blocker == "derived/covers" {
				_ = e.data.RemoveAll("derived/covers") // the cached twin covers; the file below takes their place
			}
			if err := e.data.WriteFile(c.blocker, []byte("in the way"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Ensure(ctx, lt.TrackTwinA); err == nil {
				t.Error("Ensure succeeded with an unusable directory")
			}
			if _, err := e.svc.Resolve(ctx, sum([]byte("k")), "flac"); c.blocker == "derived/dl" && !errors.Is(err, serve.ErrNotFound) && err == nil {
				t.Errorf("Resolve: %v", err)
			}
		})
	}
}

// TestTagged_ClosedBackends: a closed database or data root yields errors, not panics.
func TestTagged_ClosedBackends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	good, err := e.svc.Ensure(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	_ = e.data.Close()
	if _, err := e.svc.Ensure(ctx, lt.TrackTwinB); err == nil {
		t.Error("Ensure on a closed root")
	}
	if _, err := e.svc.Resolve(ctx, good.Key, "flac"); err == nil || errors.Is(err, serve.ErrNotFound) {
		t.Errorf("Resolve on a closed root: %v", err)
	}
	if _, err := e.svc.Sweep(ctx, 0); err == nil {
		t.Error("Sweep on a closed root")
	}
	_ = e.st.Close()
	if _, err := e.svc.Ensure(ctx, lt.TrackTwinB); err == nil {
		t.Error("Ensure on a closed store")
	}
	if _, err := e.svc.TagsOf(ctx, lt.TrackTwinB); err == nil {
		t.Error("TagsOf on a closed store")
	}
}

// TestTagged_UnreadableCachedCopy: a cached copy that cannot be read is an error, not a wrong sha.
func TestTagged_UnreadableCachedCopy(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads mode 000 files")
	}
	e := newEnv(t)
	d, err := e.svc.Ensure(context.Background(), lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	fresh := tagged.New(e.data, library.New(e.st, e.data, e.clk.now), cover.New(e.data, library.New(e.st, e.data, e.clk.now), e.clk.now), e.clk.now)
	if err := e.data.Chmod(d.Object.RelPath, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Ensure(context.Background(), lt.TrackTwinA); err == nil {
		t.Error("an unreadable cached copy was served")
	}
	_ = e.data.Chmod(d.Object.RelPath, 0o444)
}
