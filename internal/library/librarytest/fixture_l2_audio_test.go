package librarytest

import (
	"bytes"
	"context"
	"math"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestFixtureL2Audio_Shape(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	tools := testtools.Tools()
	a := FixtureL2Audio(t, tools)
	if len(a.Sources) != 6 {
		t.Fatalf("%d sources", len(a.Sources))
	}
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range a.Sources {
		st, err := os.Stat(a.Abs(s))
		if err != nil || st.Size() != s.Bytes || st.Mode().Perm() != 0o444 || len(s.SHA256) != 64 {
			t.Fatalf("%s: %v %+v", s.Name, err, s)
		}
		if !s.Lossy {
			b, _ := os.ReadFile(a.Abs(s))
			info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				t.Fatalf("%s: %v", s.Name, err)
			}
			si := info.StreamInfo
			if int(si.SampleRate) != s.Rate || int(si.BitsPerSample) != s.Bits || int(si.Channels) != s.Channels || s.Samples == 0 ||
				math.Abs(float64(s.Samples)/float64(s.Rate)-s.Seconds) > 0.01 {
				t.Errorf("%s: %+v vs %+v", s.Name, si, s)
			}
		}
		if s.Calibrated {
			l, err := an.Loudness(context.Background(), a.Abs(s))
			if err != nil {
				t.Fatal(err)
			}
			tol := 0.1
			if s.Lossy {
				tol = 0.7 // lossy encoding moves the level a little
			}
			if math.Abs(l.IntegratedLUFS-s.LUFS) > tol {
				t.Errorf("%s: measured %.2f LUFS, calibrated to %.1f", s.Name, l.IntegratedLUFS, s.LUFS)
			}
		}
	}
	if a.Source(t, "lossy").Lossy != true || a.Source(t, "mono").Channels != 1 {
		t.Fatal("flags")
	}
}
