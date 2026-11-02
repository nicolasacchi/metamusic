package atlas

import (
	"cmp"
	"slices"
)

// paletteSize is the number of distinct overlay colours; ranks from here on share the last index ("other").
const paletteSize = 12

// otherValue is the legend value of the shared last colour.
const otherValue = "other"

var (
	stateNames = []string{"draft", "rendered", "judged", "picked"}
	pickNames  = []string{"none", "newest", "stars", "ladder", "pinned"}
	recNames   = []string{"none", "draft_record", "published_record"}
)

// colorBys are the accepted color_by values.
var colorBys = []string{"none", "muse", "state", "pick", "family", "vocal", "era", "mood", "language", "key", "record"}

// colourValue returns the overlay value of a row: the Muse id ("none" without one), a state, Pick or Record word, or the
// value of a Sound or key axis ("(none)" when the Song lacks it). color_by none has no value.
func colourValue(by string) func(row) string {
	switch by {
	case "muse":
		return func(r row) string {
			if r.muse == "" {
				return "none"
			}
			return r.muse
		}
	case "state":
		return func(r row) string { return stateNames[r.st] }
	case "pick":
		return func(r row) string { return pickNames[r.pk] }
	case "record":
		return func(r row) string { return recNames[r.rec] }
	case "family", "vocal", "era", "mood", "language", "key":
		a, _ := Axis(by)
		return func(r row) string {
			if v, _ := r.category(a); v != "" {
				return v
			}
			return NoneBand
		}
	}
	return func(row) string { return "" }
}

// overlay assigns the colour index of every value over vals (the plotted points' values, in point order) and builds the
// legend: entries ordered by count descending then value, palette index = rank, ranks from paletteSize on merged into one
// "other" entry. idx[i] is the colour of vals[i]; rank maps a value to its legend position among the first paletteSize.
func overlay(vals []string) (idx []int, legend []LegendEntry, rank map[string]int) {
	idx = make([]int, len(vals))
	legend = []LegendEntry{}
	rank = map[string]int{}
	counts := map[string]int{}
	for _, v := range vals {
		counts[v]++
	}
	keys := make([]string, 0, len(counts))
	for v := range counts {
		keys = append(keys, v)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(counts[b], counts[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	other := 0
	for i, v := range keys {
		if i < paletteSize {
			rank[v] = i
			legend = append(legend, LegendEntry{C: i, Value: v, Count: counts[v]})
		} else {
			other += counts[v]
		}
	}
	if other > 0 {
		legend = append(legend, LegendEntry{C: paletteSize, Value: otherValue, Count: other})
	}
	for i, v := range vals {
		if r, ok := rank[v]; ok {
			idx[i] = r
		} else {
			idx[i] = paletteSize
		}
	}
	return idx, legend, rank
}
