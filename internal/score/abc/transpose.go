package abc

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

var (
	sharpNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	flatNames  = [12]string{"C", "Db", "D", "Eb", "E", "F", "Gb", "G", "Ab", "A", "Bb", "B"}
)

// noteTokInfo is a note token read back from the source: accidental written (if any), letter, octave (0 = the octave of
// middle C, lower-case letters 1) and where the length suffix starts.
type noteTokInfo struct {
	hasAcc  bool
	acc     int
	letter  byte
	oct     int
	prefEnd int
}

// scanNote reads the accidental, letter and octave marks of the note token src[s:e].
func scanNote(src []byte, s, e int) (nt noteTokInfo, ok bool) {
	i := s
	for i < e && (src[i] == '^' || src[i] == '_' || src[i] == '=') {
		nt.hasAcc = true
		switch src[i] {
		case '^':
			nt.acc++
		case '_':
			nt.acc--
		default:
			nt.acc = 0
		}
		i++
	}
	if i >= e || !((src[i] >= 'A' && src[i] <= 'G') || (src[i] >= 'a' && src[i] <= 'g')) {
		return nt, false
	}
	nt.letter = src[i]
	if nt.letter >= 'a' {
		nt.letter -= 'a' - 'A'
		nt.oct = 1
	}
	i++
	for i < e && (src[i] == '\'' || src[i] == ',') {
		if src[i] == '\'' {
			nt.oct++
		} else {
			nt.oct--
		}
		i++
	}
	nt.prefEnd = i
	return nt, true
}

// spell names a pitch: the letter and the accidental (-1, 0, 1), sharps when sharp is set, else flats.
func spell(pitch int, sharp bool) (letter byte, acc int) {
	pc := ((pitch % 12) + 12) % 12
	name := flatNames[pc]
	if sharp {
		name = sharpNames[pc]
	}
	letter = name[0]
	if len(name) == 2 {
		if name[1] == '#' {
			acc = 1
		} else {
			acc = -1
		}
	}
	return letter, acc
}

func accText(a int) string {
	switch {
	case a == 0:
		return "="
	case a > 0:
		return strings.Repeat("^", a)
	}
	return strings.Repeat("_", -a)
}

// notePrefix writes a letter with octave marks for the octave oct (0 = C..B around middle C, 1 = c..b).
func notePrefix(letter byte, oct int) string {
	if oct >= 1 {
		return string(letter+('a'-'A')) + strings.Repeat("'", oct-1)
	}
	return string(letter) + strings.Repeat(",", -oct)
}

type transposer struct {
	t     *Tune
	semis int
	sharp bool
	dia   Dialect
}

// endsTied reports whether the last element of a bar is tied into the next bar.
func endsTied(b Bar) bool {
	if len(b.Notes) == 0 {
		return false
	}
	last := b.Notes[len(b.Notes)-1].At
	for i := len(b.Notes) - 1; i >= 0 && b.Notes[i].At.Cmp(last) == 0; i-- {
		if b.Notes[i].Tie {
			return true
		}
	}
	return false
}

// bar re-spells every note of one bar and transposes its chord symbols. A note carries an accidental only when the
// reading state of the bar (key signature plus the accidentals written so far, by the tune's dialect) differs from the
// wanted one, or when the note follows a tie, whose accidental the parser would otherwise inherit.
func (tr transposer) bar(oi int, v Voice, bi int) ([]splice, error) {
	b := v.Bars[bi]
	src := tr.t.Src
	var out []splice
	mem := map[accKey]int{}
	force := bi > 0 && endsTied(v.Bars[bi-1])
	for g := 0; g < len(b.Notes); {
		h := g
		tied := false
		for h < len(b.Notes) && b.Notes[h].At.Cmp(b.Notes[g].At) == 0 {
			n := b.Notes[h]
			h++
			tied = tied || n.Tie
			if n.Pitch < 0 {
				continue
			}
			tok, ok := scanNote(src, n.Start, n.End)
			if !ok {
				return nil, editErr(oi, "internal", "unreadable note token at byte %d", n.Start)
			}
			np := n.Pitch + tr.semis
			if np < 0 || np > 127 {
				return nil, editErr(oi, "pitch_range", "a note would leave the range 0..127")
			}
			letter, acc := spell(np, tr.sharp)
			oct := (np - acc - letterSemis[letter] - 60) / 12
			if oct < -8 || oct > 8 {
				return nil, editErr(oi, "pitch_range", "a note would leave the octave range")
			}
			key := tr.dia.key(letter, oct)
			state, seen := mem[key]
			if !seen {
				state = tr.t.Header.Key.Accidentals[letterIdx(letter)]
			}
			text := notePrefix(letter, oct)
			if force || acc != state {
				text = accText(acc) + text
				mem[key] = acc
			}
			out = append(out, splice{n.Start, tok.prefEnd, []byte(text)})
		}
		force = tied
		g = h
	}
	return append(out, tr.chordSplices(b)...), nil
}

// chordSplices rewrites the chord symbols found in the bar's source span.
func (tr transposer) chordSplices(b Bar) []splice {
	src := tr.t.Src
	var out []splice
	for i := b.Start; i < b.End; {
		switch src[i] {
		case '%':
			for i < b.End && src[i] != '\n' {
				i++
			}
		case '!':
			j := bytes.IndexByte(src[i+1:b.End], '!')
			if j < 0 {
				i++
			} else {
				i += j + 2
			}
		case '"':
			j := bytes.IndexByte(src[i+1:b.End], '"')
			if j < 0 {
				i = b.End
				break
			}
			if nt, ok := tr.chordText(string(src[i+1 : i+1+j])); ok {
				out = append(out, splice{i + 1, i + 1 + j, []byte(nt)})
			}
			i += j + 2
		default:
			i++
		}
	}
	return out
}

// rootAt reads a note name with an optional #/b at the start of s and returns its pitch class and length.
func rootAt(s string) (pc, n int, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	pc, ok = noteClass[s[0]]
	if !ok {
		return 0, 0, false
	}
	n = 1
	if n < len(s) && s[n] == '#' {
		pc, n = pc+1, n+1
	} else if n < len(s) && s[n] == 'b' {
		pc, n = pc-1, n+1
	}
	return ((pc % 12) + 12) % 12, n, true
}

func (tr transposer) name(pc int) string {
	pc = ((pc+tr.semis)%12 + 12) % 12
	if tr.sharp {
		return sharpNames[pc]
	}
	return flatNames[pc]
}

// chordText transposes the text of one quoted string if it is a chord symbol (whitespace around it is kept).
func (tr transposer) chordText(text string) (string, bool) {
	if _, ok := parseChordSymbol(text, Rat{0, 1}); !ok {
		return "", false
	}
	core := strings.TrimSpace(text)
	pre := text[:strings.Index(text, core)]
	post := text[len(pre)+len(core):]
	pc, n, ok := rootAt(core)
	if !ok {
		return "", false
	}
	rest := core[n:]
	qual, bass := rest, ""
	slash := strings.IndexByte(rest, '/')
	if slash >= 0 {
		qual, bass = rest[:slash], rest[slash+1:]
	}
	res := tr.name(pc) + qual
	if slash >= 0 {
		if bpc, bn, ok := rootAt(bass); ok {
			bass = tr.name(bpc) + bass[bn:]
		}
		res += "/" + bass
	}
	return pre + res + post, true
}

// transposeSplices builds the splices of a transpose_bars op and a verifier that the re-parsed result holds exactly the
// transposed pitches and chord roots in the target bars.
func transposeSplices(t *Tune, op Op, oi int) ([]splice, func(*Tune) error, error) {
	if op.Semitones == 0 || op.Semitones < -MaxSemitones || op.Semitones > MaxSemitones {
		return nil, nil, editErr(oi, "bad_semitones", "semitones must be -%d..%d and not 0", MaxSemitones, MaxSemitones)
	}
	var vis []int
	if op.Voice == "" {
		for i := range t.Voices {
			vis = append(vis, i)
		}
	} else if i := findVoice(t, op.Voice); i >= 0 {
		vis = []int{i}
	} else {
		return nil, nil, editErr(oi, "bad_voice", "no voice %q", op.Voice)
	}
	if inlineKeyRe.Match(t.Src) {
		return nil, nil, editErr(oi, "unsupported_feature", "the tune changes key inside the body, which transpose_bars does not support")
	}
	tr := transposer{t: t, semis: op.Semitones, sharp: t.Header.Key.Sharps > 0, dia: t.opts.dialect()}
	var sp []splice
	type want struct {
		vi, bi int
		pitch  []int
		roots  []int
	}
	var wants []want
	for _, vi := range vis {
		v := t.Voices[vi]
		if op.BarFrom < 0 || op.BarTo < op.BarFrom || op.BarTo >= len(v.Bars) {
			return nil, nil, editErr(oi, "bad_range", "bars %d..%d are outside voice %q with %d bars", op.BarFrom, op.BarTo, v.ID, len(v.Bars))
		}
		if sharedSpan(v.Bars, op.BarFrom, op.BarTo) {
			return nil, nil, editErr(oi, "shared_span", "a bar in the range shares its source with another (multi-bar rest)")
		}
		for bi := op.BarFrom; bi <= op.BarTo; bi++ {
			s, err := tr.bar(oi, v, bi)
			if err != nil {
				return nil, nil, err
			}
			sp = append(sp, s...)
			w := want{vi: vi, bi: bi}
			for _, n := range v.Bars[bi].Notes {
				if n.Pitch < 0 {
					w.pitch = append(w.pitch, -1)
				} else {
					w.pitch = append(w.pitch, n.Pitch+op.Semitones)
				}
			}
			for _, c := range v.Bars[bi].Chords {
				w.roots = append(w.roots, ((c.Root+op.Semitones)%12+12)%12)
			}
			wants = append(wants, w)
		}
	}
	verify := func(nt *Tune) error {
		for _, w := range wants {
			if w.vi >= len(nt.Voices) || w.bi >= len(nt.Voices[w.vi].Bars) {
				return fmt.Errorf("transposed bar %d of voice %d is missing", w.bi, w.vi)
			}
			b := nt.Voices[w.vi].Bars[w.bi]
			if len(b.Notes) != len(w.pitch) || len(b.Chords) != len(w.roots) {
				return fmt.Errorf("transposed bar %d of voice %d changed its notes or chords", w.bi, w.vi)
			}
			for i, n := range b.Notes {
				if n.Pitch != w.pitch[i] {
					return fmt.Errorf("transposed bar %d of voice %d: note %d is %d, want %d", w.bi, w.vi, i, n.Pitch, w.pitch[i])
				}
			}
			for i, c := range b.Chords {
				if c.Root != w.roots[i] {
					return fmt.Errorf("transposed bar %d of voice %d: chord %d root %d, want %d", w.bi, w.vi, i, c.Root, w.roots[i])
				}
			}
		}
		return nil
	}
	return sp, verify, nil
}

// inlineKeyRe finds a key change in the body: an inline [K:..] field or a K: line that is not the header's.
var inlineKeyRe = regexp.MustCompile(`\[K:|(?s:\nK:.*\nK:)`)
