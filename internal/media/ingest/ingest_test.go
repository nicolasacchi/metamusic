package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

var (
	srcCacheDir string
	srcMu       sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if srcCacheDir != "" {
		_ = os.RemoveAll(srcCacheDir)
	}
	os.Exit(code)
}

var recordedAt = time.Date(2026, 10, 12, 9, 7, 30, 0, time.UTC)

// source returns a fresh copy (in a test temp dir) of a cached synthetic FLAC.
func source(t *testing.T, style synth.FLACStyle, seconds float64, variant int) string {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	srcMu.Lock()
	if srcCacheDir == "" {
		d, err := os.MkdirTemp("", "ingest-src-")
		if err != nil {
			srcMu.Unlock()
			t.Fatal(err)
		}
		srcCacheDir = d
	}
	name := filepath.Join(srcCacheDir, fmt.Sprintf("s%d-%v-%d.flac", style, seconds, variant))
	if _, err := os.Stat(name); err != nil {
		tone := synth.Tone{LUFS: -20, Seconds: seconds, BPM: 120, Variant: variant}
		if err := synth.FLAC(context.Background(), testtools.Tools(), name, tone, style); err != nil {
			srcMu.Unlock()
			t.Fatal(err)
		}
	}
	srcMu.Unlock()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "in.flac")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type env struct {
	t       *testing.T
	dir     string
	root    *os.Root
	p       *Pipeline
	tools   mediatool.Tools
	an      analysis.Analyzer
	idCount int
}

func newEnv(t *testing.T, tools mediatool.Tools, an analysis.Analyzer) *env {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if an == nil {
		an, err = ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
	}
	return &env{t: t, dir: dir, root: root, tools: tools, an: an,
		p: New(root, dir, tools, an, func() time.Time { return recordedAt })}
}

func realEnv(t *testing.T) *env {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	return newEnv(t, testtools.Tools(), nil)
}

// request opens src read-only and builds a Request. The file is closed at cleanup.
func (e *env) request(src string, mut ...func(*Request)) Request {
	e.t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		e.t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = f.Close() })
	e.idCount++
	req := Request{
		TrackID: fmt.Sprintf("0192f000-0000-7000-8000-%012d", e.idCount), Title: "Città è <&>",
		Slug: "demo", RecipeID: "r-1", RequestID: "req-1", Seed: 1101,
		Source: f, SourceSHA256: sha(b), RecordedAt: recordedAt,
	}
	for _, m := range mut {
		m(&req)
	}
	return req
}

func (e *env) ingest(src string, mut ...func(*Request)) (Request, Result) {
	e.t.Helper()
	req := e.request(src, mut...)
	res, err := e.p.Ingest(context.Background(), req)
	if err != nil {
		e.t.Fatal(err)
	}
	return req, res
}

func (e *env) files() []string {
	var out []string
	_ = filepath.WalkDir(e.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			r, _ := filepath.Rel(e.dir, p)
			out = append(out, r)
		}
		return nil
	})
	return out
}

func (e *env) readInfo(rel string) (flacmeta.Info, string) {
	e.t.Helper()
	f, err := os.Open(filepath.Join(e.dir, rel))
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		e.t.Fatal(err)
	}
	fr, err := flacmeta.FramesSHA256(f, st.Size(), info.FramesOffset)
	if err != nil {
		e.t.Fatal(err)
	}
	return info, fr
}

var styles = []struct {
	name  string
	style synth.FLACStyle
}{{"ffmpeg", synth.StyleFFmpeg}, {"yue2", synth.StyleYuE2}}

func TestFreeze_PreservesStreamInfoMD5(t *testing.T) {
	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			e := realEnv(t)
			src := source(t, s.style, 4, 0)
			before, _ := e.readInfoAt(src)
			_, res := e.ingest(src)
			after, _ := e.readInfo(res.MasterRelPath)
			if before.StreamInfo.MD5 != after.StreamInfo.MD5 || before.StreamInfo.TotalSamples != after.StreamInfo.TotalSamples {
				t.Fatalf("STREAMINFO changed: %+v -> %+v", before.StreamInfo, after.StreamInfo)
			}
			if res.AudioMD5 != hex.EncodeToString(after.StreamInfo.MD5[:]) {
				t.Fatal("AudioMD5")
			}
		})
	}
}

func (e *env) readInfoAt(abs string) (flacmeta.Info, string) {
	e.t.Helper()
	rel, err := filepath.Rel(e.dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		f, err := os.Open(abs)
		if err != nil {
			e.t.Fatal(err)
		}
		defer f.Close()
		st, _ := f.Stat()
		info, err := flacmeta.Read(f, st.Size())
		if err != nil {
			e.t.Fatal(err)
		}
		fr, _ := flacmeta.FramesSHA256(f, st.Size(), info.FramesOffset)
		return info, fr
	}
	return e.readInfo(rel)
}

func TestFreeze_PreservesFrameBytes(t *testing.T) {
	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			e := realEnv(t)
			src := source(t, s.style, 4, 0)
			_, pre := e.readInfoAt(src)
			_, res := e.ingest(src)
			_, post := e.readInfo(res.MasterRelPath)
			if pre != post || res.FramesSHA256 != pre {
				t.Fatalf("frames hash changed: %s -> %s (result %s)", pre, post, res.FramesSHA256)
			}
		})
	}
}

func TestFreeze_ExactlyOneVorbisComment(t *testing.T) {
	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			e := realEnv(t)
			_, res := e.ingest(source(t, s.style, 4, 0))
			info, _ := e.readInfo(res.MasterRelPath)
			if n := info.Count(flacmeta.BlockVorbisComment); n != 1 {
				t.Fatalf("%d VORBIS_COMMENT blocks", n)
			}
			if info.Vendor == "" {
				t.Fatal("vendor string lost")
			}
		})
	}
}

func TestFreeze_TagsWrittenOnce(t *testing.T) {
	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			e := realEnv(t)
			req, res := e.ingest(source(t, s.style, 4, 0))
			info, _ := e.readInfo(res.MasterRelPath)
			var keys []string
			for _, c := range info.Comments {
				keys = append(keys, c[:strings.IndexByte(c, '=')])
			}
			want := []string{"TITLE", "MM_TAKE_ID", "MM_RECIPE_ID", "MM_REQUEST_ID", "MM_SEED",
				"REPLAYGAIN_TRACK_GAIN", "REPLAYGAIN_TRACK_PEAK", "REPLAYGAIN_REFERENCE_LOUDNESS"}
			if !slices.Equal(keys, want) {
				t.Fatalf("keys = %v", keys)
			}
			if info.Comments[0] != "TITLE=Città è <&>" {
				t.Fatalf("title = %q", info.Comments[0])
			}
			if info.Comments[1] != "MM_TAKE_ID="+req.TrackID || info.Comments[4] != "MM_SEED=1101" ||
				info.Comments[7] != "REPLAYGAIN_REFERENCE_LOUDNESS=-18.00 LUFS" {
				t.Fatalf("comments = %q", info.Comments)
			}
		})
	}
}

func TestFreeze_SeekTable(t *testing.T) {
	for _, s := range styles {
		t.Run(s.name, func(t *testing.T) {
			e := realEnv(t)
			_, res := e.ingest(source(t, s.style, 12, 0))
			info, _ := e.readInfo(res.MasterRelPath)
			if info.Count(flacmeta.BlockSeekTable) != 1 || len(info.SeekPoints) < 2 {
				t.Fatalf("seektable: %d blocks, %d points", info.Count(flacmeta.BlockSeekTable), len(info.SeekPoints))
			}
			if info.SeekPoints[0].Sample != 0 {
				t.Fatalf("first point %d", info.SeekPoints[0].Sample)
			}
			limit := 2*uint64(info.StreamInfo.SampleRate) + uint64(info.StreamInfo.MaxBlock)
			for i := 1; i < len(info.SeekPoints); i++ {
				if gap := info.SeekPoints[i].Sample - info.SeekPoints[i-1].Sample; gap > limit {
					t.Fatalf("gap %d > %d at %d", gap, limit, i)
				}
			}
		})
	}
}

func TestFreeze_Mode0444AndShaAddress(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleYuE2, 4, 0)
	var workMode fs.FileMode
	var finalExisted = true
	beforeClaim = func(id string) {
		st, err := os.Stat(filepath.Join(e.dir, "incoming", "ingest-"+id, "master.flac"))
		if err != nil {
			t.Error(err)
			return
		}
		workMode = st.Mode().Perm()
		_, err = os.Stat(filepath.Join(e.dir, "audio", "masters", "demo__1101__20261012-0907.flac"))
		finalExisted = err == nil
	}
	defer func() { beforeClaim = nil }()
	_, res := e.ingest(src)
	beforeClaim = nil
	if workMode != 0o444 || finalExisted {
		t.Fatalf("at claim time: mode %v, final existed %v", workMode, finalExisted)
	}
	if res.MasterRelPath != "audio/masters/demo__1101__20261012-0907.flac" {
		t.Fatal(res.MasterRelPath)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, res.MasterRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if sha(b) != res.MasterSHA256 || int64(len(b)) != res.MasterBytes {
		t.Fatal("sha or size mismatch")
	}
	st, _ := os.Stat(filepath.Join(e.dir, res.MasterRelPath))
	if st.Mode().Perm() != 0o444 {
		t.Fatalf("mode %v", st.Mode())
	}
	_, res2 := e.ingest(src)
	if res2.MasterRelPath != "audio/masters/demo__1101__20261012-0907__2.flac" {
		t.Fatal(res2.MasterRelPath)
	}
}

func TestIngest_SourceUntouched(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	inbox := filepath.Dir(src)
	if err := os.Chmod(src, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(inbox, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inbox, 0o755) })
	stat := func() (string, fs.FileMode, time.Time, int64, fs.FileMode) {
		b, _ := os.ReadFile(src)
		st, _ := os.Stat(src)
		dst, _ := os.Stat(inbox)
		return sha(b), st.Mode(), st.ModTime(), st.Size(), dst.Mode()
	}
	a1, a2, a3, a4, a5 := stat()
	e.ingest(src)
	b1, b2, b3, b4, b5 := stat()
	if a1 != b1 || a2 != b2 || !a3.Equal(b3) || a4 != b4 || a5 != b5 {
		t.Fatal("source or inbox changed")
	}
	ents, _ := os.ReadDir(inbox)
	if len(ents) != 1 {
		t.Fatalf("inbox has %d entries", len(ents))
	}
}

func TestIngest_SourceChangedAbort(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	req := e.request(src, func(r *Request) { r.SourceSHA256 = strings.Repeat("0", 64) })
	_, err := e.p.Ingest(context.Background(), req)
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeSourceChanged {
		t.Fatalf("err = %v", err)
	}
	e.assertEmpty()
}

func (e *env) assertEmpty() {
	e.t.Helper()
	if f := e.files(); len(f) != 0 {
		e.t.Fatalf("files left behind: %v", f)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(ents) != 0 {
		e.t.Fatalf("incoming not empty: %v", ents)
	}
}

func TestIngest_InvalidAudioRejected(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 12, 0)
	b, _ := os.ReadFile(src)
	if err := os.WriteFile(src, b[:len(b)*60/100], 0o644); err != nil {
		t.Fatal(err)
	}
	req := e.request(src)
	_, err := e.p.Ingest(context.Background(), req)
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeInvalidAudio {
		t.Fatalf("err = %v", err)
	}
	e.assertEmpty()
}

// overrideTP wraps an analyzer and replaces the measured true peak.
type overrideTP struct {
	analysis.Analyzer
	tp float64
}

func (o overrideTP) Loudness(ctx context.Context, p string) (analysis.Loudness, error) {
	l, err := o.Analyzer.Loudness(ctx, p)
	l.TruePeakDBTP = o.tp
	return l, err
}

// logWrappers returns tools whose binaries append their argv to a log and exec the rl ones.
func logWrappers(t *testing.T) (mediatool.Tools, string) {
	t.Helper()
	rl := testtools.Tools()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	mk := func(name, bin string) string {
		p := filepath.Join(dir, name)
		s := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; echo '--'; } >> '" + log + "'\nexec '" + bin + "' \"$@\"\n"
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return mediatool.Tools{FFmpeg: mk("ffmpeg", rl.FFmpeg), FLAC: mk("flac", rl.FLAC), Metaflac: mk("metaflac", rl.Metaflac)}, log
}

type mp3Frame struct{ bitrate, rate int }

// parseMP3 walks MPEG-1 Layer III frame headers.
func parseMP3(t *testing.T, b []byte) []mp3Frame {
	t.Helper()
	if len(b) > 10 && string(b[:3]) == "ID3" {
		n := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
		b = b[10+n:]
	}
	br := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	sr := []int{44100, 48000, 32000, 0}
	var out []mp3Frame
	for len(b) >= 4 {
		if b[0] != 0xFF || b[1]&0xFE != 0xFA { // sync + MPEG1 + layer III (CRC bit ignored)
			if len(out) > 0 && len(b) < 2000 {
				break // trailing tag
			}
			t.Fatalf("lost sync after %d frames", len(out))
		}
		bi, si := int(b[2]>>4), int(b[2]>>2&3)
		pad := int(b[2] >> 1 & 1)
		f := mp3Frame{br[bi], sr[si]}
		if f.bitrate == 0 || f.rate == 0 {
			t.Fatalf("bad header %x", b[:4])
		}
		out = append(out, f)
		n := 144*f.bitrate*1000/f.rate + pad
		if n > len(b) {
			break
		}
		b = b[n:]
	}
	return out
}

func TestIngest_MP3Rendition(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	src := source(t, synth.StyleFFmpeg, 4, 0)
	tools, log := logWrappers(t)
	rl, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	argvOf := func() string { b, _ := os.ReadFile(log); return string(b) }
	mp3Gain := func(tp float64) (Result, bool) {
		e := newEnv(t, tools, overrideTP{rl, tp})
		_ = os.Remove(log)
		_, res := e.ingest(src)
		return res, strings.Contains(argvOf(), "-af\nvolume=")
	}

	res, hasAF := mp3Gain(-0.2)
	if !hasAF || !strings.Contains(argvOf(), "volume=-0.800dB") {
		t.Fatalf("expected volume=-0.800dB, argv:\n%s", argvOf())
	}
	if g := res.MP3.Params["gain_db"].(float64); g < -0.801 || g > -0.799 {
		t.Fatalf("gain_db = %v", g)
	}
	if !strings.Contains(argvOf(), "libmp3lame") {
		t.Fatal("no libmp3lame")
	}
	e0 := newEnv(t, tools, overrideTP{rl, -0.2})
	_, a := e0.ingest(src)
	fr := parseMP3(t, mustRead(t, filepath.Join(e0.dir, a.MP3.RelPath)))
	if len(fr) < 50 {
		t.Fatalf("%d frames", len(fr))
	}
	for i, f := range fr {
		if f.bitrate != 320 || f.rate != 48000 {
			t.Fatalf("frame %d: %+v", i, f)
		}
	}
	if a.MP3.Params["encoder"] != "libmp3lame" || a.MP3.Params["bitrate"] != "320k" ||
		a.MP3.Params["sample_rate"] != 48000 || a.MP3.Params["ffmpeg"] != strings.TrimPrefix(rl.ToolVersion(), "ffmpeg ") {
		t.Fatalf("params = %v", a.MP3.Params)
	}

	// quiet source: no -af, gain 0
	res, hasAF = mp3Gain(-12)
	if hasAF || res.MP3.Params["gain_db"].(float64) != 0 {
		t.Fatalf("quiet source got a gain: %v", res.MP3.Params)
	}

	// two runs in separate libraries produce the same bytes
	e1, e2 := newEnv(t, tools, overrideTP{rl, -0.2}), newEnv(t, tools, overrideTP{rl, -0.2})
	_, r1 := e1.ingest(src)
	_, r2 := e2.ingest(src)
	if r1.MP3.SHA256 != r2.MP3.SHA256 || r1.MP3.SHA256 != a.MP3.SHA256 {
		t.Fatal("MP3 is not deterministic")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngest_PeaksAndAnalysesInResult(t *testing.T) {
	e := realEnv(t)
	_, res := e.ingest(source(t, synth.StyleFFmpeg, 4, 0))
	if !regexp.MustCompile(`^ffmpeg \S+$`).MatchString(res.ToolVersion) {
		t.Fatalf("ToolVersion = %q", res.ToolVersion)
	}
	if v := analysis.LoudnessDef + " " + res.ToolVersion; !regexp.MustCompile(`^mm-loudness/1 ffmpeg \S+$`).MatchString(v) {
		t.Fatal(v)
	}
	if v := analysis.SpectrumDef + " " + res.ToolVersion; !regexp.MustCompile(`^mm-spectrum/1 ffmpeg \S+$`).MatchString(v) {
		t.Fatal(v)
	}
	if d := res.Loudness.IntegratedLUFS + 20; d < -0.2 || d > 0.2 {
		t.Fatalf("I = %v", res.Loudness.IntegratedLUFS)
	}
	if res.Spectrum.Frames == 0 {
		t.Fatal("no spectrum frames")
	}
	b := mustRead(t, filepath.Join(e.dir, res.Peaks.RelPath))
	if sha(b) != res.Peaks.SHA256 || res.Peaks.Bytes != int64(len(b)) || !bytes.HasSuffix(b, []byte("\n")) {
		t.Fatal("peaks rendition mismatch")
	}
	var pj struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(b, &pj); err != nil || pj.Format != analysis.PeaksFormat {
		t.Fatalf("peaks json: %v %q", err, pj.Format)
	}
	if res.Peaks.RelPath != "derived/"+res.Peaks.SHA256[:2]+"/"+res.Peaks.SHA256+".peaks.json" ||
		res.MP3.RelPath != "derived/"+res.MP3.SHA256[:2]+"/"+res.MP3.SHA256+".mp3" {
		t.Fatal("derived names")
	}
	for _, rel := range []string{res.Peaks.RelPath, res.MP3.RelPath} {
		if st, _ := os.Stat(filepath.Join(e.dir, rel)); st.Mode().Perm() != 0o444 {
			t.Fatalf("%s mode %v", rel, st.Mode())
		}
	}
	if res.Peaks.Params["format"] != "mm.peaks/1" {
		t.Fatal("peaks params")
	}
}

func TestIngest_ArtifactsCopied0444(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	dir := t.TempDir()
	contents := map[string]string{"score.abc": "X:1\nK:C\nCDEF|\n", "latent.npy": "binary\x00data"}
	var arts []ArtifactSrc
	for name, c := range contents {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		arts = append(arts, ArtifactSrc{Name: name, File: f, SHA256: sha([]byte(c))})
	}
	req, res := e.ingest(src, func(r *Request) { r.Artifacts = arts })
	if len(res.Artifacts) != 2 {
		t.Fatalf("%d artifacts", len(res.Artifacts))
	}
	for _, a := range res.Artifacts {
		name := filepath.Base(a.RelPath)
		if a.Kind != "artifact" || a.RelPath != "takes/"+req.TrackID+"/"+name || a.SHA256 != sha([]byte(contents[name])) {
			t.Fatalf("%+v", a)
		}
		p := filepath.Join(e.dir, a.RelPath)
		if string(mustRead(t, p)) != contents[name] {
			t.Fatal("content")
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o444 {
			t.Fatalf("mode %v", st.Mode())
		}
		if !slices.Contains(res.Created, a.RelPath) {
			t.Fatal("not in Created")
		}
	}

	// a changed artifact aborts and leaves nothing
	e2 := realEnv(t)
	p := filepath.Join(dir, "score.abc")
	f, _ := os.Open(p)
	t.Cleanup(func() { _ = f.Close() })
	r2 := e2.request(src, func(r *Request) {
		r.Artifacts = []ArtifactSrc{{Name: "score.abc", File: f, SHA256: strings.Repeat("a", 64)}}
	})
	_, err := e2.p.Ingest(context.Background(), r2)
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeSourceChanged {
		t.Fatalf("err = %v", err)
	}
	e2.assertEmpty()
}

func TestIngest_ReplayGainFormatting(t *testing.T) {
	for _, c := range []struct {
		i, tp      float64
		gain, peak string
	}{
		{-12.34, -1, "-5.66 dB", "0.891251"},
		{-20, -6.0206, "+2.00 dB", "0.500000"},
		{-70, -120, "+24.00 dB", "0.000001"},
		{-300, -300, "+24.00 dB", "0.000000"},
		{20, 0, "-24.00 dB", "1.000000"},
	} {
		g, p := replayGain(analysis.Loudness{IntegratedLUFS: c.i, TruePeakDBTP: c.tp})
		gs, ps := replayGainStrings(g, p)
		if gs != c.gain || ps != c.peak {
			t.Errorf("I=%v TP=%v: %q %q, want %q %q", c.i, c.tp, gs, ps, c.gain, c.peak)
		}
	}
	e := realEnv(t)
	_, res := e.ingest(source(t, synth.StyleFFmpeg, 4, 0))
	info, _ := e.readInfo(res.MasterRelPath)
	g, p := replayGainStrings(res.ReplayGainDB, res.ReplayGainPeak)
	if info.Comments[5] != "REPLAYGAIN_TRACK_GAIN="+g || info.Comments[6] != "REPLAYGAIN_TRACK_PEAK="+p {
		t.Fatalf("tags %q vs %s %s", info.Comments[5:7], g, p)
	}
}

func TestIngest_ConcurrentSameNameNoClobber(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	var wg sync.WaitGroup
	wg.Add(2)
	var reached sync.WaitGroup
	reached.Add(2)
	release := make(chan struct{})
	beforeClaim = func(string) {
		reached.Done()
		<-release
	}
	defer func() { beforeClaim = nil }()
	reqs := []Request{e.request(src), e.request(src, func(r *Request) { r.Title = "Second" })}
	results := make([]Result, 2)
	errs := make([]error, 2)
	for i := range reqs {
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.p.Ingest(context.Background(), reqs[i])
		}()
	}
	reached.Wait()
	close(release)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	names := []string{results[0].MasterRelPath, results[1].MasterRelPath}
	slices.Sort(names)
	if names[0] != "audio/masters/demo__1101__20261012-0907.flac" || names[1] != "audio/masters/demo__1101__20261012-0907__2.flac" {
		t.Fatalf("names = %v", names)
	}
	for _, r := range results {
		p := filepath.Join(e.dir, r.MasterRelPath)
		if sha(mustRead(t, p)) != r.MasterSHA256 {
			t.Fatalf("%s sha does not match its Result", r.MasterRelPath)
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o444 {
			t.Fatal("mode")
		}
	}
}

func TestIngest_DiscardOnlyOwnFiles(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	artFile := func(content string) ArtifactSrc {
		p := filepath.Join(t.TempDir(), "score.abc")
		_ = os.WriteFile(p, []byte(content), 0o644)
		f, _ := os.Open(p)
		t.Cleanup(func() { _ = f.Close() })
		return ArtifactSrc{Name: "score.abc", File: f, SHA256: sha([]byte(content))}
	}
	_, a := e.ingest(src) // pre-existing master under the base name and derived files
	reqB, b := e.ingest(src, func(r *Request) { r.Artifacts = []ArtifactSrc{artFile("X:1\n")} })
	if b.MP3.SHA256 != a.MP3.SHA256 {
		t.Fatal("test needs identical MP3s")
	}
	want := []string{b.MasterRelPath, "takes/" + reqB.TrackID + "/score.abc"}
	if !slices.Equal(b.Created, want) {
		t.Fatalf("Created = %v, want %v", b.Created, want)
	}
	if err := e.p.Discard(b); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{b.MasterRelPath, "takes/" + reqB.TrackID} {
		if _, err := os.Stat(filepath.Join(e.dir, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s survived: %v", rel, err)
		}
	}
	for _, rel := range []string{a.MasterRelPath, a.MP3.RelPath, a.Peaks.RelPath} {
		if _, err := os.Stat(filepath.Join(e.dir, rel)); err != nil {
			t.Fatalf("%s removed: %v", rel, err)
		}
	}
	// Discard never touches paths outside the lists it is allowed to remove
	if err := e.p.Discard(Result{Created: []string{a.MP3.RelPath, a.MasterRelPath + "/../" + a.MasterRelPath, "incoming/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, a.MP3.RelPath)); err != nil {
		t.Fatal("derived file removed by Discard")
	}
	if _, err := os.Stat(filepath.Join(e.dir, a.MasterRelPath)); err != nil {
		t.Fatal("unclean path removed a master")
	}
}

func TestIngest_HostileTagsSanitised(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools, log := logWrappers(t)
	e := newEnv(t, tools, nil)
	src := source(t, synth.StyleYuE2, 4, 0)
	title := "--flag=\x00x\ry\nz\tw\xff\u0085v TITLE-SECRET"
	_, res := e.ingest(src, func(r *Request) { r.Title = title; r.RequestID = "REQID-SECRET\n=1" })
	info, _ := e.readInfo(res.MasterRelPath)
	if len(info.Comments) != 8 {
		t.Fatalf("%d comments: %q", len(info.Comments), info.Comments)
	}
	if want := "TITLE=--flag= x y z w� v TITLE-SECRET"; info.Comments[0] != want {
		t.Fatalf("title = %q, want %q", info.Comments[0], want)
	}
	if info.Comments[3] != "MM_REQUEST_ID=REQID-SECRET =1" {
		t.Fatalf("request id = %q", info.Comments[3])
	}
	argv := string(mustRead(t, log))
	for _, secret := range []string{"TITLE-SECRET", "REQID-SECRET", "--flag=", "Citt"} {
		if strings.Contains(argv, secret) {
			t.Fatalf("argv leaks %q", secret)
		}
	}
	if !strings.Contains(argv, "--import-tags-from=") {
		t.Fatal("metaflac not invoked as expected")
	}
}

func TestSanitizeTag(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  plain  ", 0, "plain"},
		{"a\x00b\x7fc\u0090d", 0, "a b c d"},
		{"bad\xc3(", 0, "bad�("},
		{strings.Repeat("é", 300), 200, strings.Repeat("é", 200)},
		{"ends " + strings.Repeat(" ", 10) + "x", 6, "ends"},
	} {
		if got := sanitizeTag(c.in, c.max); got != c.want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIngest_ToolTimeouts(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	src := source(t, synth.StyleFFmpeg, 4, 0)
	fake := filepath.Join(t.TempDir(), "flac")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := testtools.Tools()
	tools.FLAC = fake
	e := newEnv(t, tools, nil)
	e.p.to.flacTest = 300 * time.Millisecond
	req := e.request(src)
	start := time.Now()
	_, err := e.p.Ingest(context.Background(), req)
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeToolFailed {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 300*time.Millisecond+6*time.Second {
		t.Fatalf("took %v", d)
	}
	e.assertEmpty()
}

func TestIngest_ProgrammerErrors(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleFFmpeg, 4, 0)
	for name, mut := range map[string]func(*Request){
		"slug": func(r *Request) { r.Slug = "../x" },
		"id":   func(r *Request) { r.TrackID = "a/b" },
		"art":  func(r *Request) { r.Artifacts = []ArtifactSrc{{Name: "../x", SHA256: "0"}} },
	} {
		_, err := e.p.Ingest(context.Background(), e.request(src, mut))
		var ie *Error
		if !errors.As(err, &ie) || ie.Code != CodeIO {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.p.Ingest(context.Background(), e.request(src, func(r *Request) { r.Source = nil })); err == nil {
		t.Error("nil source accepted")
	}
	// a duplicate track id refuses to reuse a work dir
	req := e.request(src)
	if err := e.root.MkdirAll("incoming/ingest-"+req.TrackID, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.Ingest(context.Background(), req); err == nil {
		t.Error("existing work dir accepted")
	}
}

func TestIngest_Speed12s(t *testing.T) {
	e := realEnv(t)
	src := source(t, synth.StyleYuE2, 12, 0)
	req := e.request(src)
	start := time.Now()
	if _, err := e.p.Ingest(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	t.Logf("12 s track ingested in %v", time.Since(start))
}

func TestNoStubMarker(t *testing.T) {
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		if e.IsDir() || e.Name() == "ingest_test.go" || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if b, _ := os.ReadFile(e.Name()); bytes.Contains(b, []byte("STUB(M1-")) {
			t.Errorf("%s still has a STUB marker", e.Name())
		}
	}
}
