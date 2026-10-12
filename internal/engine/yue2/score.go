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
	kind, raw string
	bar       int
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
	firstSet := false
	bars := 0
	hasNote := false
	var seq []string // body voice-switch sequence
	seenVoices := map[string]bool{}
	interleaved := false
	var labels []scoreLabel
	laterLabels := map[string][]string{} // kinds of labels seen in non-first voices
	var laterOrder []string
	var firstKinds []string
	curInFirst := func() bool { return cur == "" || cur == first }

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.HasPrefix(line, "%%") {
			continue
		}
		if m := commentRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "%") {
			if k, ok := sectionKind(m[1]); ok {
				labels = append(labels, scoreLabel{kind: k, raw: m[1], bar: bars})
				if curInFirst() {
					firstKinds = append(firstKinds, k)
				} else {
					if _, ok := laterLabels[cur]; !ok {
						laterOrder = append(laterOrder, cur)
					}
					laterLabels[cur] = append(laterLabels[cur], k)
					labels[len(labels)-1].bar = -1 - bars // marks a later-voice label; decoded below
				}
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
				if !firstSet {
					firstSet = true
					if bars > 0 || hasNote {
						first = ""
					} else {
						first = id
					}
				}
				if id != cur {
					if seenVoices[id] {
						interleaved = true
					}
					seenVoices[id] = true
					seq = append(seq, id)
				}
				cur = id
			}
			continue
		}
		if !inBody {
			inBody = true
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
	_ = seq
	sc.Bars = bars

	// sections
	if interleaved {
		for _, l := range labels {
			bar := l.bar
			if bar < 0 {
				bar = -1 - bar
			}
			sc.Sections = append(sc.Sections, ScoreSection{Kind: l.kind, Raw: l.raw, StartBar: bar})
		}
	} else {
		for _, l := range labels {
			if l.bar < 0 {
				continue
			}
			sc.Sections = append(sc.Sections, ScoreSection{Kind: l.kind, Raw: l.raw, StartBar: l.bar})
		}
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
		case (c >= 'A' && c <= 'G') || (c >= 'a' && c <= 'g') || c == 'z' || c == 'Z' || c == 'x' || c == 'X':
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
