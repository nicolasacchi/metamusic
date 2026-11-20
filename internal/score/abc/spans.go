package abc

// Span locates one bar in the source: the byte range of its content, and its start time when the tune is timed.
type Span struct {
	Voice      string
	Bar        int
	Start, End int
	StartS     *float64
}

// BarSpans lists every bar of every voice in voice order. StartS is set when the tune is timed (SecondsPerBar is true):
// the bar's cumulative start in seconds.
func (t *Tune) BarSpans() []Span {
	if t == nil {
		return nil
	}
	_, timed := t.SecondsPerBar()
	var out []Span
	for _, v := range t.Voices {
		starts := barStarts(v)
		for i, b := range v.Bars {
			sp := Span{Voice: v.ID, Bar: i, Start: b.Start, End: b.End}
			if timed {
				tp := t.Header.Tempo
				// whole notes per second = BPM * unit / 60
				s := starts[i].Float() / (tp.BPM * tp.Unit.Float() / 60)
				sp.StartS = &s
			}
			out = append(out, sp)
		}
	}
	return out
}
