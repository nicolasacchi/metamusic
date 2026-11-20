package abc

// TimedNote is one note or rest of a voice in source order (repeats are not expanded). Start and Dur are whole notes
// from the tune start; MIDI is -1 for a rest. The key signature and the dialect's accidentals are applied.
type TimedNote struct {
	Bar        int
	Start, Dur Rat
	MIDI       int
}

// BeatNote is a TimedNote in beats of BeatUnit (the Q: unit, else a quarter note).
type BeatNote struct {
	Bar        int
	MIDI       int
	Onset, Dur float64
}

// barStarts returns the whole-note start of every bar of a voice (the sum of the lengths before it).
func barStarts(v Voice) []Rat {
	out := make([]Rat, len(v.Bars))
	pos := Rat{0, 1}
	for i, b := range v.Bars {
		out[i] = pos
		pos = pos.Add(b.Length)
	}
	return out
}

// Notes flattens one voice (by index in t.Voices): every note and rest in source order with its time from the tune start.
// Chord notes appear one per pitch at the same Start. An index out of range returns nil.
func (t *Tune) Notes(voice int) []TimedNote {
	if t == nil || voice < 0 || voice >= len(t.Voices) {
		return nil
	}
	v := t.Voices[voice]
	starts := barStarts(v)
	var out []TimedNote
	for i, b := range v.Bars {
		for _, n := range b.Notes {
			out = append(out, TimedNote{Bar: i, Start: starts[i].Add(n.At), Dur: n.Dur, MIDI: n.Pitch})
		}
	}
	return out
}

// BeatUnit is the length of one beat: the unit of Q:, else a quarter note.
func (t *Tune) BeatUnit() Rat {
	if t.Header.Tempo != nil {
		return t.Header.Tempo.Unit
	}
	return Rat{1, 4}
}

// BeatNotes is Notes with onset and duration in beats (BeatUnit), for consumers that compare against audio.
func (t *Tune) BeatNotes(voice int) []BeatNote {
	ns := t.Notes(voice)
	if ns == nil {
		return nil
	}
	u := t.BeatUnit().Float()
	out := make([]BeatNote, len(ns))
	for i, n := range ns {
		out[i] = BeatNote{Bar: n.Bar, MIDI: n.MIDI, Onset: n.Start.Float() / u, Dur: n.Dur.Float() / u}
	}
	return out
}

// timingFeature reports whether a feature stops the tune from being timed by bar count alone.
func timingFeature(f Feature) bool {
	switch f.Code {
	case "repeat", "volta":
		return true
	case "inline_field":
		return f.Detail == "M" || f.Detail == "Q" || f.Detail == "V"
	}
	return false
}

// SecondsPerBar is the length of one bar in seconds. It is false when the tune is untimed: no usable Q:, no bars, a
// repeat, volta or mid-tune meter, tempo or voice change, or a section with no bars.
func (t *Tune) SecondsPerBar() (float64, bool) {
	if t == nil || t.Header.Tempo == nil || len(t.Voices) == 0 || len(t.Voices[0].Bars) == 0 || t.zeroSect {
		return 0, false
	}
	for _, f := range t.Features {
		if timingFeature(f) {
			return 0, false
		}
	}
	tp := t.Header.Tempo
	if tp.BPM <= 0 || tp.Unit.IsZero() || t.Header.Meter.IsZero() {
		return 0, false
	}
	return t.Header.Meter.Float() / tp.Unit.Float() * 60 / tp.BPM, true
}
