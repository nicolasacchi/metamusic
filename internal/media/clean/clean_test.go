package clean

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	svc    *Service
	st     *store.Store
	data   *os.Root
	dir    string
	taints map[string]cleantest.Taint
}

func newEnv(t *testing.T, o Options) *env {
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
	e := &env{st: st, data: root, dir: dir}
	e.taints = cleantest.Rewrite(t, st, root, lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLossy)
	o.Store, o.Data = st, root
	if o.Now == nil {
		o.Now = func() time.Time { return lt.FixedNow }
	}
	e.svc = New(o)
	return e
}

func (e *env) read(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := e.data.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// assertNoHidden fails when the bytes carry the marker or any planted key anywhere in the metadata area (the head of the
// file, and for MP3 the last 256 bytes). The audio frames are binary and are compared separately.
func assertNoHidden(t *testing.T, what string, b []byte, head int) {
	t.Helper()
	zones := [][]byte{b[:min(head, len(b))], b[max(0, len(b)-256):]}
	for _, z := range zones {
		if bytes.Contains(z, []byte(cleantest.Marker)) {
			t.Errorf("%s carries the private marker", what)
		}
		for _, k := range cleantest.HiddenKeys {
			if bytes.Contains(bytes.ToUpper(z), []byte(k+"=")) || bytes.Contains(z, []byte(k)) {
				t.Errorf("%s carries the key %s", what, k)
			}
		}
	}
}

func TestEnsureFLACKeepsAudioAndDropsEverythingElse(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	id := lt.TrackTwinA
	masterBefore := e.read(t, e.taints[id].MasterPath)
	r, err := e.svc.Ensure(context.Background(), id, KindFLAC)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindFLAC || r.Ext != "flac" || r.ContentType != "audio/flac" || r.Lossy {
		t.Fatalf("rendition %+v", r)
	}
	b := e.read(t, r.Path)
	if r.SHA256 != sum(b) || r.Bytes != int64(len(b)) {
		t.Fatalf("row hash or size does not describe the file")
	}
	if r.SHA256 == e.taints[id].MasterSHA {
		t.Fatalf("the rendition has the master's sha256")
	}
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, bl := range info.Blocks {
		if bl.Type != flacmeta.BlockStreamInfo && bl.Type != flacmeta.BlockSeekTable && bl.Type != flacmeta.BlockVorbisComment {
			t.Errorf("block type %d survived", bl.Type)
		}
	}
	var title string
	if err := e.st.R.QueryRow(`SELECT title FROM tracks WHERE id = ?`, id).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if len(info.Comments) != 1 || info.Comments[0] != "TITLE="+title || info.Vendor != Vendor {
		t.Errorf("comments %q vendor %q", info.Comments, info.Vendor)
	}
	if got := b[info.FramesOffset:]; !bytes.Equal(got, e.taints[id].Frames) {
		t.Errorf("audio frames differ from the master's")
	}
	if info.StreamInfo.MD5 != e.taints[id].MD5 {
		t.Errorf("STREAMINFO MD5 differs (a lossless copy keeps it)")
	}
	assertNoHidden(t, "flac_clean", b, int(info.FramesOffset))
	if err := Check(KindFLAC, bytes.NewReader(b), int64(len(b))); err != nil {
		t.Errorf("own output fails own check: %v", err)
	}
	st, err := e.data.Stat(r.Path)
	if err != nil || st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v %v", st.Mode(), err)
	}
	// the master is only read
	if got := e.read(t, e.taints[id].MasterPath); !bytes.Equal(got, masterBefore) {
		t.Errorf("master changed")
	}
	if mst, _ := e.data.Stat(e.taints[id].MasterPath); mst.Mode().Perm() != 0o444 {
		t.Errorf("master mode %v", mst.Mode())
	}
	if r.Filename != "twin_a.flac" || strings.Contains(r.Filename, "__") {
		t.Errorf("filename %q", r.Filename)
	}
}

func TestEnsureMP3(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	id := lt.TrackTwinB
	r, err := e.svc.Ensure(context.Background(), id, KindMP3)
	if err != nil {
		t.Fatal(err)
	}
	b := e.read(t, r.Path)
	if r.Ext != "mp3" || r.ContentType != "audio/mpeg" || !bytes.HasPrefix(b, []byte("ID3\x04")) {
		t.Fatalf("rendition %+v head %q", r, b[:4])
	}
	if !bytes.HasSuffix(b, e.taints[id].MP3Frames) {
		t.Fatalf("frames missing")
	}
	tagLen := len(b) - len(e.taints[id].MP3Frames)
	if !bytes.Contains(b[:tagLen], []byte("TIT2")) {
		t.Fatalf("tag %q", b[:tagLen])
	}
	for _, f := range []string{"TPE1", "COMM", "TXXX", "TALB"} {
		if bytes.Contains(b[:tagLen], []byte(f)) {
			t.Errorf("frame %s survived", f)
		}
	}
	assertNoHidden(t, "mp3_320_clean", b, tagLen)
	if err := Check(KindMP3, bytes.NewReader(b), int64(len(b))); err != nil {
		t.Errorf("own output fails own check: %v", err)
	}
	// the Owner's mp3_320 file is not touched
	var rp string
	if err := e.st.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, id).Scan(&rp); err != nil {
		t.Fatal(err)
	}
	if got := e.read(t, rp); !bytes.Contains(got, []byte(cleantest.Marker)) {
		t.Errorf("the source rendition was rewritten")
	}
}

func TestEnsureLossyMasterAnswersMP3ForBothKinds(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	for _, kind := range []string{KindFLAC, KindMP3} {
		r, err := e.svc.Ensure(context.Background(), lt.TrackLossy, kind)
		if err != nil {
			t.Fatal(kind, err)
		}
		if r.Kind != KindMP3 || !r.Lossy || r.Ext != "mp3" {
			t.Errorf("%s: %+v", kind, r)
		}
		b := e.read(t, r.Path)
		if !bytes.HasSuffix(b, e.taints[lt.TrackLossy].Frames) {
			t.Errorf("%s: frames differ", kind)
		}
		assertNoHidden(t, "lossy clean", b, len(b)-len(e.taints[lt.TrackLossy].Frames))
		if err := Check(KindMP3, bytes.NewReader(b), int64(len(b))); err != nil {
			t.Errorf("%v", err)
		}
	}
	if n := e.svc.Builds(); n != 1 {
		t.Errorf("builds = %d, want 1 (both kinds are one file)", n)
	}
	var n int
	_ = e.st.R.QueryRow(`SELECT COUNT(*) FROM renditions WHERE track_id = ? AND kind = ?`, lt.TrackLossy, KindFLAC).Scan(&n)
	if n != 0 {
		t.Errorf("a flac_clean row exists for a lossy track")
	}
}

func TestEnsureCacheAndTitleRebuild(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	id := lt.TrackTwinA
	ctx := context.Background()
	r1, err := e.svc.Ensure(ctx, id, KindFLAC)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := e.svc.Ensure(ctx, id, KindFLAC)
	if err != nil || r2.SHA256 != r1.SHA256 || e.svc.Builds() != 1 {
		t.Fatalf("second request rebuilt: builds %d err %v", e.svc.Builds(), err)
	}
	// a fresh service (a restart) finds the row too
	fresh := New(Options{Store: e.st, Data: e.data})
	if _, err := fresh.Ensure(ctx, id, KindFLAC); err != nil || fresh.Builds() != 0 {
		t.Fatalf("restart rebuilt: %d %v", fresh.Builds(), err)
	}
	if _, err := e.st.W.Exec(`UPDATE tracks SET title = ? WHERE id = ?`, "Renamed Twin", id); err != nil {
		t.Fatal(err)
	}
	r3, err := e.svc.Ensure(ctx, id, KindFLAC)
	if err != nil || r3.SHA256 == r1.SHA256 || e.svc.Builds() != 2 || r3.Filename != "renamed_twin.flac" {
		t.Fatalf("title change: %+v builds %d err %v", r3, e.svc.Builds(), err)
	}
	if _, err := e.data.Stat(r1.Path); err == nil {
		t.Errorf("the replaced file was left behind")
	}
	if b := e.read(t, r3.Path); !bytes.Contains(b, []byte("TITLE=Renamed Twin")) {
		t.Errorf("new title missing")
	}
	// a file removed under the row is rebuilt
	if err := e.data.Remove(r3.Path); err != nil {
		t.Fatal(err)
	}
	if r4, err := e.svc.Ensure(ctx, id, KindFLAC); err != nil || r4.SHA256 != r3.SHA256 || e.svc.Builds() != 3 {
		t.Fatalf("rebuild after removal: builds %d err %v", e.svc.Builds(), err)
	}
}

func TestEnsureSingleflight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	gate := make(chan struct{})
	e.svc.hold = func() { <-gate }
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Ensure(context.Background(), lt.TrackTwinA, KindFLAC)
			errs <- err
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := e.svc.Builds(); n != 1 {
		t.Fatalf("builds = %d, want 1", n)
	}
}

func TestEnsureCapAnswersBusy(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{Cap: 1, Queue: -1})
	gate, in := make(chan struct{}), make(chan struct{}, 2)
	e.svc.hold = func() { in <- struct{}{}; <-gate }
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Ensure(context.Background(), lt.TrackTwinA, KindFLAC)
		done <- err
	}()
	<-in // the only slot is taken
	_, err := e.svc.Ensure(context.Background(), lt.TrackTwinB, KindMP3)
	var busy *ErrBusy
	if !errors.As(err, &busy) || busy.RetryAfter <= 0 {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// the slot is free again
	if _, err := e.svc.Ensure(context.Background(), lt.TrackTwinB, KindMP3); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureQueueWaitsForASlot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{Cap: 1, Queue: 1})
	gate, in := make(chan struct{}), make(chan struct{}, 4)
	e.svc.hold = func() { in <- struct{}{}; <-gate }
	res := make(chan error, 2)
	for _, id := range []string{lt.TrackTwinA, lt.TrackTwinB} {
		go func() { _, err := e.svc.Ensure(context.Background(), id, KindFLAC); res <- err }()
		if id == lt.TrackTwinA {
			<-in
		}
	}
	time.Sleep(50 * time.Millisecond) // the second waits in the queue
	// a third finds slot and queue full
	if _, err := e.svc.Ensure(context.Background(), lt.TrackRefFlac, KindFLAC); !errors.As(err, new(*ErrBusy)) {
		t.Fatalf("third: %v", err)
	}
	// a cancelled waiter gives its queue place back
	close(gate)
	for range 2 {
		if err := <-res; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureCancelledWaiter(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{Cap: 1, Queue: 1})
	gate, in := make(chan struct{}), make(chan struct{}, 4)
	e.svc.hold = func() { in <- struct{}{}; <-gate }
	go func() { _, _ = e.svc.Ensure(context.Background(), lt.TrackTwinA, KindFLAC) }()
	<-in
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { _, err := e.svc.Ensure(ctx, lt.TrackTwinB, KindFLAC); res <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	close(gate)
}

func TestEnsureFailsClosed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	ctx := context.Background()
	if _, err := e.svc.Ensure(ctx, "01900000-0000-7000-8000-0000000fffff", KindFLAC); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
	if _, err := e.svc.Ensure(ctx, lt.TrackTwinA, "mp3_320"); !errors.As(err, new(*ErrUnavailable)) {
		t.Errorf("the master-derived kind must be refused: %v", err)
	}
	var nilSvc *Service
	if _, err := nilSvc.Ensure(ctx, lt.TrackTwinA, KindFLAC); !errors.As(err, new(*ErrUnavailable)) {
		t.Errorf("nil service: %v", err)
	}
	// a master that is not a FLAC file: nothing is cached or served
	put(t, e.data, e.taints[lt.TrackRefFlac].MasterPath, []byte("not a flac file at all"))
	if _, err := e.svc.Ensure(ctx, lt.TrackRefFlac, KindFLAC); !errors.As(err, new(*ErrUnavailable)) {
		t.Errorf("corrupt master: %v", err)
	}
	var n int
	_ = e.st.R.QueryRow(`SELECT COUNT(*) FROM renditions WHERE track_id = ? AND kind = ?`, lt.TrackRefFlac, KindFLAC).Scan(&n)
	if n != 0 {
		t.Errorf("a failed build left a row")
	}
	// a missing master
	if err := e.data.Remove(e.taints[lt.TrackTwinB].MasterPath); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(ctx, lt.TrackTwinB, KindFLAC); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("missing master: %v", err)
	}
	// a lossless track without its mp3_320 rendition
	if _, err := e.st.W.Exec(`DELETE FROM renditions WHERE 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE renditions SET kind = 'mp3_x' WHERE track_id = ? AND kind = 'mp3_320'`, lt.TrackTwinA); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(ctx, lt.TrackTwinA, KindMP3); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("no mp3_320 row: %v", err)
	}
	// an mp3_320 source that is not an MP3
	put(t, e.data, e.taints[lt.TrackRefFlac].MasterPath, []byte("x"))
	var rp string
	_ = e.st.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, lt.TrackRefFlac).Scan(&rp)
	put(t, e.data, rp, bytes.Repeat([]byte{7}, 400))
	if _, err := e.svc.Ensure(ctx, lt.TrackRefFlac, KindMP3); !errors.As(err, new(*ErrUnavailable)) {
		t.Errorf("corrupt mp3 source: %v", err)
	}
	// a missing source file of an existing row
	if err := e.data.Remove(rp); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ensure(ctx, lt.TrackRefFlac, KindMP3); !errors.Is(err, library.ErrMediaUnavailable) {
		t.Errorf("missing mp3 source: %v", err)
	}
}

func put(t *testing.T, data *os.Root, rel string, b []byte) {
	t.Helper()
	_ = data.Chmod(rel, 0o644)
	if err := data.WriteFile(rel, b, 0o444); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	ctx := context.Background()
	rf, err := e.svc.Ensure(ctx, lt.TrackTwinA, KindFLAC)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := e.svc.Ensure(ctx, lt.TrackTwinA, KindMP3)
	if err != nil {
		t.Fatal(err)
	}
	o, err := e.svc.Resolve(ctx, rf.SHA256, "flac")
	if err != nil || o.RelPath != rf.Path || o.ContentType != "audio/flac" || o.ETag != rf.SHA256 || o.DownloadName != "" {
		t.Fatalf("flac: %+v %v", o, err)
	}
	if o2 := rf.Object(); o2.RelPath != o.RelPath || o2.ETag != o.ETag {
		t.Errorf("Object(): %+v", o2)
	}
	if o, err = e.svc.Resolve(ctx, rm.SHA256, "mp3"); err != nil || o.ContentType != "audio/mpeg" {
		t.Fatalf("mp3: %+v %v", o, err)
	}
	// the master's sha is not a rendition: a Listener holding no master address cannot reach it, and neither can a guess
	for name, c := range map[string][2]string{
		"master sha": {e.taints[lt.TrackTwinA].MasterSHA, "flac"}, "wrong ext": {rf.SHA256, "mp3"}, "bad ext": {rf.SHA256, "jpg"}, "unknown": {strings.Repeat("a", 64), "flac"},
	} {
		if _, err := e.svc.Resolve(ctx, c[0], c[1]); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := e.data.Remove(rf.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Resolve(ctx, rf.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("a removed file: %v", err)
	}
	var nilSvc *Service
	if _, err := nilSvc.Resolve(ctx, rf.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("nil: %v", err)
	}
}

func TestSanitize(t *testing.T) {
	t.Parallel()
	if got := sanitize("  a\x00b\x7fc\xffd  "); got != "a b c�d" {
		t.Errorf("%q", got)
	}
	if got := sanitize(strings.Repeat("é", 700)); len(got) > maxTitleBytes || !strings.HasPrefix(got, "éé") {
		t.Errorf("len %d", len(got))
	}
	if !IsKind(KindFLAC) || !IsKind(KindMP3) || IsKind("mp3_320") {
		t.Errorf("IsKind")
	}
	var e error = &ErrBusy{}
	if e.Error() == "" || (&ErrUnavailable{Err: io.EOF}).Error() == "" || !errors.Is(&ErrUnavailable{Err: io.EOF}, io.EOF) {
		t.Errorf("error text")
	}
}
