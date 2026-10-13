package clip_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// env is a throwaway DataDir with an os.Root.
type env struct {
	dir  string
	root *os.Root
}

func newEnv(t testing.TB) *env {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &env{dir: dir, root: root}
}

func (e *env) abs(rel string) string { return filepath.Join(e.dir, filepath.FromSlash(rel)) }

// putMaster stores b as a frozen master (mode 0444) and returns its spec skeleton.
func (e *env) putMaster(t testing.TB, rel string, b []byte, rate, ch int) clip.Spec {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.abs(rel), b, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return clip.Spec{MasterSHA256: hex.EncodeToString(sum[:]), MasterPath: rel, SampleRate: rate, Channels: ch,
		W: clip.Window{StartMS: 5000, EndMS: 35000}}
}

// fakeMaster is a header-valid FLAC the fake render path never decodes.
func (e *env) fakeMaster(t testing.TB, name string) clip.Spec {
	t.Helper()
	b := flactest.Build(flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: 48000 * 60,
		Frames: []byte("master-" + name)})
	return e.putMaster(t, "audio/masters/"+name+".flac", b, 48000, 2)
}

func (e *env) cachedPaths(t testing.TB) []string {
	t.Helper()
	m, _ := filepath.Glob(e.abs("derived/clips/*/*.flac"))
	return m
}

func (e *env) workDirs(t testing.TB) []string {
	t.Helper()
	m, _ := filepath.Glob(e.abs("incoming/clip-*"))
	return m
}

// realTools requires ffmpeg and builds the analyzer.
func realTools(t testing.TB) (mediatool.Tools, *ffmpeg.FFmpeg) {
	t.Helper()
	testtools.Require(t, "ffmpeg")
	tools := testtools.Tools()
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return tools, an
}

// lavfiMaster renders a FLAC (24 bit) from an aevalsrc expression list into the env.
func lavfiMaster(t testing.TB, e *env, tools mediatool.Tools, rel, exprs string, rate int, seconds int) clip.Spec {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-f", "lavfi", "-i",
		"aevalsrc='" + exprs + "':s=" + itoa(rate) + ":d=" + itoa(seconds),
		"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-f", "flac", "file:" + e.abs(rel)}
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, args, mediatool.RunOpts{Timeout: 60e9, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs(rel), 0o444); err != nil {
		t.Fatal(err)
	}
	ch := 1
	if bytes.Contains([]byte(exprs), []byte("|")) {
		ch = 2
	}
	return e.putMasterBytes(rel, b, rate, ch)
}

func (e *env) putMasterBytes(rel string, b []byte, rate, ch int) clip.Spec {
	sum := sha256.Sum256(b)
	return clip.Spec{MasterSHA256: hex.EncodeToString(sum[:]), MasterPath: rel, SampleRate: rate, Channels: ch,
		W: clip.Window{StartMS: 5000, EndMS: 35000}}
}

func itoa(n int) string { return strconv.Itoa(n) }

// decode returns the interleaved float samples of a FLAC file and its channel count.
func decode(t testing.TB, tools mediatool.Tools, path string, channels int) []float32 {
	t.Helper()
	out, err := mediatool.Run(context.Background(), tools.FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error",
		"-i", "file:" + path, "-f", "f32le", "-acodec", "pcm_f32le", "-"}, mediatool.RunOpts{Timeout: 60e9, MaxStdout: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	s := make([]float32, len(out)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:]))
	}
	_ = channels
	return s
}

func streamInfo(t testing.TB, path string) flacmeta.StreamInfo {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return info.StreamInfo
}

func mustAnalyzer(t testing.TB, tools mediatool.Tools) *ffmpeg.FFmpeg {
	t.Helper()
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return an
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nlink(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Nlink) //nolint:unconvert,gosec // Nlink width differs per platform
	}
	return 0
}
