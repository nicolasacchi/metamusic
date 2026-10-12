package ffmpeg_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// fakeFFmpeg writes a shell script standing in for ffmpeg. version/filters/
// encoders answer the capability probes; every other invocation appends its
// argv (one arg per line, "--" between runs) to log and runs body.
func fakeFFmpeg(t *testing.T, version, filters, encoders, body string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -version) printf '%s\n' '` + version + `'; exit 0;;
    -filters) printf '%s\n' '` + filters + `'; exit 0;;
    -encoders) printf '%s\n' '` + encoders + `'; exit 0;;
  esac
done
{ printf '%s\n' "$@"; echo '--'; } >> '` + log + `'
` + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

const okFilters = " ... ebur128 A->N EBU R128 scanner."
const okEncoders = " A....D libmp3lame MP3"

func TestFFmpeg_VersionTokenVerbatim(t *testing.T) {
	for _, tok := range []string{"n9.0.1", "7.1.2-0ubuntu1", "N-118123-gabcdef"} {
		bin, _ := fakeFFmpeg(t, "ffmpeg version "+tok+" Copyright (c) the FFmpeg developers", okFilters, okEncoders, "exit 0")
		f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.ToolVersion(); got != "ffmpeg "+tok {
			t.Errorf("ToolVersion = %q", got)
		}
	}
}

func TestFFmpeg_NewChecksCapabilities(t *testing.T) {
	ver := "ffmpeg version n9.0.1 Copyright"
	for _, c := range []struct{ filters, encoders, names string }{
		{" ... aecho A->A", okEncoders, "ebur128"},
		{okFilters, " V....D libx264", "libmp3lame"},
	} {
		bin, _ := fakeFFmpeg(t, ver, c.filters, c.encoders, "exit 0")
		_, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
		if !errors.Is(err, analysis.ErrToolMissing) || !strings.Contains(err.Error(), c.names) {
			t.Errorf("err = %v, want ErrToolMissing naming %s", err, c.names)
		}
	}
	// "ebur128" must be a whole word in the listing: a longer name does not count
	bin, _ := fakeFFmpeg(t, ver, " ... xebur128y A->A", okEncoders, "exit 0")
	if _, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin}); !errors.Is(err, analysis.ErrToolMissing) {
		t.Errorf("substring match accepted: %v", err)
	}
	bin, _ = fakeFFmpeg(t, "garbage", okFilters, okEncoders, "exit 0")
	if _, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin}); err == nil {
		t.Error("garbage version accepted")
	}
	if _, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("missing binary accepted")
	}
}

func TestArgv_OnlyWorkPaths(t *testing.T) {
	body := `case "$*" in *ebur128*) printf 'frame:0    pts:0       pts_time:0\nlavfi.r128.S=-30.0\nlavfi.r128.I=-30.0\nlavfi.r128.LRA=0.0\nlavfi.r128.LRA.low=-30.0\nlavfi.r128.LRA.high=-30.0\nlavfi.r128.sample_peak=0.1\nlavfi.r128.true_peak=0.1\n';; esac`
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, body)
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	in := filepath.Join(work, "master.flac")
	if _, err := f.Loudness(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Scan(context.Background(), in, 48000, 2, 0); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	runs := strings.Split(strings.TrimSpace(string(b)), "\n--\n")
	if len(runs) != 2 {
		t.Fatalf("runs = %d", len(runs))
	}
	for _, r := range runs {
		args := strings.Split(strings.TrimSpace(strings.TrimSuffix(r, "\n--")), "\n")
		pw, ii := -1, -1
		for i, a := range args {
			switch a {
			case "-protocol_whitelist":
				pw = i
				if args[i+1] != "file,pipe" {
					t.Errorf("whitelist %q", args[i+1])
				}
			case "-i":
				ii = i
				if args[i+1] != "file:"+in {
					t.Errorf("input %q", args[i+1])
				}
			}
		}
		if pw < 0 || ii < 0 || pw > ii {
			t.Errorf("-protocol_whitelist must precede -i: %v", args)
		}
		if args[len(args)-1] != "-" {
			t.Errorf("output must be the pipe: %v", args)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "/") {
				t.Errorf("bare absolute path in argv: %q", a)
			}
		}
	}
	if _, err := f.Loudness(context.Background(), "relative.flac"); err == nil {
		t.Error("relative path must be rejected")
	}
	if _, _, err := f.Scan(context.Background(), "relative.flac", 48000, 2, 0); err == nil {
		t.Error("relative path must be rejected")
	}
}

func TestScan_ExcessFramesKilled(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "exec cat /dev/zero")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err = f.Scan(context.Background(), filepath.Join(t.TempDir(), "m.flac"), 48000, 2, 48000)
	if err == nil || !strings.Contains(err.Error(), "more frames") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestScan_ShortDecodeRejected(t *testing.T) {
	bin, _ := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "exit 0")
	f, _ := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	_, _, err := f.Scan(context.Background(), filepath.Join(t.TempDir(), "m.flac"), 48000, 2, 48000)
	if err == nil || !strings.Contains(err.Error(), "frame count") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := f.Scan(context.Background(), filepath.Join(t.TempDir(), "m.flac"), 0, 2, 1); err == nil {
		t.Fatal("bad sample rate")
	}
}

func newReal(t *testing.T) (*ffmpeg.FFmpeg, mediatool.Tools) {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	return f, tools
}

func TestFFmpeg_ErrorCarriesStderrTail(t *testing.T) {
	f, _ := newReal(t)
	_, err := f.Loudness(context.Background(), filepath.Join(t.TempDir(), "missing.flac"))
	var te *mediatool.ToolError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v", err)
	}
	if te.StderrTail == "" || len(te.StderrTail) > 2048 {
		t.Fatalf("stderr tail len %d", len(te.StderrTail))
	}
	if strings.Contains(te.Error(), "-protocol_whitelist") {
		t.Fatal("argv leaked")
	}
}

func TestLoudness_EBU3341_Cases(t *testing.T) {
	f, tools := newReal(t)
	for n := 1; n <= 4; n++ {
		p := filepath.Join(t.TempDir(), "ebu.flac")
		want, err := synth.EBU3341(context.Background(), tools, p, n)
		if err != nil {
			t.Fatal(err)
		}
		l, err := f.Loudness(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(l.IntegratedLUFS-want) > 0.1 {
			t.Errorf("case %d: I = %.3f, want %.1f", n, l.IntegratedLUFS, want)
		}
		if len(l.ShortTerm1Hz) < 20 {
			t.Errorf("case %d: curve length %d", n, len(l.ShortTerm1Hz))
		}
	}
	if _, err := synth.EBU3341(context.Background(), tools, filepath.Join(t.TempDir(), "x.flac"), 9); err == nil {
		t.Error("unknown case accepted")
	}
}

func TestLoudness_TruePeakLoudSine(t *testing.T) {
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "loud.flac")
	if err := loudSine(tools, p); err != nil {
		t.Fatal(err)
	}
	l, err := f.Loudness(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(l.TruePeakDBTP-(-3)) > 0.2 {
		t.Errorf("TP = %.3f", l.TruePeakDBTP)
	}
}

func loudSine(tools mediatool.Tools, dst string) error {
	_, err := mediatool.Run(context.Background(), tools.FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error",
		"-f", "lavfi", "-i", "sine=f=1000:d=3:sample_rate=48000", "-af", "volume=15dB,pan=stereo|c0=c0|c1=c0",
		"-c:a", "flac", "-sample_fmt", "s32", "-y", "file:" + dst}, mediatool.RunOpts{Timeout: 30 * time.Second, MaxStdout: 1 << 20})
	return err
}

func bandwidthOf(t *testing.T, tone synth.Tone) *int {
	t.Helper()
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "bw.flac")
	if err := synth.FLAC(context.Background(), tools, p, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	info := readInfo(t, p)
	_, sp, err := f.Scan(context.Background(), p, int(info.StreamInfo.SampleRate), int(info.StreamInfo.Channels), info.StreamInfo.TotalSamples)
	if err != nil {
		t.Fatal(err)
	}
	return sp.BandwidthHz
}

func readInfo(t *testing.T, p string) flacmeta.Info {
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
	return info
}

func TestBandwidth_Upsampled16k(t *testing.T) {
	bw := bandwidthOf(t, synth.Tone{LUFS: -14, Seconds: 4, BandLimitHz: 8000})
	if bw == nil || math.Abs(float64(*bw)-8000) > 200 {
		t.Fatalf("bandwidth %v", bw2s(bw))
	}
}

func TestBandwidth_Upsampled32k(t *testing.T) {
	bw := bandwidthOf(t, synth.Tone{LUFS: -14, Seconds: 4, BandLimitHz: 16000})
	if bw == nil || math.Abs(float64(*bw)-16000) > 300 {
		t.Fatalf("bandwidth %v", bw2s(bw))
	}
}

func TestBandwidth_FullBandNoise(t *testing.T) {
	bw := bandwidthOf(t, synth.Tone{LUFS: -14, Seconds: 4, BandLimitHz: 24000})
	if bw == nil || *bw < 23000 {
		t.Fatalf("bandwidth %v", bw2s(bw))
	}
}

func TestBandwidth_Silence(t *testing.T) {
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "silence.flac")
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, []string{"-nostdin", "-hide_banner", "-v", "error",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "3", "-c:a", "flac", "-y", "file:" + p},
		mediatool.RunOpts{Timeout: 30 * time.Second, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	info := readInfo(t, p)
	_, sp, err := f.Scan(context.Background(), p, 48000, 2, info.StreamInfo.TotalSamples)
	if err != nil {
		t.Fatal(err)
	}
	if sp.BandwidthHz != nil {
		t.Fatalf("bandwidth %d", *sp.BandwidthHz)
	}
}

func TestScan_FrameCountMatchesStreamInfo(t *testing.T) {
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "m.flac")
	if err := synth.FLAC(context.Background(), tools, p, synth.Tone{LUFS: -14, Seconds: 3, BPM: 120}, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	info := readInfo(t, p)
	total := info.StreamInfo.TotalSamples
	pk, sp, err := f.Scan(context.Background(), p, 48000, 2, total)
	if err != nil {
		t.Fatal(err)
	}
	if pk.Detail.Length != int((total+2399)/2400) || sp.Frames == 0 || pk.DurationS != 3 {
		t.Fatalf("peaks %+v spectrum %+v", pk.Detail.Length, sp)
	}
	// a STREAMINFO that disagrees by more than a block in either direction is an error
	if _, _, err := f.Scan(context.Background(), p, 48000, 2, total+20000); err == nil {
		t.Error("too few decoded frames accepted")
	}
	if _, _, err := f.Scan(context.Background(), p, 48000, 2, total-20000); err == nil {
		t.Error("too many decoded frames accepted")
	}
	// Loudness on a real file also yields a curve of whole seconds
	l, err := f.Loudness(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.ShortTerm1Hz) != 3 {
		t.Errorf("curve %v", l.ShortTerm1Hz)
	}
}

func bw2s(bw *int) any {
	if bw == nil {
		return "nil"
	}
	return *bw
}
