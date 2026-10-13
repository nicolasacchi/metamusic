package spectro

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"testing"
)

const (
	tW, tH, tX0 = 1024, 384, 142
)

func plotImage(w, h, x0 int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w+2*x0, h+128))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{20, 20, 20, 255}), image.Point{}, draw.Src)
	return img
}

func lum(c color.Color) int {
	r, g, b, _ := c.RGBA()
	return int((r + g + b) / 3 >> 8)
}

// anyBright reports whether some pixel in the rectangle is brighter than min.
func anyBright(img image.Image, r image.Rectangle, floor int) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if lum(img.At(x, y)) > floor {
				return true
			}
		}
	}
	return false
}

func geom(dur float64) Geometry { return Geometry{X0: tX0, Y0: 64, W: tW, H: tH, DurationS: dur} }

func bandOf(lmax int) int { return 6 + 13*(2+lmax) }

func TestCompose_BandAndLines(t *testing.T) {
	t.Parallel()
	o := Overlay{
		Header: "mm-spectro/1 | 0:12",
		Timed: []Section{
			{Label: "intro", StartS: 0, EndS: 4, Lines: []string{"one", "two"}},
			{Label: "verse 1", StartS: 4, EndS: 8, Lines: []string{"three"}},
			{Label: "chorus 1", StartS: 8, EndS: 12},
		},
	}
	out, g, err := Compose(plotImage(tW, tH, tX0), geom(12), o)
	if err != nil {
		t.Fatal(err)
	}
	band := bandOf(2)
	if g.Y0 != band+64 || g.X0 != tX0 || g.W != tW || g.H != tH {
		t.Errorf("geometry = %+v, want Y0 %d", g, band+64)
	}
	if out.Bounds().Dy() != band+tH+128 || out.Bounds().Dx() != tW+2*tX0 {
		t.Errorf("bounds = %v", out.Bounds())
	}
	plotRows := image.Rect(0, 0, 1, 1)
	for _, off := range []int{341, 683} {
		found := false
		for dx := -1; dx <= 1 && !found; dx++ {
			x := tX0 + off + dx
			plotRows = image.Rect(x, g.Y0, x+1, g.Y0+tH)
			// every plot row of the column is overlay-bright
			all := true
			for y := plotRows.Min.Y; y < plotRows.Max.Y; y++ {
				if lum(out.At(x, y)) < 100 {
					all = false
					break
				}
			}
			found = all
		}
		if !found {
			t.Errorf("no boundary column near x0+%d", off)
		}
	}
	// the first section has no boundary at its own start (x0)
	if lum(out.At(tX0, g.Y0+10)) > 40 {
		t.Error("boundary drawn at t=0")
	}
	// label pixels in row 2 of every section column
	for i, s := range o.Timed {
		x0 := tX0 + int(s.StartS/12*tW+0.5)
		x1 := tX0 + int(s.EndS/12*tW+0.5)
		if !anyBright(out, image.Rect(x0+2, 16, x1-1, 29), 100) {
			t.Errorf("section %d: no label pixels", i)
		}
	}
	// header row
	if !anyBright(out, image.Rect(0, 3, tW, 16), 100) {
		t.Error("no header pixels")
	}
	// lyric rows: section 0 has two lines, section 2 none
	if !anyBright(out, image.Rect(tX0, 29, tX0+300, 55), 100) {
		t.Error("no lyric pixels in section 0")
	}
	if anyBright(out, image.Rect(tX0+683+3, 29, tX0+tW, band), 40) {
		t.Error("lyric pixels in the empty last section")
	}
}

func TestCompose_Untimed(t *testing.T) {
	t.Parallel()
	o := Overlay{Header: "mm-spectro/1 | 0:12", Structure: []string{"verse 1", "chorus 1"}}
	out, g, err := Compose(plotImage(tW, tH, tX0), geom(12), o)
	if err != nil {
		t.Fatal(err)
	}
	if g.Y0 != bandOf(0)+64 {
		t.Errorf("Y0 = %d", g.Y0)
	}
	if !anyBright(out, image.Rect(0, 16, 400, 29), 100) {
		t.Error("structure row missing")
	}
	for x := tX0; x < tX0+tW; x++ {
		if lum(out.At(x, g.Y0+100)) > 40 {
			t.Fatalf("boundary column at %d in an untimed overlay", x)
		}
	}
}

func TestCompose_Truncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("abcdefghij", 30)
	many := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8"}
	o := Overlay{Header: "h", Timed: []Section{
		{Label: "a", StartS: 0, EndS: 6, Lines: []string{long}},
		{Label: "b", StartS: 6, EndS: 12, Lines: many},
		{Label: "c", StartS: 12, EndS: 18, Lines: []string{"x", "y"}},
	}}
	out, g, err := Compose(plotImage(tW, tH, tX0), geom(18), o)
	if err != nil {
		t.Fatal(err)
	}
	band := bandOf(6)
	if g.Y0 != band+64 {
		t.Errorf("Y0 = %d, want %d", g.Y0, band+64)
	}
	b1 := tX0 + int(math.Round(6.0/18*float64(tW)))
	// the long line (row 3) stays inside section a: nothing bright between a's end and b's own text start region? b has its own l1 at row 3, so
	// check the pixels just left of the boundary (last 2 px of a) are empty in row 3.
	if anyBright(out, image.Rect(b1-3, 29, b1, 42), 40) {
		t.Error("long line leaks past its section")
	}
	// section a's text reaches close to the boundary (it is truncated, not dropped)
	if !anyBright(out, image.Rect(b1-60, 29, b1-8, 42), 100) {
		t.Error("long line was not drawn up to the section edge")
	}
	// "+N more" occupies the last line row (row 8) in section b only
	last := image.Rect(tX0, 3+13*7, tX0+tW, 3+13*8)
	if !anyBright(out, image.Rect(b1+2, last.Min.Y, b1+120, last.Max.Y), 100) {
		t.Error("no +N more row in section b")
	}
	if anyBright(out, image.Rect(tX0+3, last.Min.Y, b1-3, last.Max.Y), 40) {
		t.Error("section a has a last row")
	}
	if got := sectionLines(Section{Lines: many}, 6); strings.Join(got, "|") != "l1|l2|l3|l4|l5|+3 more" {
		t.Errorf("sectionLines = %q", got)
	}
	if got := sectionLines(Section{Lines: many[:6]}, 6); len(got) != 6 || got[5] != "l6" {
		t.Errorf("sectionLines(6) = %q", got)
	}
}

func TestCompose_GeometryMismatch(t *testing.T) {
	t.Parallel()
	_, _, err := Compose(plotImage(tW, tH, tX0+1), geom(12), Overlay{Header: "h"})
	if !errors.Is(err, ErrGeometry) || Code(err) != "SPECTROGRAM_GEOMETRY" {
		t.Errorf("err = %v", err)
	}
}

func TestOverlaySHA(t *testing.T) {
	t.Parallel()
	a := Overlay{Header: "h", Timed: []Section{{Label: "x", EndS: 1}}}
	b := a
	b.Header = "h2"
	if OverlaySHA(a) != OverlaySHA(Overlay{Header: "h", Timed: []Section{{Label: "x", EndS: 1}}}) || OverlaySHA(a) == OverlaySHA(b) || len(OverlaySHA(a)) != 64 {
		t.Error("OverlaySHA not stable/sensitive")
	}
}
