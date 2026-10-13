package spectro

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Budget is the hard JPEG size limit (D-M2b-9).
const Budget = 100_000

// Qualities and Sizes are the C50.3 ladder, tried size-major.
var (
	Qualities = []int{85, 75, 65, 55, 45}
	Sizes     = [][2]int{{1024, 384}, {768, 288}, {512, 192}}
)

// EncodeBudget returns the first JPEG of img, trying qualities in order,
// that is at most budget bytes; none fits => ErrTooLarge.
func EncodeBudget(img image.Image, budget int, qualities []int) ([]byte, int, error) {
	for _, q := range qualities {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, 0, err
		}
		if b.Len() <= budget {
			return b.Bytes(), q, nil
		}
	}
	return nil, 0, ErrTooLarge
}

// X0 is the plot origin x of ffmpeg's legend layout for a sample rate.
func X0(sampleRate int) int { return int((math.Log10(float64(sampleRate)) + 1) * 25) }

// Picturer renders the raw ffmpeg spectrogram PNG (*ffmpeg.FFmpeg).
type Picturer interface {
	SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error
}

// Input describes one render.
type Input struct {
	MasterPath string // absolute work-dir master
	WorkDir    string // absolute dir for the intermediate PNGs
	SampleRate int
	DurationS  float64
	Overlay    Overlay
}

// Result is the encoded picture and what the rendition params record.
type Result struct {
	JPEG          []byte
	Quality       int
	PlotW, PlotH  int
	Width, Height int
	Geometry      Geometry // of the final image (Y0 includes the band)
	OverlaySHA    string
}

// Render runs the size/quality ladder: the first plot size whose composed
// picture fits Budget at some quality wins; none => ErrTooLarge.
func Render(ctx context.Context, p Picturer, in Input) (Result, error) {
	x0 := X0(in.SampleRate)
	for _, sz := range Sizes {
		w, h := sz[0], sz[1]
		out := filepath.Join(in.WorkDir, fmt.Sprintf("spectro-%dx%d.png", w, h))
		if err := p.SpectrumPicture(ctx, in.MasterPath, out, w, h); err != nil {
			return Result{}, err
		}
		plot, err := readPNG(out)
		if err != nil {
			return Result{}, err
		}
		img, g, err := Compose(plot, Geometry{X0: x0, Y0: 64, W: w, H: h, DurationS: in.DurationS}, in.Overlay)
		if err != nil {
			return Result{}, err
		}
		jpg, q, err := EncodeBudget(img, Budget, Qualities)
		if errors.Is(err, ErrTooLarge) {
			continue
		}
		if err != nil {
			return Result{}, err
		}
		return Result{JPEG: jpg, Quality: q, PlotW: w, PlotH: h, Width: img.Bounds().Dx(), Height: img.Bounds().Dy(),
			Geometry: g, OverlaySHA: OverlaySHA(in.Overlay)}, nil
	}
	return Result{}, ErrTooLarge
}

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path) //nolint:gosec // path is built from the caller's work dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGeometry, err)
	}
	return img, nil
}
