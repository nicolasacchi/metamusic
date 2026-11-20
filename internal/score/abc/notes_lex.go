package abc

import (
	"bytes"
)

var letterSemis = map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

type noteTok struct {
	pitch      int // -1 rest
	key        accKey
	acc        int
	inner      Rat
	start, end int
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// length reads an ABC length at i: n, /, //, /n, n/m. It returns the fraction and the index after it.
func (p *parser) length(i, to int) (Rat, int, error) {
	src := p.src
	num, den := int64(1), int64(1)
	j := i
	if j < to && isDigit(src[j]) {
		num = 0
		for j < to && isDigit(src[j]) {
			num = num*10 + int64(src[j]-'0')
			if num > 128 {
				return Rat{}, j, p.fail(i, "bad_length", "length numerator out of range")
			}
			j++
		}
	}
	if j < to && src[j] == '/' {
		if j+1 < to && isDigit(src[j+1]) {
			j++
			den = 0
			for j < to && isDigit(src[j]) {
				den = den*10 + int64(src[j]-'0')
				if den > 128 {
					return Rat{}, j, p.fail(i, "bad_length", "length denominator out of range")
				}
				j++
			}
		} else {
			for j < to && src[j] == '/' {
				den *= 2
				if den > 128 {
					return Rat{}, j, p.fail(i, "bad_length", "length denominator out of range")
				}
				j++
			}
		}
	}
	if num == 0 || den == 0 {
		return Rat{}, j, p.fail(i, "bad_length", "zero length")
	}
	return R(num, den), j, nil
}

// noteTok parses accidental, letter, octave marks and inner length at i (a note letter or accidental).
func (p *parser) noteTok(v *vstate, i, to int) (noteTok, int, error) {
	src := p.src
	start := i
	acc, hasAcc := 0, false
	for i < to && (src[i] == '^' || src[i] == '_' || src[i] == '=') {
		hasAcc = true
		switch src[i] {
		case '^':
			acc++
		case '_':
			acc--
		default:
			acc = 0
		}
		i++
		if i-start > 2 {
			return noteTok{}, i, p.fail(start, "bad_note", "too many accidentals")
		}
	}
	if i >= to || !((src[i] >= 'A' && src[i] <= 'G') || (src[i] >= 'a' && src[i] <= 'g')) {
		return noteTok{}, i, p.fail(start, "bad_note", "an accidental must be followed by a note letter")
	}
	lower := src[i] >= 'a'
	letter := src[i]
	if lower {
		letter -= 'a' - 'A'
	}
	i++
	oct := 0
	if lower {
		oct = 1
	}
	for i < to && (src[i] == '\'' || src[i] == ',') {
		if src[i] == '\'' {
			oct++
		} else {
			oct--
		}
		i++
		if oct > 8 || oct < -8 {
			return noteTok{}, i, p.fail(start, "bad_note", "octave out of range")
		}
	}
	key := p.d.key(letter, oct)
	a := 0
	switch {
	case hasAcc:
		a = acc
		if v.acc == nil {
			v.acc = map[accKey]int{}
		}
		v.acc[key] = acc
	default:
		if x, ok := v.tiedNow[key]; ok {
			a = x
		} else if x, ok := v.acc[key]; ok {
			a = x
		} else {
			a = v.st.key.Accidentals[letterIdx(letter)]
		}
	}
	pitch := 60 + letterSemis[letter] + 12*oct + a
	ln, j, err := p.length(i, to)
	if err != nil {
		return noteTok{}, j, err
	}
	if pitch < 0 || pitch > 127 {
		return noteTok{}, j, p.fail(start, "bad_note", "pitch out of range")
	}
	return noteTok{pitch: pitch, key: key, acc: a, inner: ln, start: start, end: j}, j, nil
}

func (p *parser) mul(a, b Rat) Rat {
	r, ok := mulc(a, b)
	if !ok {
		p.ovf = true
	}
	return r
}

func (p *parser) add(a, b Rat) Rat {
	r, ok := addc(a, b)
	if !ok {
		p.ovf = true
	}
	return r
}

// factor is the tuplet and broken-rhythm multiplier of the next element; it consumes the broken-rhythm part.
func (p *parser) factor(v *vstate) Rat {
	f := Rat{1, 1}
	if v.tupLeft > 0 {
		f = R(v.tupQ, v.tupP)
	}
	if v.broken.Num != 0 {
		f = p.mul(f, v.broken)
		v.broken = Rat{}
	}
	return f
}

func (p *parser) startElem(v *vstate) {
	v.tiedNow, v.tied = v.tied, nil
	if v.seq == 0 {
		p.seq++
		v.seq = p.seq
	}
	if p.first == nil {
		p.first = v
	}
}

// addElem appends one element (a note, a chord or a rest) to the open bar and advances the cursor by the first note.
func (p *parser) addElem(v *vstate, b *barB, ns []Note, e elem) {
	at := b.pos
	e.from = len(b.notes)
	for i := range ns {
		ns[i].At = at
	}
	b.notes = append(b.notes, ns...)
	e.to = len(b.notes)
	e.valid = true
	b.last = e
	b.pos = p.add(at, ns[0].Dur)
	b.hasNote = true
	if v.tupLeft > 0 {
		v.tupLeft--
	}
}

func (p *parser) music(from, to int) error {
	src := p.src
	v := p.cur
	i := from
	for i < to {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\\':
			i++
		case c == '%':
			return nil
		case c == '"':
			j := bytes.IndexByte(src[i+1:to], '"')
			if j < 0 {
				p.warn("unterminated_text", p.line)
				return nil
			}
			b := p.touch(v, i, i+j+2)
			text := string(src[i+1 : i+1+j])
			if ch, ok := parseChordSymbol(text, b.pos); ok {
				b.chords = append(b.chords, ch)
			} else if len(text) > 0 && text[0] >= 'A' && text[0] <= 'G' {
				p.warn("chord_unknown", p.line)
			}
			i += j + 2
		case c == '!' || c == '+' || c == '{':
			end := map[byte]byte{'!': '!', '+': '+', '{': '}'}[c]
			code := "decoration_ignored"
			if c == '{' {
				code = "grace_ignored"
			}
			p.warn(code, p.line)
			j := bytes.IndexByte(src[i+1:to], end)
			if j < 0 {
				p.touch(v, i, to)
				return nil
			}
			p.touch(v, i, i+j+2)
			i += j + 2
		case c == '(':
			if i+1 < to && src[i+1] >= '2' && src[i+1] <= '9' {
				j, err := p.tuplet(v, i, to)
				if err != nil {
					return err
				}
				p.touch(v, i, j)
				i = j
			} else {
				i++
			}
		case c == ')':
			i++
		case c == '-':
			p.tie(v)
			if v.cur != nil {
				v.cur.end = max(v.cur.end, i+1)
			}
			i++
		case c == '.' || c == '~' || (c >= 'H' && c <= 'W') || (c >= 'h' && c <= 'w'):
			p.warn("decoration_ignored", p.line)
			p.touch(v, i, i+1)
			i++
		case c == '[':
			j, nv, err := p.bracket(v, i, to)
			if err != nil {
				return err
			}
			v, i = nv, j
		case c == '|' || (c == ':' && i+1 < to && (src[i+1] == '|' || src[i+1] == ':')):
			i = p.barline(v, i, to)
		case c == ':' || c == ']':
			p.warn("stray_char", p.line)
			i++
		case c == '&':
			return p.fail(i, "unsupported_overlay", "voice overlay is not supported")
		case c == '>' || c == '<':
			i = p.broken(v, i, to)
		case c == 'y':
			i++
		case c == 'z' || c == 'x' || c == 'Z' || c == 'X':
			j, err := p.rest(v, i, to)
			if err != nil {
				return err
			}
			i = j
		case c == '^' || c == '_' || c == '=' || (c >= 'A' && c <= 'G') || (c >= 'a' && c <= 'g'):
			j, err := p.note(v, i, to)
			if err != nil {
				return err
			}
			i = j
		default:
			p.warn("unknown_char", p.line)
			p.touch(v, i, i+1)
			i++
		}
	}
	return nil
}

func (p *parser) note(v *vstate, i, to int) (int, error) {
	p.startElem(v)
	nt, j, err := p.noteTok(v, i, to)
	if err != nil {
		return j, err
	}
	b := p.touch(v, i, j)
	d := p.mul(p.mul(v.st.unit, nt.inner), p.factor(v))
	p.addElem(v, b, []Note{{Pitch: nt.pitch, Dur: d, Start: nt.start, End: nt.end}}, elem{keys: []accKey{nt.key}, accs: []int{nt.acc}})
	return j, nil
}

func (p *parser) rest(v *vstate, i, to int) (int, error) {
	src := p.src
	c := src[i]
	if c == 'Z' || c == 'X' {
		j := i + 1
		n := 1
		if j < to && isDigit(src[j]) {
			n = 0
			for j < to && isDigit(src[j]) {
				n = n*10 + int(src[j]-'0')
				if n > 10000 {
					return j, p.fail(i, "bad_length", "multi-bar rest too long")
				}
				j++
			}
			if n == 0 {
				return j, p.fail(i, "bad_length", "zero-bar rest")
			}
		}
		if v.seq == 0 {
			p.startElem(v)
		}
		p.closeBar(v, "")
		meter := R(int64(v.st.meterNum), int64(v.st.meterDen))
		for k := 0; k < n && !p.tooMany; k++ {
			b := p.touch(v, i, j)
			b.start, b.end = i, j
			b.notes = []Note{{Pitch: -1, Dur: meter, Start: i, End: j}}
			b.pos, b.hasNote = meter, true
			p.closeBar(v, "")
		}
		return j, nil
	}
	p.startElem(v)
	ln, j, err := p.length(i+1, to)
	if err != nil {
		return j, err
	}
	b := p.touch(v, i, j)
	d := p.mul(p.mul(v.st.unit, ln), p.factor(v))
	p.addElem(v, b, []Note{{Pitch: -1, Dur: d, Start: i, End: j}}, elem{})
	return j, nil
}

func (p *parser) tie(v *vstate) {
	b := v.cur
	if b == nil || !b.last.valid {
		return
	}
	e := b.last
	for k := e.from; k < e.to; k++ {
		b.notes[k].Tie = true
	}
	for k, key := range e.keys {
		if v.tied == nil {
			v.tied = map[accKey]int{}
		}
		v.tied[key] = e.accs[k]
	}
}

func (p *parser) tuplet(v *vstate, i, to int) (int, error) {
	src := p.src
	j := i + 1
	read := func() (int, bool) {
		n, ok := 0, false
		for j < to && isDigit(src[j]) {
			n, ok = n*10+int(src[j]-'0'), true
			if n > 99 {
				n = 99
			}
			j++
		}
		return n, ok
	}
	pp, _ := read()
	q, r := 0, pp
	if j < to && src[j] == ':' {
		j++
		if x, ok := read(); ok {
			q = x
		}
		if j < to && src[j] == ':' {
			j++
			if x, ok := read(); ok {
				r = x
			}
		}
	}
	if q == 0 {
		switch pp {
		case 2:
			q = 3
		case 3, 6:
			q = 2
		case 4, 8:
			q = 3
		default:
			if v.st.meterNum%3 == 0 && (v.st.meterNum > 3 || v.st.meterDen == 8) {
				q = 3
			} else {
				q = 2
			}
		}
	}
	if pp < 2 || r < 1 {
		return j, p.fail(i, "bad_note", "bad tuplet")
	}
	v.tupP, v.tupQ, v.tupLeft = int64(pp), int64(q), r
	return j, nil
}

func (p *parser) broken(v *vstate, i, to int) int {
	src := p.src
	c := src[i]
	n := 0
	j := i
	for j < to && src[j] == c && n < 3 {
		n++
		j++
	}
	small := R(1, int64(1)<<n)
	big := R(2*(int64(1)<<n)-1, int64(1)<<n)
	prev, next := big, small
	if c == '<' {
		prev, next = small, big
	}
	b := v.cur
	if b != nil && b.last.valid {
		e := b.last
		for k := e.from; k < e.to; k++ {
			b.notes[k].Dur = p.mul(b.notes[k].Dur, prev)
		}
		b.pos = p.add(b.notes[e.from].At, b.notes[e.from].Dur)
		b.last.valid = false
		b.end = max(b.end, j)
	} else {
		p.warn("broken_rhythm_unpaired", p.line)
	}
	v.broken = next
	return j
}

// barline reads a bar line, a repeat sign or a volta mark starting at i.
func (p *parser) barline(v *vstate, i, to int) int {
	src := p.src
	s := i
	for i < to {
		c := src[i]
		if c == '|' || c == ':' || c == ']' {
			i++
		} else if c == '[' && i+1 < to && src[i+1] == '|' {
			i += 2
		} else {
			break
		}
	}
	tok := string(src[s:i])
	hasEnd := tok[0] == ':'
	hasStart := tok[len(tok)-1] == ':'
	p.closeBar(v, tok)
	if hasEnd {
		if n := len(v.bars); n > 0 {
			v.bars[n-1].RepeatEnd = true
			if v.bars[n-1].Right == "" {
				v.bars[n-1].Right = tok
			}
		}
	}
	if hasEnd || hasStart {
		p.feature("repeat", "")
	}
	if tok != "|" {
		v.volta = 0
	}
	v.left, v.repStart = tok, hasStart
	// volta mark: |1 or |[1
	k := i
	if k < to && src[k] == '[' && k+1 < to && isDigit(src[k+1]) {
		k++
	}
	if k < to && isDigit(src[k]) {
		n := int(src[k] - '0')
		for k < to && (isDigit(src[k]) || src[k] == ',' || src[k] == '-') {
			k++
		}
		v.volta = n
		p.feature("volta", "")
		i = k
	}
	return i
}

// bracket handles everything that starts with '[': bar lines, volta marks, inline fields and chords. It returns the new
// index and the (possibly switched) voice.
func (p *parser) bracket(v *vstate, i, to int) (int, *vstate, error) {
	src := p.src
	switch {
	case i+1 < to && src[i+1] == '|':
		return p.barline(v, i, to), v, nil
	case i+1 < to && isDigit(src[i+1]):
		p.closeBar(v, "")
		v.volta = int(src[i+1] - '0')
		p.feature("volta", "")
		j := i + 2
		for j < to && (isDigit(src[j]) || src[j] == ',' || src[j] == '-') {
			j++
		}
		return j, v, nil
	case i+2 < to && src[i+2] == ':' && isLetter(src[i+1]):
		j := bytes.IndexByte(src[i:to], ']')
		if j < 0 {
			return to, v, p.fail(i, "unterminated_chord", "inline field is not closed")
		}
		j += i
		f := src[i+1]
		val := string(bytes.TrimSpace(src[i+3 : j]))
		switch f {
		case 'V':
			p.feature("inline_field", "V")
			p.switchVoice(val)
			v = p.cur
			p.startElem(v)
			v.tiedNow = nil
			return j + 1, v, nil
		case 'K', 'M', 'L', 'Q':
			if err := p.applyField(f, val); err != nil {
				return j + 1, v, err
			}
			p.touch(v, i, j+1)
		default:
			p.feature("inline_field", string(f))
			p.touch(v, i, j+1)
		}
		return j + 1, v, nil
	}
	j, err := p.chord(v, i, to)
	return j, v, err
}

func (p *parser) chord(v *vstate, i, to int) (int, error) {
	src := p.src
	p.startElem(v)
	j := i + 1
	var toks []noteTok
	var ties []bool
	for {
		for j < to && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j >= to {
			return j, p.fail(i, "unterminated_chord", "chord is not closed")
		}
		c := src[j]
		if c == ']' {
			j++
			break
		}
		if c == '-' {
			if len(ties) > 0 {
				ties[len(ties)-1] = true
			}
			j++
			continue
		}
		if c == '^' || c == '_' || c == '=' || (c >= 'A' && c <= 'G') || (c >= 'a' && c <= 'g') {
			nt, k, err := p.noteTok(v, j, to)
			if err != nil {
				return k, err
			}
			toks = append(toks, nt)
			ties = append(ties, false)
			j = k
			continue
		}
		if c == 'z' || c == 'x' {
			_, k, err := p.length(j+1, to)
			if err != nil {
				return k, err
			}
			j = k
			continue
		}
		if c == '"' || c == '!' || c == '.' || c == '~' {
			p.warn("decoration_ignored", p.line)
			j++
			continue
		}
		if c == '|' {
			return j, p.fail(i, "unterminated_chord", "chord is not closed")
		}
		return j, p.fail(j, "bad_note", "unexpected character in chord")
	}
	outer, k, err := p.length(j, to)
	if err != nil {
		return k, err
	}
	b := p.touch(v, i, k)
	if len(toks) == 0 {
		// an empty chord or only rests: a rest of the outer length
		d := p.mul(p.mul(v.st.unit, outer), p.factor(v))
		p.addElem(v, b, []Note{{Pitch: -1, Dur: d, Start: i, End: k}}, elem{})
		return k, nil
	}
	f := p.factor(v)
	ns := make([]Note, len(toks))
	e := elem{}
	for n, t := range toks {
		d := p.mul(p.mul(p.mul(v.st.unit, t.inner), outer), f)
		ns[n] = Note{Pitch: t.pitch, Dur: d, Tie: ties[n], Start: t.start, End: t.end}
		e.keys = append(e.keys, t.key)
		e.accs = append(e.accs, t.acc)
		if ties[n] {
			if v.tied == nil {
				v.tied = map[accKey]int{}
			}
			v.tied[t.key] = t.acc
		}
	}
	p.addElem(v, b, ns, e)
	return k, nil
}
