package atlas

import (
	"cmp"
	"slices"
)

// Layout constants (C141.2): R0 is the spiral step 0.0035 in Q16; the cell and strip fractions are percentages.
const (
	spiralR0     = 229
	catRadiusPct = 45 // radius of the sunflower inside a band cell, percent of the band size
	stripPct     = 90 // share of the band a horizontal strip covers; the rest is the margin
	goldenMilli  = 137508
)

// Input is one point to lay out. For a numeric axis the value is XV or YV in the axis' fixed-point units (the caller
// leaves out points whose value is missing; an out-of-domain value is clamped). For a categorical axis the value is XC or
// YC and XV or YV is ignored: the bands are the categories present among the points in the axis' fixed order, and an empty
// or unknown value falls in the trailing (none) band.
type Input struct {
	ID     string
	XV, YV int
	XC, YC string
}

// axisPos is the position of one point on one axis: u in Q16 for a numeric axis, band index for a categorical one.
type axisPos struct {
	u    int
	band int
}

type axisPlan struct {
	def   AxisDef
	bands int // number of present bands (categorical only)
	pos   []axisPos
}

func (p *axisPlan) numeric() bool { return p.def.Kind == KindNumeric }

func newAxisPlan(def AxisDef, n int, num func(i int) int, cat func(i int) string) *axisPlan {
	p := &axisPlan{def: def, pos: make([]axisPos, n)}
	if def.Kind == KindNumeric {
		for i := range p.pos {
			p.pos[i].u = normalise(def, num(i))
		}
		return p
	}
	idx := make([]int, n)
	present := make([]bool, len(def.Order)+1) // the last slot is (none)
	for i := range idx {
		j := slices.Index(def.Order, cat(i))
		if cat(i) == "" || j < 0 {
			j = len(def.Order)
		}
		idx[i] = j
		present[j] = true
	}
	rank := make([]int, len(present))
	for j, ok := range present {
		if ok {
			rank[j] = p.bands
			p.bands++
		}
	}
	for i := range p.pos {
		p.pos[i].band = rank[idx[i]]
	}
	return p
}

// normalise maps a numeric value to u in [0, 65536] (Q16).
func normalise(def AxisDef, v int) int {
	v = min(max(v, def.Min), def.Max)
	if def.Max <= def.Min {
		return 0
	}
	if def.Scale == "log2" {
		lo, hi := ilog2q16(def.Min), ilog2q16(def.Max)
		return (ilog2q16(v) - lo) * q16One / (hi - lo)
	}
	return (v - def.Min) * q16One / (def.Max - def.Min)
}

type cell struct{ a, b int }

// Layout places points with the integer-only algorithm of C141.2: values are normalised to Q16, points sharing a cell are
// spread on a sunflower (Vogel) spiral in id order, and the result is rounded to 4 decimals. The output has one entry per
// input, in input order; it depends only on the points' values and ids (ties on equal ids break by input order), never on
// the input order or on a clock.
func Layout(pts []Input, x, y AxisDef) []XY {
	out := make([]XY, len(pts))
	if len(pts) == 0 {
		return out
	}
	px := newAxisPlan(x, len(pts), func(i int) int { return pts[i].XV }, func(i int) string { return pts[i].XC })
	py := newAxisPlan(y, len(pts), func(i int) int { return pts[i].YV }, func(i int) string { return pts[i].YC })

	groups := make(map[cell][]int)
	for i := range pts {
		k := cellOf(px, py, i)
		groups[k] = append(groups[k], i)
	}
	for _, members := range groups {
		slices.SortStableFunc(members, func(a, b int) int { return cmp.Compare(pts[a].ID, pts[b].ID) })
		m := len(members)
		for k, i := range members {
			qx, qy := place(px, py, i, k, m)
			out[i] = XY{X: coord(qx), Y: coord(qy)}
		}
	}
	return out
}

func cellOf(px, py *axisPlan, i int) cell {
	switch {
	case px.numeric() && py.numeric():
		return cell{quant(px.pos[i].u, QuantNumeric), quant(py.pos[i].u, QuantNumeric)}
	case !px.numeric() && !py.numeric():
		return cell{px.pos[i].band, py.pos[i].band}
	case px.numeric():
		return cell{quant(px.pos[i].u, QuantMixed), py.pos[i].band}
	default:
		return cell{px.pos[i].band, quant(py.pos[i].u, QuantMixed)}
	}
}

func quant(u, n int) int { return min(u*n/q16One, n-1) }

// place returns the Q16 position of member k of m of a group.
func place(px, py *axisPlan, i, k, m int) (int, int) {
	switch {
	case px.numeric() && py.numeric():
		dx, dy := spiralOffset(k)
		return reflect(px.pos[i].u + dx), reflect(py.pos[i].u + dy)
	case !px.numeric() && !py.numeric():
		cx, bw := bandCentre(px.pos[i].band, px.bands)
		cy, bh := bandCentre(py.pos[i].band, py.bands)
		dx, dy := sunflowerOffset(k, m, bw, bh)
		return cx + dx, cy + dy
	case px.numeric():
		return px.pos[i].u, strip(py.pos[i].band, py.bands, k, m)
	default:
		return strip(px.pos[i].band, px.bands, k, m), py.pos[i].u
	}
}

// angle returns cos and sin (Q16) of the golden angle times k, taken on the committed whole-degree table.
func angle(k int) (int, int) {
	d := int(int64(k) * goldenMilli / 1000 % 360)
	return int(spiralCos[d]), int(spiralSin[d])
}

// spiralOffset is spiral(k)*R0*sqrt(k) in Q16; k = 0 is not offset.
func spiralOffset(k int) (int, int) {
	if k == 0 {
		return 0, 0
	}
	r := spiralR0 * int(isqrt(int64(k)<<32)) >> 16
	c, s := angle(k)
	return r * c >> 16, r * s >> 16
}

// reflect folds a Q16 coordinate that left [0, 1] back inside, so edge stacks do not pile up on the border.
func reflect(q int) int {
	if q < 0 {
		q = -q
	}
	if q > q16One {
		q = 2*q16One - q
	}
	return min(max(q, 0), q16One)
}

// bandCentre returns the centre and the width of band i of n.
func bandCentre(i, n int) (centre, width int) {
	return (2*i + 1) * q16One / (2 * n), q16One / n
}

// sunflowerOffset is (cos*rho*0.45*bw, sin*rho*0.45*bh) with rho = sqrt((k+1/2)/m); a lone member is not offset.
func sunflowerOffset(k, m, bw, bh int) (int, int) {
	if m == 1 {
		return 0, 0
	}
	rho := int(isqrt(int64(2*k+1) << 32 / int64(2*m)))
	c, s := angle(k)
	return (c * rho >> 16) * bw * catRadiusPct / 100 >> 16, (s * rho >> 16) * bh * catRadiusPct / 100 >> 16
}

// strip places member k of m on a horizontal strip inside band i of n: band_lo + ((k+1/2)/m)*0.9*bw + 0.05*bw.
func strip(i, n, k, m int) int {
	lo, bw := i*q16One/n, q16One/n
	margin := (100 - stripPct) / 2
	return lo + (2*k+1)*bw*stripPct/(200*m) + bw*margin/100
}
