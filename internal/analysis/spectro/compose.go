package spectro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Def identifies the layout and rendering recipe (rendition params "def").
const Def = "mm-spectro/1"

const (
	rowH     = 13
	charW    = 7
	maxLines = 6
	padTop   = 3
)

// Section is one timed section of the overlay.
type Section struct {
	Label        string
	StartS, EndS float64
	Lines        []string
}

// Overlay is the text drawn above and over the plot.
type Overlay struct {
	Header    string
	Timed     []Section // empty when untimed
	Structure []string  // used only when untimed
}

// Geometry locates the plot inside the image.
type Geometry struct {
	X0, Y0, W, H int
	DurationS    float64
}

// OverlaySHA is the sha256 (hex) of the canonical overlay JSON.
func OverlaySHA(o Overlay) string {
	b, _ := json.Marshal(o)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var sectionColors = []color.RGBA{{0xF2, 0xA6, 0x5A, 255}, {0x9A, 0xD0, 0xEC, 255}}

// sectionLines returns the rows drawn for a section: all lines when they fit
// in lmax rows, else lmax-1 lines and a "+N more" row.
func sectionLines(s Section, lmax int) []string {
	if len(s.Lines) <= lmax {
		return s.Lines
	}
	keep := lmax - 1
	return append(append([]string{}, s.Lines[:keep]...), fmt.Sprintf("+%d more", len(s.Lines)-keep))
}

// Compose draws the annotation band above plot (the full ffmpeg image) and the
// section boundary lines over it. The returned Geometry has Y0 moved down by
// the band height.
func Compose(plot image.Image, g Geometry, o Overlay) (*image.RGBA, Geometry, error) {
	pb := plot.Bounds()
	if pb.Min != (image.Point{}) || pb.Dx() != g.W+2*g.X0 || pb.Dy() != g.H+128 || g.DurationS <= 0 {
		return nil, Geometry{}, fmt.Errorf("%w: image %dx%d, want %dx%d", ErrGeometry, pb.Dx(), pb.Dy(), g.W+2*g.X0, g.H+128)
	}
	lmax := 0
	for _, s := range o.Timed {
		lmax = max(lmax, len(s.Lines))
	}
	lmax = min(lmax, maxLines)
	band := 6 + rowH*(2+lmax)
	out := image.NewRGBA(image.Rect(0, 0, pb.Dx(), band+pb.Dy()))
	draw.Draw(out, image.Rect(0, 0, pb.Dx(), band), image.NewUniform(color.Black), image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(0, band, pb.Dx(), band+pb.Dy()), plot, image.Point{}, draw.Src)

	text := func(x, row int, s string, maxPx int, c color.Color) {
		n := maxPx / charW
		s = FoldASCII(s)
		if len(s) > n {
			s = s[:max(n, 0)]
		}
		if s == "" {
			return
		}
		d := font.Drawer{Dst: out, Src: image.NewUniform(c), Face: basicfont.Face7x13,
			Dot: fixed.P(x, padTop+rowH*(row-1)+11)}
		d.DrawString(s)
	}
	text(2, 1, o.Header, pb.Dx()-4, color.White)
	if len(o.Timed) == 0 {
		if len(o.Structure) > 0 {
			text(g.X0+2, 2, "structure: "+strings.Join(o.Structure, ", "), g.W-4, color.White)
		}
	}
	col := func(t float64) int {
		return g.X0 + int(math.Round(math.Min(math.Max(t, 0), g.DurationS)/g.DurationS*float64(g.W)))
	}
	plotBottom := band + g.Y0 + g.H
	for i, s := range o.Timed {
		xs, xe := col(s.StartS), col(s.EndS)
		c := sectionColors[i%2]
		w := xe - xs - 2
		if w/charW >= 3 {
			text(xs+2, 2, s.Label, w, c)
		}
		for j, l := range sectionLines(s, lmax) {
			text(xs+2, 3+j, l, w, color.White)
		}
		if s.StartS > 0 && s.StartS < g.DurationS {
			for y := padTop + rowH; y < plotBottom; y++ {
				p := out.RGBAAt(xs, y)
				out.SetRGBA(xs, y, color.RGBA{blend(p.R), blend(p.G), blend(p.B), 255})
			}
		}
	}
	g.Y0 += band
	return out, g, nil
}

// blend mixes c with white at 55 % alpha.
func blend(c uint8) uint8 { return uint8(min((int(c)*45+255*55+50)/100, 255)) } //nolint:gosec // clamped to 0..255
