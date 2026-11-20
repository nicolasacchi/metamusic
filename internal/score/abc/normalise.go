package abc

import "slices"

// normSplices lists the accidentals a yue2 tune needs for its standard reading to keep the pitches the yue2 reading
// gives (YuE2 carries an accidental to every octave of a pitch class; ABC 2.1 only to the same octave). It also counts the
// bars (of every voice) that gain one.
func normSplices(t *Tune) (ins []splice, divergent int) {
	for _, v := range t.Voices {
		var tied map[accKey]int
		for _, b := range v.Bars {
			mem := map[accKey]int{}
			div := false
			for g := 0; g < len(b.Notes); {
				h := g
				tiedNow := tied
				tied = nil
				for h < len(b.Notes) && b.Notes[h].At.Cmp(b.Notes[g].At) == 0 {
					n := b.Notes[h]
					h++
					if n.Pitch < 0 {
						continue
					}
					tok, ok := scanNote(t.Src, n.Start, n.End)
					if !ok {
						continue
					}
					want := n.Pitch - 60 - letterSemis[tok.letter] - 12*tok.oct
					key := DialectStandard.key(tok.letter, tok.oct)
					state := 0
					switch x, inTied := tiedNow[key]; {
					case tok.hasAcc:
						state = tok.acc
						mem[key] = tok.acc
					case inTied:
						state = x
					default:
						if y, seen := mem[key]; seen {
							state = y
						} else {
							state = t.Header.Key.Accidentals[letterIdx(tok.letter)]
						}
					}
					if !tok.hasAcc && state != want {
						ins = append(ins, splice{n.Start, n.Start, []byte(accText(want))})
						mem[key] = want
						div = true
					}
					if n.Tie {
						if tied == nil {
							tied = map[accKey]int{}
						}
						tied[key] = want
					}
				}
				g = h
			}
			if div {
				divergent++
			}
		}
	}
	return ins, divergent
}

// Normalise returns the display text of a yue2 tune: the source with explicit accidentals added so that its standard
// ABC 2.1 reading has the same pitches as the yue2 reading (D-DESK-15). changed is false, and text is the source, when no
// bar diverges, for a tune of the standard dialect, and (defensively) when the rewrite would not read back to the same
// pitches. A nil tune returns nil.
func Normalise(t *Tune) (text []byte, changed bool) {
	if t == nil {
		return nil, false
	}
	same := slices.Clone(t.Src)
	if t.opts.dialect() != DialectYuE2 {
		return same, false
	}
	ins, _ := normSplices(t)
	if len(ins) == 0 {
		return same, false
	}
	out, err := applySplices(t.Src, ins)
	if err != nil {
		return same, false
	}
	std, err := Parse(out, Options{Dialect: DialectStandard, DefaultBPM: t.opts.DefaultBPM})
	if err != nil || !samePitches(t, std) {
		return same, false
	}
	return out, true
}

func samePitches(a, b *Tune) bool {
	if len(a.Voices) != len(b.Voices) {
		return false
	}
	for i := range a.Voices {
		x, y := a.Notes(i), b.Notes(i)
		if len(x) != len(y) {
			return false
		}
		for k := range x {
			if x[k].MIDI != y[k].MIDI || x[k].Start.Cmp(y[k].Start) != 0 || x[k].Dur.Cmp(y[k].Dur) != 0 {
				return false
			}
		}
	}
	return true
}
