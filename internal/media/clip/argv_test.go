package clip_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// TestArgv_OnlyWorkPaths_Clip runs the default render against a fake ffmpeg that
// logs its argv, for the four filter-chain variants, and checks the exact
// invocation and that every path argument is a file: URL under the work dir.
func TestArgv_OnlyWorkPaths_Clip(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		rate     int
		gain     int
		wantMid  string // between asetpts and the fades
		wantFade string
	}{
		{"plain", 48000, 0, "", "afade=t=in:st=0:d=0.010,afade=t=out:st=29.990:d=0.010"},
		{"resample", 44100, 0, ",aresample=48000:resampler=swr", "afade=t=in:st=0:d=0.010,afade=t=out:st=29.990:d=0.010"},
		{"gain", 48000, -350, ",volume=-3.50dB:precision=double", "afade=t=in:st=0:d=0.010,afade=t=out:st=29.990:d=0.010"},
		{"both", 44100, -1, ",aresample=48000:resampler=swr,volume=-0.01dB:precision=double", "afade=t=in:st=0:d=0.010,afade=t=out:st=29.990:d=0.010"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			logf := filepath.Join(t.TempDir(), "argv.log")
			bin := filepath.Join(t.TempDir(), "ffmpeg")
			script := "#!/bin/sh\n{ printf '%s\\n' \"$@\"; } > '" + logf + "'\nexit 1\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			sp := e.fakeMaster(t, "argv")
			sp.SampleRate, sp.GainCDB = c.rate, c.gain
			s := clip.New(e.root, e.dir, mediatool.Tools{FFmpeg: bin}, cliptest.NewAnalyzer(), nil, nil, clip.Options{})
			if _, err := s.Ensure(context.Background(), sp); err == nil {
				t.Fatal("the fake ffmpeg fails; Ensure must too")
			}
			b, err := os.ReadFile(logf)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			st, en := sp.Samples()
			var in, out string
			for i, a := range args {
				if a == "-i" {
					in = args[i+1]
				}
			}
			out = args[len(args)-1]
			wd := "file:" + e.dir + "/incoming/clip-"
			if !strings.HasPrefix(in, wd) || !strings.HasSuffix(in, "/master.flac") || !strings.HasPrefix(out, wd) || !strings.HasSuffix(out, "/clip.flac") {
				t.Fatalf("paths: in %q out %q", in, out)
			}
			af := "atrim=start_sample=" + itoa(int(st)) + ":end_sample=" + itoa(int(en)) + ",asetpts=PTS-STARTPTS" + c.wantMid + "," + c.wantFade
			want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", in, "-map", "0:a:0",
				"-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", "-af", af,
				"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-compression_level", "5", "-f", "flac", out}
			if !slices.Equal(args, want) {
				t.Errorf("argv =\n%q\nwant\n%q", args, want)
			}
			pw, ii := slices.Index(args, "-protocol_whitelist"), slices.Index(args, "-i")
			if pw < 0 || pw > ii {
				t.Error("-protocol_whitelist must precede -i")
			}
			for _, a := range args {
				if strings.HasPrefix(a, "/") {
					t.Errorf("bare absolute path in argv: %q", a)
				}
			}
			if len(e.workDirs(t)) != 0 {
				t.Error("work dir left behind")
			}
		})
	}
}
