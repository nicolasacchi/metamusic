package atlas

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// pageCapBytes is the largest page body (C141.3): a page is cut at a whole point so it never exceeds it.
const pageCapBytes = 90_000

// defaultNeighbours is k when near_song_id comes without one.
const defaultNeighbours = 8

// plot is one plotted Song: its scope row, the layout input and the bases of its axes.
type plot struct {
	ri     int // index into the scope rows
	in     Input
	xb, yb string
}

// Get answers get_song_atlas in one read-only transaction: the scope, the layout (cached by token), the overlay, the
// neighbours and the totals, paged by song id. It never writes.
func (s *Service) Get(ctx context.Context, q Query) (Atlas, error) {
	if q.X == "" {
		q.X = "tempo"
	}
	if q.Y == "" {
		q.Y = "energy"
	}
	if q.ColorBy == "" {
		q.ColorBy = "none"
	}
	xa, ok := Axis(q.X)
	if !ok {
		return Atlas{}, unknownAxis("x", q.X)
	}
	ya, ok := Axis(q.Y)
	if !ok {
		return Atlas{}, unknownAxis("y", q.Y)
	}
	if q.X == q.Y {
		return Atlas{}, problem.E(http.StatusUnprocessableEntity, "AXIS_PAIR_INVALID", "x and y must be two different axes", map[string]any{"x": q.X, "y": q.Y})
	}
	if !slices.Contains(colorBys, q.ColorBy) {
		return Atlas{}, problem.E(http.StatusUnprocessableEntity, "UNKNOWN_COLOR_BY", "color_by is not one of the known overlays", map[string]any{"color_by": q.ColorBy, "color_bys": colorBys})
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultPage
	}
	limit = min(limit, MaxPage)
	var curTok, curLast string
	if q.Cursor != "" {
		var err error
		if curTok, curLast, err = decodeCursor(q.Cursor); err != nil {
			return Atlas{}, err
		}
	}

	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Atlas{}, fmt.Errorf("atlas: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only: nothing to roll back
	tax, err := taxonomyVersion(ctx, tx)
	if err != nil {
		return Atlas{}, err
	}
	cols := featureColumns(xa, ya, q.ColorBy, q.NearSongID != "" && q.Cursor == "")
	rows, scopeTotal, err := scope(ctx, tx, q.Filter, q.Dim, s.maxScope, cols)
	if err != nil {
		return Atlas{}, err
	}
	colour := colourValue(q.ColorBy)
	tok, err := token(q, tax, rows, colour)
	if err != nil {
		return Atlas{}, fmt.Errorf("atlas: token: %w", err)
	}
	if q.Cursor != "" && curTok != tok {
		return Atlas{}, stale()
	}

	var nb []Neighbour
	if q.NearSongID != "" && q.Cursor == "" {
		k := q.K
		if k <= 0 {
			k = defaultNeighbours
		}
		var near row
		var found bool
		if i := slices.IndexFunc(rows, func(r row) bool { return r.id == q.NearSongID }); i >= 0 {
			near, found = rows[i], true
		} else if near, found, err = one(ctx, tx, q.NearSongID, cols); err != nil {
			return Atlas{}, err
		}
		if !found {
			return Atlas{}, problem.E(http.StatusNotFound, "NOT_FOUND", "no such Song", map[string]any{"id": q.NearSongID})
		}
		nb = neighbours(near, rows, min(k, MaxNeighbours))
	}
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.id] = true
	}
	pending, err := pendingFor(ctx, tx, ids)
	if err != nil {
		return Atlas{}, err
	}

	plots, missing := s.plots(rows, xa, ya)
	inputs := make([]Input, len(plots))
	for i, p := range plots {
		inputs[i] = p.in
	}
	now := s.now()
	xy, hit := s.cache.get(tok, now)
	if !hit || len(xy) != len(plots) {
		xy = Layout(inputs, xa, ya)
		s.cache.put(tok, xy, now)
	}

	vals := make([]string, len(plots))
	for i, p := range plots {
		vals[i] = colour(rows[p.ri])
	}
	cidx, legend, rank := overlay(vals)
	if q.ColorBy == "none" {
		legend = []LegendEntry{} // no overlay: one colour, nothing to explain
	}
	var museIDs []string
	miOf := map[string]int{}
	if q.ColorBy == "muse" {
		for _, e := range legend {
			if e.Value != otherValue && e.Value != "none" && e.C < paletteSize {
				miOf[e.Value] = len(museIDs)
				museIDs = append(museIDs, e.Value)
			}
		}
	}

	out := Atlas{Def: Def, GeneratedAt: store.FormatTime(now), TaxonomyVersion: tax, LayoutToken: tok, Pending: pending,
		Guides: guides(q.X, q.Y), ColorBy: q.ColorBy, Legend: legend, Muses: []string{}, Neighbors: nb, Points: []Point{}}
	if museIDs != nil {
		out.Muses = museIDs
	}
	out.Totals = Totals{Scope: len(rows), ScopeTotal: scopeTotal, Truncated: scopeTotal > len(rows), Plotted: len(plots), Missing: missing}
	for _, p := range plots {
		if rows[p.ri].h == 1 {
			out.Totals.Hit++
		}
		count(&out.Totals.Basis.X, p.xb)
		count(&out.Totals.Basis.Y, p.yb)
	}
	out.Axes = AxisPair{X: axisInfo(xa, plots, true), Y: axisInfo(ya, plots, false)}
	point := func(i int) Point {
		p, r := plots[i], &rows[plots[i].ri]
		pt := Point{ID: r.id, T: truncRunes(r.title, TitleRunes), X: xy[i].X, Y: xy[i].Y, C: cidx[i], ST: r.st, PK: r.pk, XB: p.xb, YB: p.yb}
		if q.Dim {
			h := r.h
			pt.H = &h
		}
		if q.ColorBy == "muse" {
			if _, ok := rank[vals[i]]; ok {
				if mi, ok := miOf[vals[i]]; ok {
					pt.MI = &mi
				}
			}
		}
		return pt
	}

	// Page: points after the cursor, at most limit, cut at a whole point before the byte cap.
	start := 0
	if curLast != "" {
		start, _ = slices.BinarySearchFunc(plots, curLast, func(p plot, id string) int { return strings.Compare(p.in.ID, id) })
		if start < len(plots) && plots[start].in.ID == curLast {
			start++
		}
	}
	skeleton, err := json.Marshal(out)
	if err != nil {
		return Atlas{}, fmt.Errorf("atlas: encode: %w", err)
	}
	budget := pageCapBytes - len(skeleton) - 128 // room for the cursor
	end, used := start, 0
	for end < len(plots) && end-start < limit {
		pt := point(end)
		b, err := json.Marshal(pt)
		if err != nil {
			return Atlas{}, fmt.Errorf("atlas: encode: %w", err)
		}
		if end > start && used+len(b)+1 > budget {
			break
		}
		used += len(b) + 1
		out.Points = append(out.Points, pt)
		end++
	}
	if end < len(plots) {
		c := encodeCursor(tok, plots[end-1].in.ID)
		out.NextCursor = &c
	}
	return out, nil
}

func unknownAxis(which, name string) error {
	names := make([]string, len(Axes))
	for i, a := range Axes {
		names[i] = a.Name
	}
	return problem.E(http.StatusUnprocessableEntity, "UNKNOWN_AXIS", which+" is not one of the known axes", map[string]any{"axis": which, "value": name, "axes": names})
}

func count(b *BasisCounts, basis string) {
	switch basis {
	case "m":
		b.M++
	case "d":
		b.D++
	case "l":
		b.L++
	case "p":
		b.P++
	}
}

// plots builds the plotted Songs in id order: a Song missing a numeric axis value is left out and counted in missing, a
// categorical axis puts a missing value in the (none) band.
func (s *Service) plots(rows []row, xa, ya AxisDef) ([]plot, Missing) {
	var out []plot
	var miss Missing
	for i := range rows {
		r := &rows[i]
		p := plot{ri: i, in: Input{ID: r.id}}
		okx, oky := true, true
		if xa.Kind == KindNumeric {
			p.in.XV, p.xb, okx = r.numeric(xa)
		} else {
			p.in.XC, p.xb = r.category(xa)
		}
		if ya.Kind == KindNumeric {
			p.in.YV, p.yb, oky = r.numeric(ya)
		} else {
			p.in.YC, p.yb = r.category(ya)
		}
		if !okx {
			miss.X++
		}
		if !oky {
			miss.Y++
		}
		if okx && oky {
			out = append(out, p)
		}
	}
	return out, miss
}

// axisInfo describes an axis of the response; the categories are the bands Layout draws (present values in the axis' fixed
// order, then (none)), counted over the plotted Songs.
func axisInfo(a AxisDef, plots []plot, isX bool) AxisInfo {
	info := AxisInfo{Name: a.Name, Kind: a.Kind, Unit: a.Unit}
	if a.Kind == KindNumeric {
		info.Scale = a.Scale
		lo, hi := a.Min, a.Max
		info.Min, info.Max = &lo, &hi
		return info
	}
	counts := make([]int, len(a.Order)+1)
	for _, p := range plots {
		v := p.in.YC
		if isX {
			v = p.in.XC
		}
		i := slices.Index(a.Order, v)
		if v == "" || i < 0 {
			i = len(a.Order)
		}
		counts[i]++
	}
	var present []int
	for i, c := range counts {
		if c > 0 {
			present = append(present, i)
		}
	}
	info.Categories = []Category{}
	for n, i := range present {
		name := NoneBand
		if i < len(a.Order) {
			name = a.Order[i]
		}
		info.Categories = append(info.Categories, Category{Value: name, Lo: coord(n * q16One / len(present)), Hi: coord((n + 1) * q16One / len(present)), Count: counts[i]})
	}
	return info
}

// guides are the drawing hints of the requested-versus-got pair: the identity line and the two octave lines.
func guides(x, y string) []Guide {
	if (x == "bpm_requested" && y == "bpm_measured") || (x == "bpm_measured" && y == "bpm_requested") {
		return []Guide{{Kind: "identity"}, {Kind: "octave_up"}, {Kind: "octave_down"}}
	}
	return []Guide{}
}

// truncRunes cuts s to at most n runes.
func truncRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
