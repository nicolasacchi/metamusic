package morning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestBuildSide_Argv(t *testing.T) {
	t.Parallel()
	got := decodeArgs("/d/in.flac", "/d/out.wav", Source{SampleRate: 48000, Channels: 2}, 0, 960000)
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-y", "-protocol_whitelist", "file,pipe",
		"-i", "file:/d/in.flac", "-map", "0:a:0", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
		"-af", "afade=t=in:st=0:d=0.030,afade=t=out:st=19.970:d=0.030",
		"-c:a", "pcm_s24le", "-ar", "48000", "-ac", "2", "-f", "wav", "file:/d/out.wav"}
	if !slices.Equal(got, want) {
		t.Errorf("stereo argv = %q\nwant %q", got, want)
	}
	mono := decodeArgs("/d/in.flac", "/d/out.wav", Source{SampleRate: 44100, Channels: 1}, -480, 960000)
	i := slices.Index(mono, "-af")
	wantAF := "pan=stereo|c0=0.7071067811865476*c0|c1=0.7071067811865476*c0,aresample=48000:resampler=swr,volume=-4.80dB:precision=double,afade=t=in:st=0:d=0.030,afade=t=out:st=19.970:d=0.030"
	if i < 0 || mono[i+1] != wantAF {
		t.Errorf("mono filter = %q, want %q", mono[i+1], wantAF)
	}

	side := Side{Chapters: []Chapter{{N: 7, Start: 0}, {N: 2, Start: 4_320_000 + 72_000*3}}}
	fa := flacArgs("/d/side.wav", "/d/side.flac", side)
	wantF := []string{"--best", "--no-padding", "--silent", "--force", "-S", "2s", "-S", "0", "-S", "4536000",
		"-T", "CHAPTER001=00:00:00.000", "-T", "CHAPTER001NAME=Take 7",
		"-T", "CHAPTER002=00:01:34.500", "-T", "CHAPTER002NAME=Take 2", "-o", "/d/side.flac", "/d/side.wav"}
	if !slices.Equal(fa, wantF) {
		t.Errorf("flac argv = %q\nwant %q", fa, wantF)
	}
	if got := chapterTime(3600*SampleRate + 61_500*SampleRate/1000); got != "01:01:01.500" {
		t.Errorf("chapterTime = %q", got)
	}
}

// fakeTool writes an executable shell script and returns its path.
func fakeTool(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { // #nosec G306 -- test script
		t.Fatal(err)
	}
	for range 50 { // a parallel fork may hold the write descriptor: wait until exec works
		if _, err := mediatool.Run(context.Background(), p, nil, mediatool.RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10}); !strings.Contains(fmt.Sprint(err), "cannot run") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return p
}

type fixedAnalyzer struct{ m clip.Measure }

func (fixedAnalyzer) ToolVersion() string { return "fake" }
func (f fixedAnalyzer) WindowLoudness(context.Context, string, int64, int64) (clip.Measure, error) {
	return f.m, nil
}

func newRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func oneChapterInput(master string, frames int64) SideInput {
	c := Chapter{N: 1, TrackID: "t1", Start: 0, AudioStart: SlateSamples, End: SlateSamples + frames}
	return SideInput{Side: Side{Chapters: []Chapter{c}, Samples: c.End},
		Sources: map[string]Source{"t1": {TrackID: "t1", MasterPath: master, SampleRate: 48000, Channels: 2}}}
}

func TestBuildSide_RefusesBadInput(t *testing.T) {
	t.Parallel()
	root, dir := newRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "m.flac"), []byte("x"), 0o444); err != nil { // #nosec G306 -- test file
		t.Fatal(err)
	}
	b := NewBuilder(root, dir, mediatool.Tools{FFmpeg: "/nonexistent/ffmpeg", FLAC: "/nonexistent/flac"}, fixedAnalyzer{})
	in := oneChapterInput("m.flac", 48000)
	cases := map[string]func() SideInput{
		"empty side": func() SideInput { return SideInput{} },
		"no source":  func() SideInput { i := in; i.Sources = nil; return i },
		"channels": func() SideInput {
			i := oneChapterInput("m.flac", 1)
			i.Sources["t1"] = Source{MasterPath: "m.flac", SampleRate: 48000, Channels: 6}
			return i
		},
		"no master":    func() SideInput { return oneChapterInput("", 48000) },
		"missing file": func() SideInput { return oneChapterInput("gone.flac", 48000) },
		"tool absent":  func() SideInput { return in },
	}
	for name, mk := range cases {
		_, err := b.BuildSide(context.Background(), mk())
		var be *BuildError
		if !errors.As(err, &be) || be.Code != CodeToolFailed {
			t.Errorf("%s: err = %v, want TOOL_FAILED", name, err)
		}
	}
	if _, err := NewBuilder(root, dir, mediatool.Tools{}, nil).BuildSide(context.Background(), in); err == nil {
		t.Error("a builder without an analyzer must refuse")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "incoming")); len(ents) != 0 {
		t.Errorf("work dirs left behind: %v", ents)
	}
	if err := (&BuildError{Code: "X", Chapter: 2, Msg: "m", Err: os.ErrClosed}).Error(); !strings.Contains(err, "X: m (chapter 2): ") {
		t.Errorf("BuildError text = %q", err)
	}
}

func TestBuildSide_ToolFailureAndCancel(t *testing.T) {
	t.Parallel()
	root, dir := newRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "m.flac"), []byte("x"), 0o444); err != nil { // #nosec G306 -- test file
		t.Fatal(err)
	}
	in := oneChapterInput("m.flac", 48000)
	failing := fakeTool(t, "echo boom >&2; exit 3")
	b := NewBuilder(root, dir, mediatool.Tools{FFmpeg: failing, FLAC: failing}, fixedAnalyzer{})
	_, err := b.BuildSide(context.Background(), in)
	var be *BuildError
	var te *mediatool.ToolError
	if !errors.As(err, &be) || be.Code != CodeToolFailed || !errors.As(err, &te) || te.ExitCode != 3 {
		t.Fatalf("err = %v, want TOOL_FAILED wrapping the exit 3", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.BuildSide(ctx, in); !errors.As(err, &be) || be.Code != CodeInterrupted || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled build err = %v, want INTERRUPTED", err)
	}
}

// ---- media tier: real ffmpeg and flac on synthetic audio in t.TempDir() ----

// genMaster writes a synthetic FLAC master (a sine, volDB below full scale) into <dir>/library and returns its relative
// path. The file is read-only like a real master.
func genMaster(t *testing.T, tools mediatool.Tools, dir, name string, rate, channels int, seconds float64, volDB float64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "library"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel := "library/" + name + ".flac"
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=330:sample_rate=%d:duration=%g", rate, seconds),
		"-af", fmt.Sprintf("volume=%gdB", volDB), "-ac", strconv.Itoa(channels),
		"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", filepath.Join(dir, rel)}
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, rel), 0o444); err != nil {
		t.Fatal(err)
	}
	return rel
}

type masterState struct {
	sha  [32]byte
	md5  [16]byte
	mod  time.Time
	size int64
}

func stateOf(t *testing.T, p string) masterState {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return masterState{sha256.Sum256(b), info.StreamInfo.MD5, st.ModTime(), st.Size()}
}

func mediaRig(t *testing.T) (tools mediatool.Tools, an *ffmpeg.FFmpeg) {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac")
	tools = testtools.Tools()
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return tools, an
}

// candidate measures a master and returns it as a plan candidate.
func candidate(t *testing.T, an *ffmpeg.FFmpeg, dir, id, rel string, rate, ch int, secs float64) Candidate {
	t.Helper()
	m, err := an.WindowLoudness(context.Background(), filepath.Join(dir, rel), 0, int64(secs*float64(rate)))
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{TrackID: id, RootID: "root-" + id, MasterPath: rel, TotalSamples: int64(secs * float64(rate)), SampleRate: rate, Channels: ch,
		Source: SourceJob, CreatedAt: time.Unix(1_700_000_000, 0), LUFS: m.IntegratedLUFS, TruePeak: m.TruePeakDBTP, DurationS: secs}
}

func sourcesOf(cs []Candidate) map[string]Source {
	m := map[string]Source{}
	for _, c := range cs {
		m[c.TrackID] = Source{TrackID: c.TrackID, MasterPath: c.MasterPath, SampleRate: c.SampleRate, Channels: c.Channels}
	}
	return m
}

// decodeWindow returns the raw s24le stereo PCM of [from, to) of a FLAC.
func decodeWindow(t *testing.T, ffmpegBin, flacPath string, from, to int64) []byte {
	t.Helper()
	out, err := mediatool.Run(context.Background(), ffmpegBin, []string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + flacPath,
		"-af", fmt.Sprintf("atrim=start_sample=%d:end_sample=%d", from, to), "-f", "s24le", "-c:a", "pcm_s24le", "-"},
		mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBuildSide_SyntheticCrate builds a real side from synthetic masters at three loudnesses (one mono at 44.1 kHz) and checks
// the file bytes: sample-exact slates, chapter tags, metadata layout, loudness, mode, and that no master changed.
func TestBuildSide_SyntheticCrate(t *testing.T) {
	tools, an := mediaRig(t)
	root, dir := newRoot(t)
	specs := []struct {
		id        string
		rate, ch  int
		secs, vol float64
	}{{"a", 48000, 2, 12, -8}, {"b", 48000, 2, 10, -17}, {"c", 44100, 1, 9, -11}, {"d", 48000, 2, 8, -14}}
	var cands []Candidate
	for _, s := range specs {
		rel := genMaster(t, tools, dir, s.id, s.rate, s.ch, s.secs, s.vol)
		cands = append(cands, candidate(t, an, dir, s.id, rel, s.rate, s.ch, s.secs))
	}
	plan := Select(Input{Candidates: cands}, DefaultOptions(), 42)
	if len(plan.Sides) != 1 || len(plan.Sides[0].Chapters) != len(specs) {
		t.Fatalf("plan = %+v", plan)
	}
	var before []masterState
	for _, c := range cands {
		before = append(before, stateOf(t, filepath.Join(dir, c.MasterPath)))
	}
	b := NewBuilder(root, dir, tools, an)
	side := plan.Sides[0]
	res, err := b.BuildSide(context.Background(), SideInput{Side: side, Sources: sourcesOf(cands)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("side: %d bytes, ratio %.3f, wall %v", res.Bytes, res.ByteRatio, res.Wall)

	// masters are untouched
	for i, c := range cands {
		if after := stateOf(t, filepath.Join(dir, c.MasterPath)); after != before[i] {
			t.Errorf("master %s changed: %+v -> %+v", c.TrackID, before[i], after)
		}
	}
	// claimed file: path, mode, hash, work dir gone
	abs := filepath.Join(dir, res.RelPath)
	st, err := os.Stat(abs)
	if err != nil || st.Mode().Perm() != 0o444 || st.Size() != res.Bytes {
		t.Fatalf("claimed file: %v %v", st, err)
	}
	if want := fmt.Sprintf("derived/morning/%s/%s.flac", res.SHA256[:2], res.SHA256); res.RelPath != want {
		t.Errorf("RelPath = %q, want %q", res.RelPath, want)
	}
	if got := stateOf(t, abs).sha; fmt.Sprintf("%x", got) != res.SHA256 {
		t.Error("SHA256 does not match the file")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "incoming")); len(ents) != 0 {
		t.Errorf("work dir not removed: %v", ents)
	}
	// metadata
	f, _ := os.Open(abs)
	defer func() { _ = f.Close() }()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	if info.StreamInfo.TotalSamples != uint64(side.Samples) || info.Count(flacmeta.BlockVorbisComment) != 1 || info.Count(flacmeta.BlockCueSheet) != 0 {
		t.Errorf("layout: %d samples (want %d), %d vorbis, %d cue", info.StreamInfo.TotalSamples, side.Samples,
			info.Count(flacmeta.BlockVorbisComment), info.Count(flacmeta.BlockCueSheet))
	}
	if len(info.SeekPoints) < len(side.Chapters) {
		t.Errorf("seek points = %d", len(info.SeekPoints))
	}
	for i, c := range side.Chapters {
		tag := fmt.Sprintf("CHAPTER%03d", i+1)
		for _, w := range []string{tag + "=" + chapterTime(c.Start), fmt.Sprintf("%sNAME=Take %d", tag, c.N)} {
			if !slices.Contains(info.Comments, w) {
				t.Errorf("tag %q missing from %q", w, info.Comments)
			}
		}
	}
	for _, c := range info.Comments { // blind: nothing but chapter tags
		if !strings.HasPrefix(c, "CHAPTER") {
			t.Errorf("unexpected tag %q", c)
		}
	}
	// sample-exact slates and loudness
	for _, c := range side.Chapters {
		got := decodeWindow(t, tools.FFmpeg, abs, c.Start, c.AudioStart)
		if !bytes.Equal(got, SlatePCM(c.N)) {
			t.Errorf("chapter %d: slate frames are not sample exact (%d bytes)", c.N, len(got))
		}
	}
	if len(res.Chapters) != len(side.Chapters) {
		t.Fatalf("chapters measured = %d", len(res.Chapters))
	}
	for _, c := range res.Chapters {
		if math.Abs(c.LUFS-TargetLUFS) > LoudnessTolerance {
			t.Errorf("chapter %d measured %.2f LUFS", c.N, c.LUFS)
		}
	}
	if res.ByteRatio <= 0 || res.ByteRatio >= 1 || res.Wall <= 0 {
		t.Errorf("ratio %v wall %v", res.ByteRatio, res.Wall)
	}
	// building again yields the same claimed file (deterministic encode)
	again, err := b.BuildSide(context.Background(), SideInput{Side: side, Sources: sourcesOf(cands)})
	if err != nil || again.SHA256 != res.SHA256 {
		t.Errorf("rebuild: %v, sha %s vs %s", err, again.SHA256, res.SHA256)
	}
}

func TestBuildSide_NeverWritesMasters(t *testing.T) {
	tools, an := mediaRig(t)
	root, dir := newRoot(t)
	rel := genMaster(t, tools, dir, "m", 48000, 2, 5, -12)
	c := candidate(t, an, dir, "t1", rel, 48000, 2, 5)
	before := stateOf(t, filepath.Join(dir, rel))
	plan := Select(Input{Candidates: []Candidate{c}}, DefaultOptions(), 1)
	for _, wa := range []clip.WindowAnalyzer{an, fixedAnalyzer{clip.Measure{IntegratedLUFS: -10, TruePeakDBTP: -1}}} { // success and a failed check
		_, _ = NewBuilder(root, dir, tools, wa).BuildSide(context.Background(), SideInput{Side: plan.Sides[0], Sources: sourcesOf([]Candidate{c})})
		if got := stateOf(t, filepath.Join(dir, rel)); got != before {
			t.Fatalf("master changed: %+v -> %+v", before, got)
		}
	}
	st, _ := os.Stat(filepath.Join(dir, rel))
	if st.Mode().Perm() != 0o444 {
		t.Errorf("master mode = %v", st.Mode())
	}
}

func TestBuildSide_SampleMismatch(t *testing.T) {
	tools, an := mediaRig(t)
	root, dir := newRoot(t)
	rel := genMaster(t, tools, dir, "m", 48000, 2, 3, -12)
	// the plan claims 4 s of a 3 s master
	in := oneChapterInput(rel, 4*SampleRate)
	_, err := NewBuilder(root, dir, tools, an).BuildSide(context.Background(), in)
	var be *BuildError
	if !errors.As(err, &be) || be.Code != CodeSamples || be.Chapter != 1 {
		t.Fatalf("err = %v, want MORNING_CRATE_SAMPLES for chapter 1", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "incoming")); len(ents) != 0 {
		t.Errorf("work dir left behind: %v", ents)
	}
	if _, err := os.Stat(filepath.Join(dir, "derived")); err == nil {
		t.Error("a failed build must not claim anything")
	}
}

func TestBuildSide_LoudnessCheck(t *testing.T) {
	tools, _ := mediaRig(t)
	root, dir := newRoot(t)
	rel := genMaster(t, tools, dir, "m", 48000, 2, 3, -12)
	for _, tc := range []struct {
		name    string
		m       clip.Measure
		limited bool
		code    string
	}{
		{"on target", clip.Measure{IntegratedLUFS: -16.4, TruePeakDBTP: -3}, false, ""},
		{"too loud", clip.Measure{IntegratedLUFS: -15.4, TruePeakDBTP: -3}, false, CodeLoudness},
		{"too quiet", clip.Measure{IntegratedLUFS: -16.6, TruePeakDBTP: -3}, false, CodeLoudness},
		{"NaN", clip.Measure{IntegratedLUFS: math.NaN(), TruePeakDBTP: -3}, false, CodeLoudness},
		{"limited and quieter is fine", clip.Measure{IntegratedLUFS: -19, TruePeakDBTP: -1}, true, ""},
		{"limited but louder than target", clip.Measure{IntegratedLUFS: -15, TruePeakDBTP: -1}, true, CodeLoudness},
		{"limited but over the ceiling", clip.Measure{IntegratedLUFS: -19, TruePeakDBTP: 0.2}, true, CodeLoudness},
	} {
		in := oneChapterInput(rel, 3*SampleRate)
		in.Side.Chapters[0].Limited = tc.limited
		_, err := NewBuilder(root, dir, tools, fixedAnalyzer{tc.m}).BuildSide(context.Background(), in)
		var be *BuildError
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.code != "" && (!errors.As(err, &be) || be.Code != tc.code):
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
}
