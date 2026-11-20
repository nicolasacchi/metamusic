package abc

import (
	"bytes"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// state is what a field line or an inline field changes; one copy is the header default, each voice has its own.
type state struct {
	key                Key
	unit               Rat
	unitSet            bool
	meterNum, meterDen int
}

type elem struct {
	from, to int // indexes into barB.notes
	keys     []accKey
	accs     []int
	valid    bool
}

type barB struct {
	start, end int // byte span; start < 0 until content begins
	line       int
	notes      []Note
	chords     []Chord
	pos        Rat
	hasNote    bool
	left       string
	repStart   bool
	volta      int
	changes    []Change
	last       elem
}

type vstate struct {
	id       string
	name     string
	st       state
	bars     []Bar
	cur      *barB
	acc      map[accKey]int
	tied     map[accKey]int
	tiedNow  map[accKey]int
	left     string
	repStart bool
	volta    int
	carry    []Change
	tupQ     int64 // tuplet: q/p multiplier and elements left
	tupP     int64
	tupLeft  int
	broken   Rat // multiplier pending for the next element (0 = none)
	seq      int // order of first music; 0 = none yet
}

type decl struct {
	name    string
	program *int
	channel *int
}

type label struct {
	kind, raw, voice string
	bar              int
}

type parser struct {
	src   []byte
	o     Options
	d     Dialect
	t     *Tune
	line  int
	glob  state
	decls map[string]*decl
	vs    []*vstate
	byID  map[string]*vstate
	cur   *vstate
	curID string // the voice named by the last V: line ("" before any)

	inBody    bool
	sawKey    bool
	ovf       bool
	warned    map[string]bool
	seq       int
	first     *vstate
	pending   []label
	labels    []label
	interleav bool
	hadX      bool
	lineStart int
	nbars     int
	tooMany   bool
}

var fieldRe = regexp.MustCompile(`^([A-Za-z]):\s*(.*)$`)

func newParser(src []byte, o Options) *parser {
	t := &Tune{Src: append([]byte(nil), src...), opts: o}
	t.Header.Fields = map[string]string{}
	p := &parser{src: t.Src, o: o, d: o.dialect(), t: t, decls: map[string]*decl{}, byID: map[string]*vstate{}, warned: map[string]bool{}}
	p.glob = state{meterNum: 4, meterDen: 4}
	p.glob.key, _, _ = parseKey("")
	return p
}

func (p *parser) warn(code string, line int) {
	if p.warned[code] {
		return
	}
	p.warned[code] = true
	p.t.Warnings = append(p.t.Warnings, Warning{Code: code, Line: line})
}

func (p *parser) fail(col int, rule, msg string) error {
	return &SyntaxError{Line: p.line, Col: col - p.lineStart + 1, Rule: rule, Msg: msg}
}

func (p *parser) feature(code, detail string) {
	p.t.Features = append(p.t.Features, Feature{Code: code, Line: p.line, Detail: detail})
}

func (p *parser) declOf(id string) *decl {
	d := p.decls[id]
	if d == nil {
		d = &decl{}
		p.decls[id] = d
	}
	return d
}

func (p *parser) run() error {
	src := p.src
	pos := 0
	p.line = 0
	for pos <= len(src) {
		end := bytes.IndexByte(src[pos:], '\n')
		next := len(src) + 1
		if end < 0 {
			end = len(src)
		} else {
			end += pos
			next = end + 1
		}
		p.line++
		p.lineStart = pos
		if err := p.doLine(pos, end, next); err != nil {
			return err
		}
		pos = next
	}
	return p.finish()
}

// doLine handles one line [from, to) (to excludes the newline); next is the offset of the following line.
func (p *parser) doLine(from, to, next int) error {
	src := p.src
	for to > from && (src[to-1] == '\r' || src[to-1] == ' ' || src[to-1] == '\t') {
		to--
	}
	if from >= to {
		return nil
	}
	line := src[from:to]
	if line[0] == '%' {
		if len(line) > 1 && line[1] == '%' {
			return p.directive(string(line[2:]))
		}
		text := strings.TrimSpace(string(line[1:]))
		if p.inBody {
			if k, ok := sectionKind(text); ok {
				p.pending = append(p.pending, label{kind: k, raw: text})
			}
		}
		return nil
	}
	if line[0] == ' ' || line[0] == '\t' {
		t := bytes.TrimLeft(line, " \t")
		if len(t) == 0 {
			return nil
		}
		if t[0] == '%' {
			return nil
		}
	}
	if line[0] == '+' && len(line) > 1 && line[1] == ':' {
		return p.fail(p.lineStart, "unsupported_continuation", "+: continuation lines are not supported")
	}
	if m := fieldRe.FindSubmatch(line); m != nil && !(len(m[2]) > 0 && (m[2][0] == '|' || m[2][0] == ':')) {
		f := m[1][0]
		val := string(m[2])
		if f != 'w' && f != 'W' {
			if i := strings.IndexByte(val, '%'); i >= 0 {
				val = val[:i]
			}
		}
		val = strings.TrimSpace(val)
		return p.fieldLine(f, val, next, from)
	}
	if !p.inBody {
		p.endHeader(next)
		p.warn("no_key", p.line)
	}
	p.flushPending()
	v := p.ensureVoice()
	if v.seq == 0 {
		p.seq++
		v.seq = p.seq
	}
	if p.first == nil {
		p.first = v
	}
	return p.music(from, to)
}

func (p *parser) endHeader(next int) {
	p.inBody = true
	p.t.Header.BodyStart = min(next, len(p.src))
	g := &p.glob
	if !g.unitSet {
		if float64(g.meterNum)/float64(g.meterDen) < 0.75 {
			g.unit = Rat{1, 16}
		} else {
			g.unit = Rat{1, 8}
		}
	}
}

func (p *parser) directive(s string) error {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	if !strings.EqualFold(f[0], "MIDI") {
		p.warn("directive_ignored", p.line)
		return nil
	}
	return p.midi(f[1:])
}

func (p *parser) midi(f []string) error {
	if len(f) < 2 {
		return nil
	}
	nums, ok := atois(f[1:])
	if !ok {
		p.warn("directive_ignored", p.line)
		return nil
	}
	d := p.declOf(p.curID)
	switch strings.ToLower(f[0]) {
	case "program":
		n := nums[len(nums)-1]
		if n < 0 || n > 127 {
			p.warn("program_range", p.line)
			return nil
		}
		d.program = &n
	case "channel":
		n := nums[0]
		if n < 1 || n > 16 || n == 10 {
			p.warn("channel_ignored", p.line)
			return nil
		}
		d.channel = &n
	default:
		p.warn("directive_ignored", p.line)
	}
	return nil
}

func atois(f []string) ([]int, bool) {
	out := make([]int, 0, len(f))
	for _, x := range f {
		n, err := strconv.Atoi(x)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func (p *parser) fieldLine(f byte, val string, next, from int) error {
	switch f {
	case 'U', 'm':
		return p.fail(p.lineStart, "unsupported_macro", "macros are not supported")
	case 'X':
		if p.hadX {
			return p.fail(p.lineStart, "unsupported_multi_tune", "a second X: starts another tune")
		}
		p.hadX = true
		p.t.Header.Fields["X"] = val
		return nil
	case 'w', 'W':
		p.warn("lyrics_ignored", p.line)
		return nil
	case 'I':
		fs := strings.Fields(val)
		if len(fs) > 0 && strings.EqualFold(fs[0], "MIDI") {
			return p.midi(fs[1:])
		}
		p.warn("directive_ignored", p.line)
		return nil
	}
	if !p.inBody {
		return p.headerField(f, val, next)
	}
	p.bodyField(f, val, from)
	return p.afterBodyField(f, val)
}

func (p *parser) headerField(f byte, val string, next int) error {
	h := &p.t.Header
	if _, ok := h.Fields[string(f)]; !ok {
		h.Fields[string(f)] = val
	}
	switch f {
	case 'M':
		n, d, ok := parseMeter(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable M: field")
		}
		p.glob.meterNum, p.glob.meterDen = n, d
	case 'L':
		u, ok := parseUnit(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable L: field")
		}
		p.glob.unit, p.glob.unitSet = u, true
	case 'Q':
		if tp, ok := parseTempo(val); ok {
			h.Tempo = &tp
		} else {
			h.Tempo = nil
			p.warn("tempo_unsupported", p.line)
		}
	case 'K':
		k, ok, ws := parseKey(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable K: field")
		}
		for _, w := range ws {
			p.warn(w, p.line)
		}
		p.glob.key = k
		h.Key = k
		p.sawKey = true
		p.endHeader(next)
		h.Meter = R(int64(p.glob.meterNum), int64(p.glob.meterDen))
		h.MeterNum, h.MeterDen = p.glob.meterNum, p.glob.meterDen
		h.Unit = p.glob.unit
	case 'V':
		id, name := splitVoice(val)
		d := p.declOf(id)
		if name != "" {
			d.name = name
		}
		p.curID = id
	}
	return nil
}

// bodyField applies a field line or an inline field found in the body to the current voice (or the defaults when no
// voice exists yet).
func (p *parser) bodyField(f byte, val string, from int) {
	if f == 'V' {
		p.switchVoice(val)
		p.flushPending()
	}
}

func (p *parser) afterBodyField(f byte, val string) error {
	switch f {
	case 'K', 'M', 'L', 'Q':
		return p.applyField(f, val)
	case 'V':
		return nil
	}
	return nil
}

// applyField applies K, M, L or Q to the current voice (or the default state) and records its feature and change.
func (p *parser) applyField(f byte, val string) error {
	st := &p.glob
	if p.cur != nil {
		st = &p.cur.st
	}
	p.feature("inline_field", string(f))
	switch f {
	case 'K':
		k, ok, ws := parseKey(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable K: field")
		}
		for _, w := range ws {
			p.warn(w, p.line)
		}
		st.key = k
	case 'L':
		u, ok := parseUnit(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable L: field")
		}
		st.unit, st.unitSet = u, true
	case 'M':
		n, d, ok := parseMeter(val)
		if !ok {
			return p.fail(p.lineStart, "bad_header", "unreadable M: field")
		}
		st.meterNum, st.meterDen = n, d
		p.addChange(Change{MeterNum: n, MeterDen: d})
	case 'Q':
		tp, ok := parseTempo(val)
		if !ok {
			p.warn("tempo_unsupported", p.line)
			return nil
		}
		p.addChange(Change{Tempo: &tp})
	}
	return nil
}

func (p *parser) addChange(c Change) {
	v := p.ensureVoice()
	if v.cur != nil {
		c.At = v.cur.pos
		v.cur.changes = append(v.cur.changes, c)
		return
	}
	v.carry = append(v.carry, c)
}

func splitVoice(val string) (id, name string) {
	f := strings.Fields(val)
	if len(f) == 0 {
		return "", ""
	}
	id = f[0]
	for _, key := range []string{"name=", "nm="} {
		if i := strings.Index(val, key); i >= 0 {
			rest := val[i+len(key):]
			if strings.HasPrefix(rest, `"`) {
				if j := strings.IndexByte(rest[1:], '"'); j >= 0 {
					return id, rest[1 : 1+j]
				}
			} else if j := strings.IndexAny(rest, " \t"); j >= 0 {
				return id, rest[:j]
			} else {
				return id, rest
			}
		}
	}
	return id, ""
}

func (p *parser) switchVoice(val string) {
	id, name := splitVoice(val)
	if id == "" {
		return
	}
	if p.cur != nil && p.cur.id != id {
		p.closeBar(p.cur, "")
	}
	if name != "" {
		p.declOf(id).name = name
	}
	if v := p.byID[id]; v != nil && v.seq > 0 && (p.cur == nil || p.cur.id != id) {
		p.interleav = true
	}
	p.curID = id
	p.cur = nil
	p.ensureVoice()
}

// ensureVoice returns the current voice, creating the implicit voice "1" or the one named by the last V: line.
func (p *parser) ensureVoice() *vstate {
	if p.cur != nil {
		return p.cur
	}
	id := p.curID
	if id == "" {
		id = "1"
	}
	v := p.byID[id]
	if v == nil {
		v = &vstate{id: id, st: p.glob}
		if d := p.decls[id]; d != nil {
			v.name = d.name
		}
		p.byID[id] = v
		p.vs = append(p.vs, v)
	}
	p.cur = v
	return v
}

func (p *parser) flushPending() {
	bar := 0
	if p.first != nil {
		bar = len(p.first.bars)
	}
	for _, l := range p.pending {
		l.voice, l.bar = p.ensureVoice().id, bar
		p.labels = append(p.labels, l)
	}
	p.pending = p.pending[:0]
}

// closeBar ends the open bar of v, emitting it when it holds a note or rest.
func (p *parser) closeBar(v *vstate, right string) {
	b := v.cur
	v.cur = nil
	v.acc = nil
	if b == nil {
		return
	}
	if !b.hasNote {
		v.carry = append(v.carry, b.changes...)
		return
	}
	if p.nbars++; p.nbars > maxExpandedBars {
		p.tooMany = true
		return
	}
	v.bars = append(v.bars, Bar{Index: len(v.bars), Line: b.line, Start: b.start, End: b.end, Notes: b.notes, Chords: b.chords,
		Length: b.pos, Left: b.left, Right: right, RepeatStart: b.repStart, Volta: b.volta, Changes: b.changes})
}

// touch makes sure a bar is open and extends its content span over [s, e).
func (p *parser) touch(v *vstate, s, e int) *barB {
	b := v.cur
	if b == nil {
		b = &barB{start: -1, line: p.line, left: v.left, repStart: v.repStart, volta: v.volta}
		b.changes = v.carry
		v.carry = nil
		v.left, v.repStart = "", false
		v.cur = b
	}
	if b.start < 0 {
		b.start, b.line = s, p.line
	}
	b.end = e
	return b
}

func (p *parser) finish() error {
	// section flush and tune end
	p.flushPending()
	for _, v := range p.vs {
		p.closeBar(v, "")
		if v.tupLeft > 0 {
			p.line = max(1, p.line-1)
			return p.fail(p.lineStart, "unterminated_tuplet", "a tuplet is not complete at the end of the tune")
		}
	}
	if p.tooMany {
		return &SyntaxError{Line: 1, Col: 1, Rule: "too_many_events", Msg: "too many bars"}
	}
	if p.ovf {
		return p.fail(p.lineStart, "bad_length", "durations are too complex to represent")
	}
	// order voices by first music; drop voices with no bars
	vs := make([]*vstate, 0, len(p.vs))
	for _, v := range p.vs {
		if len(v.bars) > 0 {
			vs = append(vs, v)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].seq < vs[j].seq })
	if len(vs) == 0 {
		return &SyntaxError{Line: 1, Col: 1, Rule: "empty_score", Msg: "no notes"}
	}
	if len(vs) > 16 {
		return &SyntaxError{Line: 1, Col: 1, Rule: "too_many_voices", Msg: "more than 16 voices"}
	}
	t := p.t
	if !p.sawKey {
		g := &p.glob
		if !g.unitSet {
			if float64(g.meterNum)/float64(g.meterDen) < 0.75 {
				g.unit = Rat{1, 16}
			} else {
				g.unit = Rat{1, 8}
			}
		}
		t.Header.Key = g.key
		t.Header.Meter = R(int64(g.meterNum), int64(g.meterDen))
		t.Header.MeterNum, t.Header.MeterDen = g.meterNum, g.meterDen
		t.Header.Unit = g.unit
	}
	glob := p.decls[""]
	for _, v := range vs {
		tv := Voice{ID: v.id, Name: v.name, Bars: v.bars}
		d := p.decls[v.id]
		if d != nil {
			tv.Program, tv.Channel = d.program, d.channel
		}
		if tv.Program == nil && glob != nil {
			tv.Program = glob.program
		}
		if tv.Channel == nil && glob != nil {
			tv.Channel = glob.channel
		}
		t.Voices = append(t.Voices, tv)
	}
	p.sections(vs)
	return nil
}

// sections ports yue2.ParseScore: labels of the first voice (or every label when voices interleave), numbered.
func (p *parser) sections(vs []*vstate) {
	t := p.t
	first := vs[0].id
	total := len(vs[0].bars)
	var kinds []string
	var secs []Section
	var later []label
	for _, l := range p.labels {
		if p.interleav || l.voice == first {
			secs = append(secs, Section{Kind: l.kind, StartBar: l.bar})
			kinds = append(kinds, l.kind)
		} else {
			later = append(later, l)
		}
	}
	if !p.interleav {
		byVoice := map[string][]string{}
		var order []string
		for _, l := range later {
			if _, ok := byVoice[l.voice]; !ok {
				order = append(order, l.voice)
			}
			byVoice[l.voice] = append(byVoice[l.voice], l.kind)
		}
		for _, id := range order {
			if !equalStrings(byVoice[id], kinds) {
				p.warn("voice_sections_differ", 1)
				break
			}
		}
	}
	for i, l := range labelKinds(kinds) {
		secs[i].Label = l
		end := total
		if i+1 < len(secs) {
			end = secs[i+1].StartBar
		}
		secs[i].Bars = end - secs[i].StartBar
		if secs[i].Bars <= 0 {
			t.zeroSect = true
		}
	}
	t.Sections = secs
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var (
	preChorusRe = regexp.MustCompile(`pre[ -]?chorus`)
	digitsRe    = regexp.MustCompile(`[0-9]+`)
	kindTable   = map[string]string{
		"intro": "intro", "verse": "verse", "pre chorus": "pre-chorus", "prechorus": "pre-chorus",
		"chorus": "chorus", "bridge": "bridge", "interlude": "interlude", "instrumental": "instrumental",
		"instrumental break": "instrumental", "solo": "solo", "outro": "outro",
	}
)

// sectionKind is yue2.sectionKind: a comment such as "Verse 1" or "Outro - fade" names a kind; unknown text is not a label.
func sectionKind(text string) (string, bool) {
	s := strings.ToLower(text)
	s = preChorusRe.ReplaceAllString(s, "prechorus")
	if i := strings.IndexAny(s, "-–—:(+"); i >= 0 {
		s = s[:i]
	}
	s = digitsRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	k, ok := kindTable[s]
	return k, ok
}

func labelKinds(kinds []string) []string {
	total := map[string]int{}
	for _, k := range kinds {
		total[k]++
	}
	seen := map[string]int{}
	out := make([]string, len(kinds))
	for i, k := range kinds {
		seen[k]++
		if total[k] == 1 {
			out[i] = k
		} else {
			out[i] = k + " " + strconv.Itoa(seen[k])
		}
	}
	return out
}

// header value parsers

var (
	meterRe = regexp.MustCompile(`^(\d+)\s*/\s*(\d+)$`)
	tempoRe = regexp.MustCompile(`^(?:(\d+)\s*/\s*(\d+)\s*=\s*)?(\d+(?:\.\d+)?)$`)
)

func parseMeter(v string) (n, d int, ok bool) {
	switch v {
	case "C":
		return 4, 4, true
	case "C|":
		return 2, 2, true
	case "none", "":
		return 4, 4, true
	}
	m := meterRe.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(m[1])
	b, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || a < 1 || b < 1 || a > 128 || b > 128 {
		return 0, 0, false
	}
	return a, b, true
}

func parseUnit(v string) (Rat, bool) {
	m := meterRe.FindStringSubmatch(v)
	if m == nil {
		return Rat{}, false
	}
	a, err1 := strconv.Atoi(m[1])
	b, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || a < 1 || b < 1 || a > 128 || b > 128 {
		return Rat{}, false
	}
	return R(int64(a), int64(b)), true
}

func parseTempo(v string) (Tempo, bool) {
	var b strings.Builder
	inQ := false
	for _, r := range v {
		if r == '"' {
			inQ = !inQ
			b.WriteByte(' ')
			continue
		}
		if !inQ {
			b.WriteRune(r)
		}
	}
	raw := strings.TrimSpace(b.String())
	m := tempoRe.FindStringSubmatch(raw)
	if m == nil {
		return Tempo{}, false
	}
	unit := Rat{1, 4}
	if m[1] != "" {
		n, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		if n < 1 || d < 1 || n > 128 || d > 128 {
			return Tempo{}, false
		}
		unit = R(int64(n), int64(d))
	}
	bpm, err := strconv.ParseFloat(m[3], 64)
	if err != nil || bpm <= 0 || bpm > 2000 {
		return Tempo{}, false
	}
	return Tempo{Unit: unit, BPM: bpm, Raw: strings.ReplaceAll(raw, " ", "")}, true
}
