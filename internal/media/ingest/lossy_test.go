package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// mp3Source returns a fresh copy (in a test temp dir) of a cached synthetic MP3
// (2.000 s, stereo, 44.1 kHz, 128 kbps).
func mp3Source(t *testing.T, noID3 bool) string {
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
	name := filepath.Join(srcCacheDir, fmt.Sprintf("lossy-%v.mp3", noID3))
	if _, err := os.Stat(name); err != nil {
		tone := synth.Tone{LUFS: -20, Seconds: 2}
		if err := synth.MP3(context.Background(), testtools.Tools(), name, tone, synth.MP3Options{NoID3: noID3}); err != nil {
			srcMu.Unlock()
			t.Fatal(err)
		}
	}
	srcMu.Unlock()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "in.mp3")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIngestLossy_Freeze(t *testing.T) {
	t.Parallel()
	e := realEnv(t)
	src := mp3Source(t, false)
	before, _ := os.ReadFile(src)
	stBefore, _ := os.Stat(src)
	req := e.request(src)
	res, err := e.p.IngestLossy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.MasterSHA256 != sha(before) || res.FramesSHA256 != res.MasterSHA256 {
		t.Fatalf("hashes: master %s frames %s source %s", res.MasterSHA256, res.FramesSHA256, sha(before))
	}
	if want := "audio/masters/demo__0__20261012-0907.mp3"; res.MasterRelPath != want {
		t.Fatalf("path = %s, want %s", res.MasterRelPath, want)
	}
	got, err := os.ReadFile(filepath.Join(e.dir, res.MasterRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, before) || res.MasterBytes != int64(len(before)) {
		t.Fatal("claimed master is not byte-identical to the source")
	}
	st, _ := os.Stat(filepath.Join(e.dir, res.MasterRelPath))
	if st.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if want := (&LossyInfo{SampleRate: 44100, Channels: 2, TotalSamples: 88200, DurationS: 2.0}); res.Lossy == nil || *res.Lossy != *want {
		t.Fatalf("Lossy = %+v, want %+v", res.Lossy, want)
	}
	if res.AudioMD5 != strings.Repeat("0", 32) {
		t.Fatalf("AudioMD5 = %q", res.AudioMD5)
	}
	after, _ := os.ReadFile(src)
	stAfter, _ := os.Stat(src)
	if !bytes.Equal(after, before) || !stAfter.ModTime().Equal(stBefore.ModTime()) {
		t.Fatal("source file touched")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(ents) != 0 {
		t.Fatalf("work dir left: %v", ents)
	}
	// a second ingest of the same recording moment takes the next free name
	res2, err := e.p.IngestLossy(context.Background(), e.request(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := "audio/masters/demo__0__20261012-0907__2.mp3"; res2.MasterRelPath != want {
		t.Fatalf("second path = %s, want %s", res2.MasterRelPath, want)
	}
}

func TestIngestLossy_NoTagsNoRendition(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	tools.Metaflac = "/nonexistent"
	e := newEnv(t, tools, nil)
	res, err := e.p.IngestLossy(context.Background(), e.request(mp3Source(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	if res.MP3.RelPath != "" || res.MP3.Kind != "" {
		t.Fatalf("MP3 rendition present: %+v", res.MP3)
	}
	for _, f := range e.files() {
		if strings.Contains(f, "hq.mp3") {
			t.Fatalf("hq.mp3 produced: %s", f)
		}
	}
	if res.Peaks.Kind != "peaks" || res.Peaks.RelPath == "" {
		t.Fatalf("Peaks = %+v", res.Peaks)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, res.Peaks.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	var pj struct {
		Overview struct {
			SamplesPerPixel int `json:"samples_per_pixel"`
			Length          int `json:"length"`
		} `json:"overview"`
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		t.Fatal(err)
	}
	// the overview's pixel width is (total+2047)/2048 samples: 44 for 88200 frames
	if want := (88200 + 2047) / 2048; pj.Overview.SamplesPerPixel != want || pj.Overview.Length != (88200+want-1)/want {
		t.Fatalf("overview = %+v, want %d samples per pixel", pj.Overview, want)
	}
	if math.IsNaN(res.ReplayGainDB) || math.IsInf(res.ReplayGainDB, 0) {
		t.Fatalf("ReplayGainDB = %v", res.ReplayGainDB)
	}
	if st, _ := os.Stat(filepath.Join(e.dir, res.Peaks.RelPath)); st.Mode().Perm() != 0o444 {
		t.Fatalf("peaks mode = %v", st.Mode())
	}
}

func TestIngestLossy_Rejections(t *testing.T) {
	t.Parallel()
	mp3 := mp3Source(t, false)
	good, _ := os.ReadFile(mp3)
	flac := source(t, synth.StyleFFmpeg, 4, 0)
	flacBytes, _ := os.ReadFile(flac)
	aac := filepath.Join(t.TempDir(), "x.mp3")
	if _, err := mediatool.Run(context.Background(), testtools.Tools().FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error",
		"-f", "lavfi", "-i", "sine=d=1", "-c:a", "aac", "-f", "adts", "file:" + aac}, mediatool.RunOpts{Timeout: 30 * time.Second, MaxStdout: 1 << 20}); err != nil {
		t.Skipf("no AAC encoder: %v", err)
	}
	code := func(err error) string {
		var ie *Error
		if errors.As(err, &ie) {
			return string(ie.Code)
		}
		return "?" + fmt.Sprint(err)
	}
	t.Run("flac", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		p := filepath.Join(t.TempDir(), "in.mp3")
		_ = os.WriteFile(p, flacBytes, 0o644)
		_, err := e.p.IngestLossy(context.Background(), e.request(p))
		if c := code(err); c != string(CodeInvalidAudio) {
			t.Fatalf("code = %s (%v)", c, err)
		}
		e.assertEmpty()
	})
	t.Run("aac", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		_, err := e.p.IngestLossy(context.Background(), e.request(aac))
		if c := code(err); c != string(CodeInvalidAudio) {
			t.Fatalf("code = %s (%v)", c, err)
		}
		e.assertEmpty()
	})
	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		p := filepath.Join(t.TempDir(), "in.mp3")
		_ = os.WriteFile(p, good[:4000], 0o644)
		res, err := e.p.IngestLossy(context.Background(), e.request(p))
		if err != nil {
			if c := code(err); c != string(CodeInvalidAudio) {
				t.Fatalf("code = %s (%v)", c, err)
			}
			e.assertEmpty()
			return
		}
		// documented: the decode count is the truth
		if res.Lossy == nil || res.Lossy.TotalSamples >= 88200 || res.Lossy.TotalSamples == 0 {
			t.Fatalf("Lossy = %+v", res.Lossy)
		}
	})
	t.Run("changed", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		req := e.request(mp3, func(r *Request) { r.SourceSHA256 = strings.Repeat("0", 64) })
		_, err := e.p.IngestLossy(context.Background(), req)
		if c := code(err); c != string(CodeSourceChanged) {
			t.Fatalf("code = %s (%v)", c, err)
		}
		e.assertEmpty()
	})
	t.Run("artifacts", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		req := e.request(mp3, func(r *Request) { r.Artifacts = []ArtifactSrc{{Name: "score.abc", SHA256: "0"}} })
		_, err := e.p.IngestLossy(context.Background(), req)
		if c := code(err); c != string(CodeIO) {
			t.Fatalf("code = %s (%v)", c, err)
		}
		e.assertEmpty()
	})
	t.Run("programmer", func(t *testing.T) {
		t.Parallel()
		e := realEnv(t)
		for name, mut := range map[string]func(*Request){
			"slug": func(r *Request) { r.Slug = "../x" },
			"id":   func(r *Request) { r.TrackID = "a/b" },
			"nil":  func(r *Request) { r.Source = nil },
		} {
			if _, err := e.p.IngestLossy(context.Background(), e.request(mp3, mut)); code(err) != string(CodeIO) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
}

func TestIngestLossy_ToolTimeouts(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	src := mp3Source(t, false)
	fake := filepath.Join(t.TempDir(), "ffmpeg")
	realBin := testtools.Tools().FFmpeg
	// the probes New makes answer from the real binary; decode runs hang
	script := "#!/bin/sh\ncase \"$*\" in *f32le*|*ebur128*) exec sleep 30;; esac\nexec '" + realBin + "' \"$@\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := testtools.Tools()
	tools.FFmpeg = fake
	e := newEnv(t, tools, nil)
	e.p.to.decode = 300 * time.Millisecond
	start := time.Now()
	_, err := e.p.IngestLossy(context.Background(), e.request(src))
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != CodeToolFailed {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v", d)
	}
	e.assertEmpty()
}

func TestIngest_ReferenceTags(t *testing.T) {
	t.Parallel()
	e := realEnv(t)
	req, res := e.ingest(source(t, synth.StyleFFmpeg, 4, 0), func(r *Request) { r.Reference = true; r.Seed = 0 })
	info, _ := e.readInfo(res.MasterRelPath)
	var keys []string
	for _, c := range info.Comments {
		keys = append(keys, c[:strings.IndexByte(c, '=')])
	}
	want := []string{"TITLE", "MM_TAKE_ID", "MM_RECIPE_ID", "MM_REQUEST_ID", "MM_REFERENCE",
		"REPLAYGAIN_TRACK_GAIN", "REPLAYGAIN_TRACK_PEAK", "REPLAYGAIN_REFERENCE_LOUDNESS"}
	if diff := cmp.Diff(want, keys); diff != "" {
		t.Fatalf("keys differ:\n%s", diff)
	}
	if info.Comments[4] != "MM_REFERENCE=1" || info.Comments[1] != "MM_TAKE_ID="+req.TrackID {
		t.Fatalf("comments = %q", info.Comments)
	}
	if slices.ContainsFunc(info.Comments, func(c string) bool { return strings.HasPrefix(c, "MM_SEED") }) {
		t.Fatal("MM_SEED written for a reference take")
	}
}

// failing wraps an analyzer and replaces selected steps.
type failing struct {
	analysis.Analyzer
	frames   *uint64
	countErr error
	loudErr  error
	scanErr  error
}

func (f failing) CountFrames(ctx context.Context, p string, sr, ch int) (uint64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.frames != nil {
		return *f.frames, nil
	}
	return f.Analyzer.CountFrames(ctx, p, sr, ch)
}

func (f failing) Loudness(ctx context.Context, p string) (analysis.Loudness, error) {
	if f.loudErr != nil {
		return analysis.Loudness{}, f.loudErr
	}
	return f.Analyzer.Loudness(ctx, p)
}

func (f failing) Scan(ctx context.Context, p string, sr, ch int, total uint64) (analysis.Peaks, analysis.Spectrum, error) {
	if f.scanErr != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, f.scanErr
	}
	return f.Analyzer.Scan(ctx, p, sr, ch, total)
}

func TestIngestLossy_AnalysisFailures(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	src := mp3Source(t, false)
	zero := uint64(0)
	realAn, err := ffmpeg.New(context.Background(), testtools.Tools())
	if err != nil {
		t.Fatal(err)
	}
	toolErr := &mediatool.ToolError{ExitCode: 1}
	for _, c := range []struct {
		name string
		an   failing
		want string
	}{
		{"zero frames", failing{frames: &zero}, CodeInvalidAudio},
		{"decode exit", failing{countErr: toolErr}, CodeInvalidAudio},
		{"decode timeout", failing{countErr: &mediatool.ToolError{ExitCode: -1}}, CodeToolFailed},
		{"loudness", failing{loudErr: errors.New("boom")}, CodeToolFailed},
		{"scan decode", failing{scanErr: errors.New("short decode")}, CodeInvalidAudio},
		{"scan tool", failing{scanErr: toolErr}, CodeToolFailed},
		{"scan missing", failing{scanErr: analysis.ErrToolMissing}, CodeToolFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.an.Analyzer = realAn
			e := newEnv(t, testtools.Tools(), c.an)
			_, err := e.p.IngestLossy(context.Background(), e.request(src))
			var ie *Error
			if !errors.As(err, &ie) || ie.Code != c.want {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			e.assertEmpty()
		})
	}
}

func TestIngestLossy_EnvironmentErrors(t *testing.T) {
	t.Parallel()
	src := mp3Source(t, false)
	e := realEnv(t)
	e.p.dataDir = "relative"
	if _, err := e.p.IngestLossy(context.Background(), e.request(src)); err == nil {
		t.Error("relative data dir accepted")
	}
	e = realEnv(t)
	req := e.request(src)
	if err := e.root.MkdirAll("incoming/ingest-"+req.TrackID, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.IngestLossy(context.Background(), req); err == nil {
		t.Error("existing work dir accepted")
	}
	e = realEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.p.IngestLossy(ctx, e.request(src)); err == nil {
		t.Error("cancelled context accepted")
	}
	e.assertEmpty()
}

func TestIngestLossy_ConcurrentSameNameNoClobber(t *testing.T) {
	t.Parallel()
	e := realEnv(t)
	src := mp3Source(t, false)
	var wg sync.WaitGroup
	paths := make([]string, 4)
	reqs := make([]Request, len(paths))
	for i := range reqs {
		reqs[i] = e.request(src)
	}
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.p.IngestLossy(context.Background(), reqs[i])
			if err != nil {
				t.Error(err)
				return
			}
			paths[i] = res.MasterRelPath
		}()
	}
	wg.Wait()
	slices.Sort(paths)
	if len(slices.Compact(slices.Clone(paths))) != len(paths) {
		t.Fatalf("name collision: %v", paths)
	}
}
