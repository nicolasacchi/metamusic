package cover

import (
	"crypto/sha256"
	"encoding/binary"
	"image"
)

// The mosaic renderer (C123): a grid of tiles, one per Song lineage, over a palette derived from the Record's identity
// alone. Every geometric constant is a Q16 fixed-point number (round(x * 65536)) and every distance is a squared Q16
// value in int64, so no floating-point unit is involved and one golden serves every architecture.
const (
	q1       = 1 << 16 // 1.0
	qHalf    = q1 / 2  // 0.5
	qGapHalf = 1311    // 2 % of a cell on each side of an edge: a 4 % gap between cells
	qShift   = 13107   // 0.2: how far a tile's centre may drift from the cell centre (total)
	qOuter0  = 19661   // 0.30
	qOuterU  = 11796   // 0.18
	qWidth0  = 3932    // 0.06
	qWidthU  = 5243    // 0.08
	qDot0    = 3277    // 0.05
	qDotU    = 2621    // 0.04
	qEmptyR  = 2621    // 0.04: the ink dot of a cell beyond the parts
	maxParts = 12      // the first parts that feed the cover (records.CoverParts)
	fillers  = 4       // tiles drawn from the identity stream when there are no parts
)

// tile is one cell's drawing: concentric rings and an ink dot around a centre, all in Q16 cell units.
type tile struct {
	empty        bool
	cx, cy       int64
	outer, width int64
	rings        int
	first        uint8 // palette slot of the outermost ring
	dot          int64
}

// RenderMosaic draws in at size x size pixels: deterministic, integer arithmetic only, no text (C123).
func RenderMosaic(in Input, size int) *image.Paletted {
	size = max(size, 1)
	h := sha256.Sum256([]byte(MosaicVersion + "|" + in.Kind + "|" + in.ID))
	hue := int(uint16(h[0])<<8|uint16(h[1])) % 360
	pal := makePalette(hue)
	r := splitmix64{s: uint64(in.Seed) ^ binary.BigEndian.Uint64(h[8:16])} //nolint:gosec // bit reinterpretation of the seed is the algorithm

	n := min(len(in.Parts), maxParts)
	var streams []*splitmix64
	switch n {
	case 0:
		n = fillers
		for range n {
			streams = append(streams, &r)
		}
	default:
		for _, p := range in.Parts[:n] {
			ph := sha256.Sum256([]byte(p.RootID))
			// The identity stream r contributes one draw per tile, so the Record's seed (cover_reroll) moves the layout even
			// though a tile is otherwise a function of its lineage alone.
			streams = append(streams, &splitmix64{s: binary.BigEndian.Uint64(ph[0:8]) ^ uint64(p.Seed) ^ r.next()}) //nolint:gosec // bit reinterpretation of the seed is the algorithm
		}
	}
	g := 4
	if n <= 4 {
		g = 2
	} else if n <= 9 {
		g = 3
	}
	tiles := make([]tile, g*g)
	for i := range tiles {
		if i >= n {
			tiles[i] = tile{empty: true}
			continue
		}
		tiles[i] = newTile(streams[i])
	}

	img := image.NewPaletted(image.Rect(0, 0, size, size), pal)
	side := int64(sub * size) // samples per axis
	G := int64(g)
	for y := range size {
		for x := range size {
			var cnt [4]int
			for sy := range sub {
				for sx := range sub {
					cnt[sample(tiles, G, side, int64(x*sub+sx), int64(y*sub+sy))]++
				}
			}
			img.Pix[y*img.Stride+x] = pixelIndex(pal, cnt)
		}
	}
	return img
}

// newTile draws a tile's parameters from its stream in the binding order: the ring count and colour order, then u1..u5.
func newTile(tr *splitmix64) tile {
	t0 := tr.next()
	rings := 2 + int(t0%3)
	first := uint8(slotPrimary)
	if t0&1 != 0 {
		first = slotSecondary
	}
	var u [5]int64
	for i := range u {
		u[i] = int64(tr.next() >> 48)
	}
	return tile{
		cx:    qHalf + (u[0]-qHalf)*qShift/q1,
		cy:    qHalf + (u[1]-qHalf)*qShift/q1,
		outer: qOuter0 + qOuterU*u[2]/q1,
		width: qWidth0 + qWidthU*u[3]/q1,
		rings: rings,
		first: first,
		dot:   qDot0 + qDotU*u[4]/q1,
	}
}

// sample returns the base palette slot of the sample at integer sample coordinates (sx, sy) of a side x side sample grid.
func sample(tiles []tile, g, side, sx, sy int64) uint8 {
	nx := (2*sx + 1) * g * q1 / (2 * side) // position in cell units, Q16
	ny := (2*sy + 1) * g * q1 / (2 * side)
	col, row := nx>>16, ny>>16
	lx, ly := nx&(q1-1), ny&(q1-1)
	if lx < qGapHalf || lx >= q1-qGapHalf || ly < qGapHalf || ly >= q1-qGapHalf {
		return slotBG
	}
	t := &tiles[row*g+col]
	if t.empty {
		if dist2(lx-qHalf, ly-qHalf) <= qEmptyR*qEmptyR {
			return slotInk
		}
		return slotBG
	}
	d2 := dist2(lx-t.cx, ly-t.cy)
	if d2 <= t.dot*t.dot {
		return slotInk
	}
	outer := t.outer
	slot := t.first
	for range t.rings {
		inner := outer - t.width
		if inner <= 0 {
			break
		}
		if d2 <= outer*outer && d2 >= inner*inner {
			return slot
		}
		outer -= 2 * t.width // a gap as wide as the ring
		slot = slotPrimary + slotSecondary - slot
	}
	return slotBG
}

func dist2(dx, dy int64) int64 { return dx*dx + dy*dy }
