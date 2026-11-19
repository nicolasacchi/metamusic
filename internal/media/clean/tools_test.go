package clean

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func run(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	out, err := mediatool.Run(context.Background(), bin, args, mediatool.RunOpts{Timeout: 60e9, MaxStdout: 64 << 20})
	if err != nil {
		t.Fatalf("%s %v: %v", filepath.Base(bin), args[:min(len(args), 2)], err)
	}
	return out
}

// TestFlacCleanWithRealTools is the media-tools tier of C308.7: a real FLAC master carrying MM_* tags, other comments and
// a picture becomes a flac_clean that the reference decoder accepts (flac -t), whose decoded audio is identical to the
// master's, and whose metadata (read by metaflac, an independent parser) is STREAMINFO, SEEKTABLE and one VORBIS_COMMENT
// with TITLE alone.
func TestFlacCleanWithRealTools(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tl := testtools.Tools()
	e := newEnv(t, Options{})
	id := lt.TrackTwinA
	dir := t.TempDir()
	master := filepath.Join(dir, "master.flac")
	run(t, tl.FFmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=330:duration=2:sample_rate=48000", "-c:a", "flac", "-sample_fmt", "s32", master)
	png := filepath.Join(dir, "cover.png")
	run(t, tl.FFmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=16x16", "-frames:v", "1", png)
	run(t, tl.Metaflac, "--set-tag=MM_SEED=42", "--set-tag=MM_REQUEST_ID=req-1", "--set-tag=LYRICS="+"private words", "--set-tag=ARTIST=someone",
		"--import-picture-from="+png, master)
	b, err := os.ReadFile(master)
	if err != nil {
		t.Fatal(err)
	}
	var p string
	if err := e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	put(t, e.data, p, b)
	if _, err := e.st.W.Exec(`UPDATE tracks SET master_sha256 = ?, master_bytes = ? WHERE id = ?`, sum(b), len(b), id); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.Ensure(context.Background(), id, KindFLAC)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.dir, filepath.FromSlash(r.Path))
	run(t, tl.FLAC, "-t", "-s", out)
	if got, want := run(t, tl.FLAC, "-d", "-c", "-s", out), run(t, tl.FLAC, "-d", "-c", "-s", master); !bytes.Equal(got, want) || len(want) == 0 {
		t.Errorf("decoded audio differs from the master's (%d vs %d bytes)", len(got), len(want))
	}
	list := string(run(t, tl.Metaflac, "--list", out))
	for _, bad := range []string{"MM_", "LYRICS", "ARTIST", "PICTURE", "private", "someone"} {
		if strings.Contains(list, bad) {
			t.Errorf("metaflac lists %q in the clean file", bad)
		}
	}
	if !strings.Contains(list, "TITLE=") || strings.Count(list, "type: 4 (VORBIS_COMMENT)") != 1 {
		t.Errorf("metaflac listing:\n%s", list)
	}
	md5 := func(f string) string {
		return strings.TrimSpace(string(run(t, tl.Metaflac, "--show-md5sum", f)))
	}
	if md5(out) != md5(master) {
		t.Errorf("STREAMINFO MD5 differs")
	}
}
