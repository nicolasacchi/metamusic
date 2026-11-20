package abc

import (
	"errors"
	"math"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/score"
)

// EventOptions configures Tune.Score.
type EventOptions struct {
	Chords   ChordMode      // "" and "none": chord symbols are silent; "block": a close-position chord voice
	Programs map[string]int // General MIDI program by voice id, used when the voice has no %%MIDI program
}

const maxExpandedBars = 200000

// expandBars returns the indexes of a voice's bars in playing order: repeats and first/second endings expanded.
func expandBars(bars []Bar) ([]int, error) {
	var out []int
	start, pass := 0, 1
	lastVolta := 0
	for i := 0; i < len(bars); {
		b := bars[i]
		if b.RepeatStart && !(pass == 2 && i == start) {
			start, pass = i, 1
		}
		if b.Volta > 0 && b.Volta != pass {
			i++
			continue
		}
		if b.Volta == 0 && lastVolta > 0 && pass == 2 {
			pass, start = 1, i
		}
		lastVolta = b.Volta
		out = append(out, i)
		if len(out) > maxExpandedBars {
			return nil, errTooMany
		}
		if b.RepeatEnd {
			if pass == 1 {
				pass = 2
				lastVolta = 0
				i = start
				continue
			}
			pass, start = 1, i+1
		}
		i++
	}
	return out, nil
}

var errTooMany = errors.New("too many events")

func usPerQ(bpm float64, unit Rat) uint32 {
	qpm := bpm * 4 * unit.Float()
	if qpm <= 0 || math.IsNaN(qpm) || math.IsInf(qpm, 0) {
		return 500000
	}
	us := math.Round(6e7 / qpm)
	return uint32(max(1, min(us, 0xFFFFFF)))
}

// Score turns the tune into note events: repeats and endings expanded, ties merged, tempo and meter changes in the maps.
// Velocity is constant (80; 60 for block chords).
func (t *Tune) Score(o EventOptions) (*score.Score, error) {
	if t == nil || len(t.Voices) == 0 {
		return nil, &SyntaxError{Line: 1, Col: 1, Rule: "empty_score", Msg: "no notes"}
	}
	extra := 0
	chordSrc := -1
	if o.Chords == ChordsBlock {
		for i, v := range t.Voices {
			for _, b := range v.Bars {
				if len(b.Chords) > 0 {
					chordSrc = i
					break
				}
			}
			if chordSrc >= 0 {
				break
			}
		}
		if chordSrc >= 0 {
			extra = 1
		}
	}
	chans, ok := assignChannels(t.Voices, extra)
	if !ok {
		return nil, &SyntaxError{Line: 1, Col: 1, Rule: "too_many_voices", Msg: "more voices than MIDI channels"}
	}
	s := &score.Score{PPQ: score.PPQ, Warnings: append([]Warning(nil), t.Warnings...)}
	h := t.Header
	bpm, unit := t.opts.bpm(), Rat{1, 4}
	if h.Tempo != nil {
		bpm, unit = h.Tempo.BPM, h.Tempo.Unit
	}
	s.Tempos = []score.Tempo{{Tick: 0, USPerQ: usPerQ(bpm, unit)}}
	mn, md := h.MeterNum, h.MeterDen
	if mn == 0 || md == 0 {
		mn, md = 4, 4
	}
	s.Meters = []score.Meter{{Tick: 0, Num: mn, Den: md}}
	ppq := int64(score.PPQ)
	total := 0
	addChange := func(tick int64, c Change) {
		if c.Tempo != nil {
			e := score.Tempo{Tick: tick, USPerQ: usPerQ(c.Tempo.BPM, c.Tempo.Unit)}
			for _, x := range s.Tempos {
				if x == e {
					return
				}
			}
			s.Tempos = append(s.Tempos, e)
		}
		if c.MeterNum > 0 {
			e := score.Meter{Tick: tick, Num: c.MeterNum, Den: c.MeterDen}
			for _, x := range s.Meters {
				if x == e {
					return
				}
			}
			s.Meters = append(s.Meters, e)
		}
	}
	var chordNotes []score.Note
	for vi, v := range t.Voices {
		order, err := expandBars(v.Bars)
		if err != nil {
			return nil, &SyntaxError{Line: 1, Col: 1, Rule: "too_many_events", Msg: "repeats expand to too many bars"}
		}
		sv := score.Voice{ID: v.ID, Name: v.Name, Program: programFor(v, o), Channel: chans[vi]}
		if sv.Name == "" {
			sv.Name = v.ID
		}
		pos := Rat{0, 1}
		open := map[int]int{} // pitch -> index of the sounding tied note
		for _, bi := range order {
			b := v.Bars[bi]
			if vi == 0 {
				s.BarTicks = append(s.BarTicks, pos.ticks(ppq))
			}
			for _, c := range b.Changes {
				addChange(pos.Add(c.At).ticks(ppq), c)
			}
			for _, n := range b.Notes {
				if n.Pitch < 0 {
					continue
				}
				a := pos.Add(n.At)
				st, en := a.ticks(ppq), a.Add(n.Dur).ticks(ppq)
				if x, ok := open[n.Pitch]; ok && sv.Notes[x].Tick+sv.Notes[x].Dur == st {
					sv.Notes[x].Dur = en - sv.Notes[x].Tick
					if !n.Tie {
						delete(open, n.Pitch)
					}
					continue
				}
				delete(open, n.Pitch)
				sv.Notes = append(sv.Notes, score.Note{Pitch: n.Pitch, Vel: defaultVelocity, Tick: st, Dur: max(en-st, 1)})
				total++
				if total > score.MaxEvents {
					return nil, &SyntaxError{Line: 1, Col: 1, Rule: "too_many_events", Msg: "too many notes"}
				}
				if n.Tie {
					open[n.Pitch] = len(sv.Notes) - 1
				}
			}
			if vi == chordSrc {
				blen := b.Length
				if blen.IsZero() {
					blen = h.Meter
				}
				cs := append([]Chord(nil), b.Chords...)
				sort.SliceStable(cs, func(a, c int) bool { return cs[a].At.Cmp(cs[c].At) < 0 })
				for ci, c := range cs {
					endAt := blen
					if ci+1 < len(cs) {
						endAt = cs[ci+1].At
					}
					st, en := pos.Add(c.At).ticks(ppq), pos.Add(endAt).ticks(ppq)
					if en <= st {
						continue
					}
					for _, pch := range ChordNotes(c) {
						chordNotes = append(chordNotes, score.Note{Pitch: pch, Vel: chordVelocity, Tick: st, Dur: en - st})
						total++
					}
					if total > score.MaxEvents {
						return nil, &SyntaxError{Line: 1, Col: 1, Rule: "too_many_events", Msg: "too many notes"}
					}
				}
			}
			pos = pos.Add(b.Length)
		}
		if vi == 0 {
			s.Bars = len(order)
		}
		s.Voices = append(s.Voices, sv)
	}
	if extra == 1 {
		s.Voices = append(s.Voices, score.Voice{ID: chordVoiceID, Name: chordVoiceID, Program: chordProgram, Channel: chans[len(t.Voices)], Notes: chordNotes})
	}
	// collapse same-tick map entries (the later one wins)
	s.Tempos = lastPerTick(s.Tempos, func(x score.Tempo) int64 { return x.Tick })
	s.Meters = lastPerTick(s.Meters, func(x score.Meter) int64 { return x.Tick })
	if err := s.Normalise(); err != nil {
		rule := "too_many_events"
		switch {
		case errors.Is(err, score.ErrTooManyVoices):
			rule = "too_many_voices"
		case errors.Is(err, score.ErrTooLong):
			rule = "too_long"
		}
		return nil, &SyntaxError{Line: 1, Col: 1, Rule: rule, Msg: err.Error()}
	}
	return s, nil
}

func lastPerTick[T any](xs []T, tick func(T) int64) []T {
	sort.SliceStable(xs, func(a, b int) bool { return tick(xs[a]) < tick(xs[b]) })
	out := xs[:0]
	for _, x := range xs {
		if n := len(out); n > 0 && tick(out[n-1]) == tick(x) {
			out[n-1] = x
			continue
		}
		out = append(out, x)
	}
	return out
}
