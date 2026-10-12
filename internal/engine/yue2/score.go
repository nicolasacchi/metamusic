package yue2

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Score is the timing-relevant summary of a YuE2 ABC score.
type Score struct {
	Meter         string         // "4/4" ("C" -> "4/4", "C|" -> "2/2"); default "4/4" when M: absent
	Q             string         // raw "1/4=92"; "" if absent
	BPM           float64        //
	SecondsPerBar float64        // 0 => untimed
	Bars          int            // bars of the first voice
	Sections      []ScoreSection // from "% <label>" comment lines
	Warnings      []string       // e.g. voice_sections_differ
}

type ScoreSection struct {
	Kind, Label, Raw string
	StartBar, Bars   int
}

var (
	fieldRe   = regexp.MustCompile(`^([A-Za-z]):\s*(.*)$`)
	meterRe   = regexp.MustCompile(`^(\d+)\s*/\s*(\d+)$`)
	tempoRe   = regexp.MustCompile(`^(\d+)\s*/\s*(\d+)\s*=\s*(\d+(?:\.\d+)?)$`)
	commentRe = regexp.MustCompile(`^%\s*(.*?)\s*$`)
)

type scoreLabel struct {
	kind, raw, voice string
	bar              int // first-voice bar count where the label's voice block starts
}

// ParseScore reads the header and counts bars of the first voice. Anything it cannot time exactly
// (repeats, voltas, mid-tune meter/tempo changes, unsupported Q: forms) leaves SecondsPerBar at 0.
func ParseScore(abc []byte) (Score, error) {
	text := strings.ReplaceAll(string(abc), "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return Score{}, errors.New("yue2: empty score")
	}
	sc := Score{Meter: "4/4"}
	untimed := false
	mNum, mDen := 4.0, 4.0
	meterOK := true
	var qNum, qDen float64
	tempoOK := false

	inBody := false
	cur, first := "", ""
	firstSet := false // first = the voice carrying the first music line
	bars := 0
	hasNote := false
	voiceMusic := map[string]bool{} // voices that already had a music line
	interleaved := false
	var labels []scoreLabel
	var pending []scoreLabel // comments waiting for the next non-comment line
	curInFirst := func() bool { return !firstSet || cur == first }
	flushPending := func() {
		for _, l := range pending {
			l.voice, l.bar = cur, bars
			labels = append(labels, l)
		}
		pending = pending[:0]
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(line, "%%") {
			continue
		}
		if m := commentRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "%") {
			if k, ok := sectionKind(m[1]); ok {
				pending = append(pending, scoreLabel{kind: k, raw: m[1]})
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil && !strings.ContainsAny(line[:1], "|[") {
			f, v := m[1], strings.TrimSpace(m[2])
			if i := strings.Index(v, "%"); i >= 0 && f != "w" && f != "W" {
				v = strings.TrimSpace(v[:i])
			}
			if !inBody {
				switch f {
				case "M":
					switch v {
					case "C":
						sc.Meter, mNum, mDen = "4/4", 4, 4
					case "C|":
						sc.Meter, mNum, mDen = "2/2", 2, 2
					default:
						if mm := meterRe.FindStringSubmatch(v); mm != nil {
							a, _ := strconv.Atoi(mm[1])
							b, _ := strconv.Atoi(mm[2])
							if a > 0 && b > 0 {
								sc.Meter, mNum, mDen = mm[1]+"/"+mm[2], float64(a), float64(b)
							} else {
								meterOK = false
							}
						} else {
							meterOK = false
						}
					}
				case "Q":
					sc.Q, qNum, qDen, sc.BPM, tempoOK = parseTempo(v)
				case "K":
					inBody = true
				}
				continue
			}
			switch f {
			case "M", "Q":
				untimed = true
			case "V":
				id := v
				if i := strings.IndexAny(id, " \t"); i >= 0 {
					id = id[:i]
				}
				if id != cur {
					if curInFirst() && hasNote { // a trailing partial bar ends with its voice block
						bars++
						hasNote = false
					}
					if voiceMusic[id] {
						interleaved = true
					}
				}
				cur = id
				flushPending()
			}
			continue
		}
		if !inBody {
			inBody = true
		}
		flushPending()
		if !firstSet {
			firstSet, first = true, cur
		}
		voiceMusic[cur] = true
		if strings.Contains(line, "[V:") {
			untimed = true // inline voice switch: not tracked
		}
		if !curInFirst() {
			// still scan for untimed markers only in the first voice, per contract
			continue
		}
		b, h, u := scanBody(line, hasNote)
		bars += b
		hasNote = h
		if u {
			untimed = true
		}
	}
	if hasNote {
		bars++
	}
	flushPending()
	sc.Bars = bars

	// sections
	laterLabels := map[string][]string{} // kinds of labels seen in non-first voices
	var laterOrder, firstKinds []string
	for _, l := range labels {
		isFirst := !firstSet || l.voice == first
		switch {
		case interleaved || isFirst:
			sc.Sections = append(sc.Sections, ScoreSection{Kind: l.kind, Raw: l.raw, StartBar: l.bar})
			if isFirst {
				firstKinds = append(firstKinds, l.kind)
			}
		default:
			if _, ok := laterLabels[l.voice]; !ok {
				laterOrder = append(laterOrder, l.voice)
			}
			laterLabels[l.voice] = append(laterLabels[l.voice], l.kind)
		}
	}
	if !interleaved {
		for _, v := range laterOrder {
			if !equalStrings(laterLabels[v], firstKinds) {
				sc.Warnings = append(sc.Warnings, "voice_sections_differ")
				break
			}
		}
	}
	kinds := make([]string, len(sc.Sections))
	for i, s := range sc.Sections {
		kinds[i] = s.Kind
	}
	for i, l := range labelKinds(kinds) {
		sc.Sections[i].Label = l
		end := bars
		if i+1 < len(sc.Sections) {
			end = sc.Sections[i+1].StartBar
		}
		sc.Sections[i].Bars = end - sc.Sections[i].StartBar
		if sc.Sections[i].Bars <= 0 {
			untimed = true // a zero-bar section means the layout was not understood
		}
	}

	if !untimed && meterOK && tempoOK && bars > 0 && sc.BPM > 0 {
		sc.SecondsPerBar = (mNum / mDen) / (qNum / qDen) * 60 / sc.BPM
	}
	return sc, nil
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

// parseTempo handles "a/b=N" optionally with quoted text; anything else is unsupported.
func parseTempo(v string) (raw string, qn, qd, bpm float64, ok bool) {
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
	raw = strings.TrimSpace(b.String())
	m := tempoRe.FindStringSubmatch(raw)
	if m == nil {
		return raw, 0, 0, 0, false
	}
	n, _ := strconv.Atoi(m[1])
	d, _ := strconv.Atoi(m[2])
	f, _ := strconv.ParseFloat(m[3], 64)
	if n <= 0 || d <= 0 || f <= 0 {
		return raw, 0, 0, 0, false
	}
	return strings.ReplaceAll(raw, " ", ""), float64(n), float64(d), f, true
}

// scanBody counts completed bars on one music line. hasNote carries the "notes since last barline"
// state across lines. untimed reports repeat/volta/inline-field tokens.
func scanBody(line string, hasNote bool) (bars int, note bool, untimed bool) {
	rs := []rune(line)
	n := len(rs)
	skipTo := func(i int, end rune) int {
		i++
		for i < n && rs[i] != end {
			i++
		}
		return i
	}
	bar := func() {
		if hasNote {
			bars++
			hasNote = false
		}
	}
	for i := 0; i < n; i++ {
		switch c := rs[i]; {
		case c == '%':
			return bars, hasNote, untimed
		case c == '"':
			i = skipTo(i, '"')
		case c == '!':
			i = skipTo(i, '!')
		case c == '+':
			i = skipTo(i, '+')
		case c == '{':
			i = skipTo(i, '}')
		case c == '[':
			switch {
			case i+1 < n && rs[i+1] == '|':
				bar()
				i++
			case i+1 < n && (rs[i+1] == '1' || rs[i+1] == '2' || rs[i+1] == '3'):
				untimed = true
			case i+2 < n && rs[i+2] == ':' && ((rs[i+1] >= 'A' && rs[i+1] <= 'Z') || (rs[i+1] >= 'a' && rs[i+1] <= 'z')):
				if rs[i+1] == 'Q' || rs[i+1] == 'M' {
					untimed = true
				}
				i = skipTo(i, ']')
			default:
				hasNote = true // chord
				i = skipTo(i, ']')
			}
		case c == ':':
			if i+1 < n && (rs[i+1] == '|' || rs[i+1] == ':') {
				untimed = true
			}
		case c == '|':
			if i+1 < n && rs[i+1] == ':' {
				untimed = true
			}
			if i+1 < n && rs[i+1] >= '1' && rs[i+1] <= '3' {
				untimed = true
			}
			bar()
			for i+1 < n && (rs[i+1] == '|' || rs[i+1] == ']') {
				i++
			}
		case c == 'Z' || c == 'X': // multi-measure rest: Z, Z4 = that many complete bars
			bar() // any partial bar before it ends here
			j := i + 1
			for j < n && rs[j] >= '0' && rs[j] <= '9' {
				j++
			}
			cnt := 1
			if j > i+1 {
				v, err := strconv.Atoi(string(rs[i+1 : j]))
				if err != nil || v < 1 || v > 100000 {
					untimed = true
				} else {
					cnt = v
				}
			}
			bars += cnt
			i = j - 1
		case (c >= 'A' && c <= 'G') || (c >= 'a' && c <= 'g') || c == 'z' || c == 'x':
			hasNote = true
		}
	}
	return bars, hasNote, untimed
}

// AlignSections maps lyric sections onto score sections. ok iff the score is timed, the section
// counts are equal and kinds match in order; otherwise (nil, false) and sections stay untimed.
func AlignSections(lyr []Section, sc Score) (times [][2]float64, ok bool) {
	if sc.SecondsPerBar <= 0 || len(lyr) == 0 || len(sc.Sections) != len(lyr) {
		return nil, false
	}
	for i := range lyr {
		if lyr[i].Kind != sc.Sections[i].Kind {
			return nil, false
		}
	}
	times = make([][2]float64, len(lyr))
	for i, s := range sc.Sections {
		times[i] = [2]float64{float64(s.StartBar) * sc.SecondsPerBar, float64(s.StartBar+s.Bars) * sc.SecondsPerBar}
	}
	return times, true
}
