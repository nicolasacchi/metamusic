package abc

import (
	"errors"
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/score"
)

// Report is the result of Check (C352.7). Every slice is non-nil except Violations, which is null when no base was
// given. Seconds are rounded to microseconds.
type Report struct {
	OK            bool            `json:"ok"`
	Errors        []ReportError   `json:"errors"`
	Warnings      []score.Warning `json:"warnings"`
	Dialect       string          `json:"dialect"`
	Header        *ReportHeader   `json:"header"`
	Voices        []ReportVoice   `json:"voices"`
	Sections      []ReportSection `json:"sections"`
	Bars          int             `json:"bars"`
	Timed         bool            `json:"timed"`
	SecondsPerBar *float64        `json:"seconds_per_bar"`
	DurationS     *float64        `json:"duration_s"`
	Events        int             `json:"events"`
	Chords        int             `json:"chords"`
	DivergentBars int             `json:"divergent_bars"`
	DisplayABC    *string         `json:"display_abc"`
	Violations    []Violation     `json:"violations"`
}

// ReportError is a syntax or limit error with its position.
type ReportError struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Rule string `json:"rule"`
}

// ReportHeader summarises the header: the written meter ("6/8"), the unit ("1/8"), the Q: text, the tempo in quarter
// notes per minute (null without a usable Q:) and the key.
type ReportHeader struct {
	Meter string   `json:"meter"`
	Unit  string   `json:"unit"`
	Q     string   `json:"q"`
	BPM   *float64 `json:"bpm"`
	Key   string   `json:"key"`
}

// ReportVoice is one voice with its bar count.
type ReportVoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Bars int    `json:"bars"`
}

// ReportSection is one section of the first voice.
type ReportSection struct {
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	StartBar int    `json:"start_bar"`
	Bars     int    `json:"bars"`
}

func round6(x float64) *float64 {
	r := math.Round(x*1e6) / 1e6
	return &r
}

// Check parses src with o and reports what the Desk shows about it: errors, warnings, header, voices, sections, timing,
// counts, the display text of a yue2 tune and, when base is given, the invariant violations against it. It is pure and
// reads nothing; it never panics.
func Check(src, base []byte, o Options) (r Report) {
	r = Report{Errors: []ReportError{}, Warnings: []score.Warning{}, Voices: []ReportVoice{}, Sections: []ReportSection{}, Dialect: string(o.dialect())}
	defer func() {
		if x := recover(); x != nil {
			r.OK = false
			r.Errors = append(r.Errors, ReportError{Line: 1, Col: 1, Rule: "internal"})
		}
	}()
	t, err := Parse(src, o)
	if err != nil {
		var se *SyntaxError
		if errors.As(err, &se) {
			r.Errors = append(r.Errors, ReportError{Line: se.Line, Col: se.Col, Rule: se.Rule})
		} else {
			r.Errors = append(r.Errors, ReportError{Line: 1, Col: 1, Rule: "internal"})
		}
		return r
	}
	if len(t.Warnings) > 0 {
		r.Warnings = t.Warnings
	}
	h := t.Header
	rh := &ReportHeader{Meter: fmt.Sprintf("%d/%d", h.MeterNum, h.MeterDen), Unit: fmt.Sprintf("%d/%d", h.Unit.Num, h.Unit.Den), Key: h.Key.Raw}
	if rh.Key == "" {
		rh.Key = "C"
	}
	if h.Tempo != nil {
		rh.Q = h.Tempo.Raw
		rh.BPM = round6(h.Tempo.BPM * h.Tempo.Unit.Float() * 4)
	}
	r.Header = rh
	for _, v := range t.Voices {
		r.Voices = append(r.Voices, ReportVoice{ID: v.ID, Name: v.Name, Bars: len(v.Bars)})
		for _, b := range v.Bars {
			r.Chords += len(b.Chords)
		}
	}
	for _, s := range t.Sections {
		r.Sections = append(r.Sections, ReportSection(s))
	}
	if len(t.Voices) > 0 {
		r.Bars = len(t.Voices[0].Bars)
	}
	if spb, ok := t.SecondsPerBar(); ok {
		r.Timed, r.SecondsPerBar = true, round6(spb)
	}
	if sc, err := t.Score(EventOptions{Chords: ChordsNone}); err != nil {
		rule, line, col := "internal", 1, 1
		var se *SyntaxError
		if errors.As(err, &se) {
			rule, line, col = se.Rule, se.Line, se.Col
		}
		r.Errors = append(r.Errors, ReportError{Line: line, Col: col, Rule: rule})
	} else {
		var end int64
		for _, v := range sc.Voices {
			r.Events += len(v.Notes)
			for _, n := range v.Notes {
				end = max(end, n.Tick+n.Dur)
			}
		}
		r.DurationS = round6(sc.SecondsAt(end))
	}
	if t.opts.dialect() == DialectYuE2 {
		_, r.DivergentBars = normSplices(t)
		if text, changed := Normalise(t); changed {
			s := string(text)
			r.DisplayABC = &s
		}
	}
	if len(base) > 0 {
		r.Violations = []Violation{}
		if bt, err := Parse(base, o); err != nil {
			r.Violations = append(r.Violations, Violation{Code: "base_unparsable", Bar: -1, Msg: err.Error()})
		} else {
			r.Violations = append(r.Violations, CheckInvariants(bt, t, Allow{})...)
		}
	}
	r.OK = len(r.Errors) == 0 && len(r.Violations) == 0
	return r
}
