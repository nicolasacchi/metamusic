package cover

import (
	"crypto/sha256"
	"encoding/binary"
	"image"
	"image/color"
	"math"
)

type ring struct{ lo2, hi2 float64 } // inner and outer radius squared, in size fractions

type bar struct{ x0, x1, y0 float64 } // columns [x0, x1) from y0 to the bottom edge

// Render draws the cover of (rootID, seed) at size x size pixels: concentric rings and equaliser bars over a
// palette derived from the root alone (C110.1). Pure and deterministic.
func Render(rootID string, seed int64, size int) *image.Paletted {
	if size < 1 {
		size = 1
	}
	h := sha256.Sum256([]byte(Version + "|" + rootID))
	hue := int(uint16(h[0])<<8|uint16(h[1])) % 360
	pal := makePalette(hue)
	r := splitmix64{s: uint64(seed) ^ binary.BigEndian.Uint64(h[8:16])} //nolint:gosec // bit reinterpretation of the seed is the algorithm

	// shapes, in the binding order: rings, then bars
	nRings := 3 + int(r.next()%4)
	cx := 0.30 + 0.40*r.unit()
	cy := 0.25 + 0.30*r.unit()
	outer := 0.30 + 0.15*r.unit()
	rings := make([]ring, 0, nRings)
	ringSlot := make([]uint8, 0, nRings)
	for i := range nRings {
		w := 0.02 + 0.04*r.unit()
		if outer-w <= 0 {
			break
		}
		rings = append(rings, ring{lo2: (outer - w) * (outer - w), hi2: outer * outer})
		slot := uint8(slotPrimary)
		if i%2 == 1 {
			slot = slotSecondary
		}
		ringSlot = append(ringSlot, slot)
		outer -= 2 * w // a gap as wide as the ring
	}
	nBars := 12 + int(r.next()%13)
	bars := make([]bar, nBars)
	for i := range bars {
		x0 := float64(i) / float64(nBars)
		bars[i] = bar{x0: x0, x1: x0 + 0.8/float64(nBars), y0: 1 - (0.05 + 0.25*r.unit())} // 20 % gap between bars
	}

	img := image.NewPaletted(image.Rect(0, 0, size, size), pal)
	// Samples sit on a 2x2 grid per pixel (4 per pixel). Each sample row is painted span by span instead of
	// testing every sample against every shape: rings are disjoint annuli, bars sit on top.
	n := sub * size
	fn := float64(n)
	rows := [sub][]uint8{make([]uint8, n), make([]uint8, n)}
	for y := range size {
		for sy := range sub {
			paintRow(rows[sy], (float64(y*sub+sy)+0.5)/fn, fn, cx, cy, rings, ringSlot, bars)
		}
		for x := range size {
			var cnt [4]int
			for sy := range sub {
				cnt[rows[sy][x*sub]]++
				cnt[rows[sy][x*sub+1]]++
			}
			img.Pix[y*img.Stride+x] = pixelIndex(pal, cnt)
		}
	}
	return img
}

// sub is the number of samples per pixel along each axis.
const sub = 2

// span clamps the closed fraction interval [a, b] to the sample indices (i+0.5)/n it contains: [lo, hi], empty
// when lo > hi.
func span(a, b, fn float64, n int) (lo, hi int) {
	lo = int(math.Ceil(float64(a*fn) - 0.5))
	hi = int(math.Floor(float64(b*fn) - 0.5))
	return max(lo, 0), min(hi, n-1)
}

func fill(row []uint8, lo, hi int, slot uint8) {
	for i := lo; i <= hi; i++ {
		row[i] = slot
	}
}

// paintRow fills row (n samples) with the base colour slots along the sample row at height py.
func paintRow(row []uint8, py, fn, cx, cy float64, rings []ring, ringSlot []uint8, bars []bar) {
	n := len(row)
	clear(row) // slotBG
	dy := py - cy
	dy2 := float64(dy * dy)
	for i, rg := range rings {
		if dy2 > rg.hi2 {
			continue
		}
		half := math.Sqrt(rg.hi2 - dy2)
		if dy2 >= rg.lo2 {
			lo, hi := span(cx-half, cx+half, fn, n)
			fill(row, lo, hi, ringSlot[i])
			continue
		}
		in := math.Sqrt(rg.lo2 - dy2)
		lo, hi := span(cx-half, cx-in, fn, n)
		fill(row, lo, hi, ringSlot[i])
		lo, hi = span(cx+in, cx+half, fn, n)
		fill(row, lo, hi, ringSlot[i])
	}
	if py < 0.65 {
		return
	}
	for _, b := range bars {
		if py >= b.y0 {
			lo := int(math.Ceil(float64(b.x0*fn) - 0.5))
			hi := int(math.Ceil(float64(b.x1*fn)-0.5)) - 1 // x1 exclusive
			fill(row, max(lo, 0), min(hi, n-1), slotInk)
		}
	}
}

// pixelIndex maps the four samples of a pixel to a palette entry: a uniform pixel keeps its base colour, a mixed
// one takes the nearest entry to the average (no dithering).
func pixelIndex(pal color.Palette, cnt [4]int) uint8 {
	total := cnt[0] + cnt[1] + cnt[2] + cnt[3]
	var r, g, b int
	for s, n := range cnt {
		if n == total {
			return uint8(s)
		}
		c := pal[s].(color.RGBA)
		r += int(c.R) * n
		g += int(c.G) * n
		b += int(c.B) * n
	}
	return nearest(pal, (2*r+total)/(2*total), (2*g+total)/(2*total), (2*b+total)/(2*total))
}
