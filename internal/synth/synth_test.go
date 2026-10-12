package synth_test

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func readInfo(t *testing.T, p string) (flacmeta.Info, int64) {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	st, _ := fh.Stat()
	info, err := flacmeta.Read(fh, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return info, st.Size()
}

func framesSHA(t *testing.T, p string) string {
	t.Helper()
	info, size := readInfo(t, p)
	fh, _ := os.Open(p)
	defer fh.Close()
	s, err := flacmeta.FramesSHA256(fh, size, info.FramesOffset)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSynthTone_LUFSWithinTolerance(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tone := range []synth.Tone{
		{LUFS: -14, Seconds: 3, BPM: 120},
		{LUFS: -10, Seconds: 3, BPM: 90, Variant: 1},
		{LUFS: -20, Seconds: 3},
		{LUFS: -14, Seconds: 3, BandLimitHz: 8000},
	} {
		p := filepath.Join(t.TempDir(), "t.flac")
		if err := synth.FLAC(context.Background(), tools, p, tone, synth.StyleFFmpeg); err != nil {
			t.Fatal(err)
		}
		l, err := f.Loudness(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(l.IntegratedLUFS-tone.LUFS) > 0.05 {
			t.Errorf("%+v: measured %.3f", tone, l.IntegratedLUFS)
		}
		if l.SamplePeakDBFS > -0.5 {
			t.Errorf("%+v: sample peak %.2f risks clipping", tone, l.SamplePeakDBFS)
		}
		info, _ := readInfo(t, p)
		si := info.StreamInfo
		if si.SampleRate != 48000 || si.Channels != 2 || si.BitsPerSample != 24 || si.TotalSamples != 3*48000 {
			t.Errorf("%+v: stream info %+v", tone, si)
		}
	}
}

func TestSynth_Styles(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	d := t.TempDir()
	a, b := filepath.Join(d, "a.flac"), filepath.Join(d, "b.flac")
	tone := synth.Tone{LUFS: -14, Seconds: 2, BPM: 100}
	if err := synth.FLAC(context.Background(), tools, a, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if err := synth.FLAC(context.Background(), tools, b, tone, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	ia, _ := readInfo(t, a)
	ib, _ := readInfo(t, b)
	if ia.Count(flacmeta.BlockType(1)) == 0 || len(ia.Comments) == 0 {
		t.Errorf("ffmpeg style should carry padding and an encoder tag: %+v %v", ia.Blocks, ia.Comments)
	}
	if ib.Count(1) != 0 || ib.Count(3) != 0 || len(ib.Comments) != 0 {
		t.Errorf("yue2 style must have no padding, seektable or tags: %+v %v", ib.Blocks, ib.Comments)
	}
	if ib.StreamInfo.TotalSamples != 2*48000 || ib.StreamInfo.BitsPerSample != 24 {
		t.Errorf("%+v", ib.StreamInfo)
	}
	// variant changes the frames; same tone twice is reproducible
	c := filepath.Join(d, "c.flac")
	if err := synth.FLAC(context.Background(), tools, c, synth.Tone{LUFS: -14, Seconds: 2, BPM: 100, Variant: 1}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if framesSHA(t, a) == framesSHA(t, c) {
		t.Error("variant must change the audio")
	}
	a2 := filepath.Join(d, "a2.flac")
	if err := synth.FLAC(context.Background(), tools, a2, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	if framesSHA(t, a) != framesSHA(t, a2) {
		t.Error("same tone must be reproducible")
	}
	if err := synth.FLAC(context.Background(), tools, filepath.Join(d, "bad.flac"), synth.Tone{LUFS: -14, Seconds: 0}, synth.StyleFFmpeg); err == nil {
		t.Error("zero duration accepted")
	}
	if err := synth.FLAC(context.Background(), tools, "relative.flac", tone, synth.StyleFFmpeg); err == nil {
		t.Error("relative dst accepted")
	}
}

// recordingFFmpeg wraps the real ffmpeg and logs argv.
func recordingFFmpeg(t *testing.T, realBin string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log, bin = filepath.Join(dir, "argv.log"), filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + log + "'\nexec '" + realBin + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestSynth_UsesSwr(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	realBin, err := exec.LookPath(tools.FFmpeg)
	if err != nil {
		t.Fatal(err)
	}
	bin, log := recordingFFmpeg(t, realBin)
	tools.FFmpeg = bin
	p := filepath.Join(t.TempDir(), "t.flac")
	if err := synth.FLAC(context.Background(), tools, p, synth.Tone{LUFS: -14, Seconds: 2, BandLimitHz: 8000}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !bytes.Contains(b, []byte("resampler=swr")) || bytes.Contains(b, []byte("soxr")) {
		t.Fatalf("argv:\n%s", b)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "/") {
			t.Errorf("bare path in argv: %q", line)
		}
	}
}

func TestSynthRetag_SameAudioID(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	d := t.TempDir()
	src, dst := filepath.Join(d, "s.flac"), filepath.Join(d, "r.flac")
	if err := synth.FLAC(context.Background(), tools, src, synth.Tone{LUFS: -14, Seconds: 2}, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	title := "Città è <&>"
	if err := synth.Retag(context.Background(), tools, src, dst, []string{"TITLE=" + title, "MM_X=1"}); err != nil {
		t.Fatal(err)
	}
	is, _ := readInfo(t, src)
	id, _ := readInfo(t, dst)
	if flacmeta.AudioID(framesSHA(t, src), is.StreamInfo.TotalSamples) != flacmeta.AudioID(framesSHA(t, dst), id.StreamInfo.TotalSamples) {
		t.Fatal("audio id changed")
	}
	if len(id.Comments) != 2 || id.Comments[0] != "TITLE="+title {
		t.Fatalf("comments %q", id.Comments)
	}
	a, _ := os.ReadFile(src)
	b, _ := os.ReadFile(dst)
	if bytes.Equal(a, b) {
		t.Fatal("bytes must differ")
	}
	// retagging again replaces tags
	dst2 := filepath.Join(d, "r2.flac")
	if err := synth.Retag(context.Background(), tools, dst, dst2, []string{"TITLE=x"}); err != nil {
		t.Fatal(err)
	}
	i2, _ := readInfo(t, dst2)
	if len(i2.Comments) != 1 {
		t.Fatalf("comments %q", i2.Comments)
	}
	if err := synth.Retag(context.Background(), tools, src, filepath.Join(d, "bad.flac"), []string{"A=b\nC=d"}); err == nil {
		t.Error("newline in a comment accepted")
	}
	if err := synth.Retag(context.Background(), tools, src, filepath.Join(d, "bad2.flac"), []string{"noequals"}); err == nil {
		t.Error("comment without = accepted")
	}
	if err := synth.Retag(context.Background(), tools, filepath.Join(d, "nope.flac"), filepath.Join(d, "bad3.flac"), nil); err == nil {
		t.Error("missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(d, "bad3.flac")); err == nil {
		t.Error("failed retag left a file")
	}
}

func TestProbeFLAC(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	p := filepath.Join(t.TempDir(), "probe.flac")
	if err := synth.ProbeFLAC(context.Background(), tools, p); err != nil {
		t.Fatal(err)
	}
	info, _ := readInfo(t, p)
	si := info.StreamInfo
	if si.BitsPerSample != 24 || si.SampleRate != 48000 || si.Channels != 2 || si.TotalSamples != 3*48000 || info.Duration() != 3.0 {
		t.Fatalf("%+v", si)
	}
}

func TestEBU3341_BadCase(t *testing.T) {
	var tools mediatool.Tools
	if _, err := synth.EBU3341(context.Background(), tools, filepath.Join(t.TempDir(), "x.flac"), 0); err == nil {
		t.Fatal("case 0 accepted")
	}
}
