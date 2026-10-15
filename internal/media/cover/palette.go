package cover

import "image/color"

// hsl converts an HSL colour to sRGB with integer arithmetic only (round half up): h in whole degrees, s and l in
// per-mille. Exact, so the palette is identical on every platform.
func hsl(h, s, l int) color.RGBA {
	h = ((h % 360) + 360) % 360
	// chroma scaled by 1e6 (s and l are per-mille), then by 60 so the hue sector division stays integral.
	c := int64(1000-abs(2*l-1000)) * int64(s)
	hm := h % 120 // (h/60 mod 2) * 60
	x := c * int64(60-abs(hm-60))
	c *= 60
	var r, g, b int64
	switch h / 60 {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := int64(l)*60000 - c/2
	const d = 60 * 1000 * 1000
	ch := func(v int64) uint8 {
		return uint8((2*(v+m)*255 + d) / (2 * d)) //nolint:gosec // 0..255 by construction
	}
	return color.RGBA{R: ch(r), G: ch(g), B: ch(b), A: 255}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// blend mixes c into bg by pct percent, rounding half up.
func blend(bg, c color.RGBA, pct int) color.RGBA {
	mix := func(a, b uint8) uint8 {
		return uint8((int(a)*(100-pct) + int(b)*pct + 50) / 100) //nolint:gosec // a convex mix of two bytes
	}
	return color.RGBA{R: mix(bg.R, c.R), G: mix(bg.G, c.G), B: mix(bg.B, c.B), A: 255}
}

// Palette slots: the four base colours, then 20/40/60/80 % blends of primary, secondary and ink with the background.
const (
	slotBG = iota
	slotPrimary
	slotSecondary
	slotInk
)

// makePalette builds the 16-entry palette from the root hue.
func makePalette(hue int) color.Palette {
	bg := hsl(hue, 350, 120)
	base := [4]color.RGBA{bg, hsl(hue+150, 650, 550), hsl(hue+210, 550, 700), hsl(hue, 200, 920)}
	pal := make(color.Palette, 0, 16)
	for _, c := range base {
		pal = append(pal, c)
	}
	for _, c := range base[1:] {
		for _, pct := range [4]int{20, 40, 60, 80} {
			pal = append(pal, blend(bg, c, pct))
		}
	}
	return pal
}

// nearest returns the palette index closest to the colour (squared RGB distance, lowest index on ties).
func nearest(pal color.Palette, r, g, b int) uint8 {
	best, bestD := 0, int(^uint(0)>>1)
	for i, c := range pal {
		p := c.(color.RGBA)
		dr, dg, db := int(p.R)-r, int(p.G)-g, int(p.B)-b
		if d := dr*dr + dg*dg + db*db; d < bestD {
			best, bestD = i, d
		}
	}
	return uint8(best)
}
