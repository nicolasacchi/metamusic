package spectro_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/spectro"
)

func maxOverlay() spectro.Overlay {
	long := "a long lyric line that needs to be truncated to fit inside its section column"
	lines := []string{long, long, long, long, long, long, long}
	return spectro.Overlay{
		Header: "mm-spectro/1 | 6:00 | sections: plan (confidence 0.7); lyric lines listed per section, not timed",
		Timed: []spectro.Section{
			{Label: "intro", StartS: 0, EndS: 2, Lines: lines},
			{Label: "verse 1", StartS: 2, EndS: 5, Lines: lines},
			{Label: "chorus 1", StartS: 5, EndS: 6, Lines: lines},
		},
	}
}

func TestSpectrogram_Geometry(t *testing.T) {
	t.Parallel()
	f, tools := newFFmpeg(t)
	master := whiteNoise(t, tools, 6)
	dir := t.TempDir()

	png48 := filepath.Join(dir, "a.png")
	if err := f.SpectrumPicture(context.Background(), master, png48, 1024, 384); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(png48)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 1308 || cfg.Height != 512 {
		t.Errorf("48 kHz picture = %dx%d, want 1308x512", cfg.Width, cfg.Height)
	}
	if spectro.X0(48000) != 142 || spectro.X0(44100) != 141 {
		t.Errorf("X0 = %d, %d", spectro.X0(48000), spectro.X0(44100))
	}

	// 44.1 kHz source => x0 141, picture 1306 wide
	m44 := filepath.Join(dir, "n44.flac")
	if err := run(tools, "-i", master, "-ar", "44100", m44); err != nil {
		t.Fatal(err)
	}
	r, err := spectro.Render(context.Background(), f, spectro.Input{MasterPath: m44, WorkDir: dir, SampleRate: 44100, DurationS: 6, Overlay: spectro.Overlay{Header: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Geometry.X0 != 141 || r.Width != 1306 || r.PlotW != 1024 {
		t.Errorf("44.1 kHz result = %+v", r)
	}

	// a wrong declared sample rate fails closed
	_, err = spectro.Render(context.Background(), f, spectro.Input{MasterPath: master, WorkDir: dir, SampleRate: 44100, DurationS: 6, Overlay: spectro.Overlay{Header: "h"}})
	if !errors.Is(err, spectro.ErrGeometry) {
		t.Errorf("wrong declared rate: err = %v", err)
	}
}

func TestSpectrogram_TimeCalibration(t *testing.T) {
	t.Parallel()
	f, tools := newFFmpeg(t)
	dir := t.TempDir()
	burst := filepath.Join(dir, "burst.flac")
	if err := run(tools, "-f", "lavfi", "-i", "anoisesrc=d=0.5:c=white:r=48000:a=0.5:seed=3,adelay=2000:all=1,apad=whole_dur=10",
		"-t", "10", "-ac", "1", "-sample_fmt", "s16", burst); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "p.png")
	if err := f.SpectrumPicture(context.Background(), burst, out, 1024, 384); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	img, err := png.Decode(fh)
	if err != nil {
		t.Fatal(err)
	}
	x0 := spectro.X0(48000)
	means := make([]float64, 1024)
	for x := range means {
		var sum float64
		for y := 64; y < 64+384; y++ {
			r, g, b, _ := img.At(x0+x, y).RGBA()
			sum += float64(r+g+b) / 3
		}
		means[x] = sum / 384
	}
	sorted := slices.Clone(means)
	slices.Sort(sorted)
	med := sorted[len(sorted)/2]
	first := -1
	for x, m := range means {
		if m > med*1.3 {
			first = x
			break
		}
	}
	if first < 202 || first > 208 {
		t.Errorf("first bright column = x0+%d, want x0+205 +-3 (median %.0f)", first, med)
	}
}

func TestSpectrogram_BudgetWorstCase(t *testing.T) {
	t.Parallel()
	f, tools := newFFmpeg(t)
	for _, secs := range []int{6, 16, 60, 360} {
		t.Run(fmt.Sprintf("%ds", secs), func(t *testing.T) {
			t.Parallel()
			master := whiteNoise(t, tools, secs)
			o := maxOverlay()
			o.Timed[2].EndS = float64(secs)
			o.Timed[0].EndS, o.Timed[1].StartS, o.Timed[1].EndS, o.Timed[2].StartS =
				float64(secs)/3, float64(secs)/3, 2*float64(secs)/3, 2*float64(secs)/3
			r, err := spectro.Render(context.Background(), f, spectro.Input{MasterPath: master, WorkDir: t.TempDir(), SampleRate: 48000, DurationS: float64(secs), Overlay: o})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%d s: plot %dx%d quality %d bytes %d", secs, r.PlotW, r.PlotH, r.Quality, len(r.JPEG))
			if len(r.JPEG) > 100_000 {
				t.Errorf("%d bytes > 100000", len(r.JPEG))
			}
			if secs <= 16 && (r.PlotW != 1024 || r.Quality > 65) {
				t.Errorf("%d s landed at %dx%d q%d, want first size with quality <= 65", secs, r.PlotW, r.PlotH, r.Quality)
			}
			img, _, err := image.Decode(bytes.NewReader(r.JPEG))
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != r.Width || img.Bounds().Dy() != r.Height {
				t.Errorf("decoded %v, result %dx%d", img.Bounds(), r.Width, r.Height)
			}
			if _, err := jpeg.DecodeConfig(bytes.NewReader(r.JPEG)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSpectrogram_Deterministic(t *testing.T) {
	t.Parallel()
	f, tools := newFFmpeg(t)
	master := whiteNoise(t, tools, 6)
	var got [][]byte
	for range 2 {
		r, err := spectro.Render(context.Background(), f, spectro.Input{MasterPath: master, WorkDir: t.TempDir(), SampleRate: 48000, DurationS: 6, Overlay: maxOverlay()})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r.JPEG)
	}
	if !bytes.Equal(got[0], got[1]) {
		t.Error("same inputs produced different bytes")
	}
}
