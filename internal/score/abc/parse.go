// Package abc is the program's single ABC parser (C352). It reads YuE2's dialect and standard ABC 2.1 into a syntax tree
// that keeps the byte span of every bar, and into note events (internal/score). It imports only the standard library and
// internal/score.
package abc

import (
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/score"
)

// MaxBytes is the largest score Parse accepts (the transform limit MaxScoreBytes).
const MaxBytes = 262144

// Warning is a non-fatal observation (an ignored decoration, an irregular bar, an unknown chord).
type Warning = score.Warning

// Tune is a parsed score. Every Bar's Start and End index Src.
type Tune struct {
	Header   Header
	Voices   []Voice // in order of first music
	Sections []Section
	Features []Feature
	Warnings []Warning
	Src      []byte

	opts     Options
	zeroSect bool // a section with no bars: the layout was not understood
}

// Header is the tune header. Meter and Unit are reduced bar and note lengths in whole notes; MeterNum/MeterDen are the
// written meter (6/8 stays 6/8 here and is 3/4 in Meter).
type Header struct {
	Meter, Unit        Rat
	MeterNum, MeterDen int
	Tempo              *Tempo // nil when Q: is absent or unsupported
	Key                Key
	Fields             map[string]string // first occurrence of each header field letter (X, T, C, ...)
	BodyStart          int               // byte offset of the first line after K:
}

// Tempo is a Q: field: BPM beats of Unit per minute.
type Tempo struct {
	Unit Rat
	BPM  float64
	Raw  string
}

// Voice is one voice (V: line, or the implicit voice "1").
type Voice struct {
	ID, Name string
	Program  *int // %%MIDI program
	Channel  *int // %%MIDI channel, 1-based as in ABC (1..16)
	Bars     []Bar
}

// Bar is one bar of one voice. Start and End are the byte span of its content, bar lines (and volta marks) excluded; a
// multi-bar rest (Z4) yields that many bars sharing one span.
type Bar struct {
	Index, Line int
	Start, End  int
	Notes       []Note
	Chords      []Chord
	Length      Rat // sum of the durations of its content
	Left, Right string
	RepeatStart bool // Left opens a repeat
	RepeatEnd   bool // Right closes a repeat
	Volta       int  // 1, 2 ... when the bar is inside a first/second ending
	Changes     []Change
}

// Change is a tempo or meter change inside a bar, from an inline field or a field line.
type Change struct {
	At       Rat
	Tempo    *Tempo
	MeterNum int
	MeterDen int
}

// Note is one note or rest. Pitch is the MIDI number with the key signature and the dialect's accidentals applied (-1
// for a rest); At is its offset from the bar start, Dur its length, both in whole notes; Start and End are the byte span
// of its token.
type Note struct {
	Pitch      int
	At, Dur    Rat
	Tie        bool
	Start, End int
}

// Chord is a chord symbol ("Am7"): Root is a pitch class 0..11, Quality one of the canonical names of chords.go.
type Chord struct {
	Root    int
	Quality string
	Raw     string
	At      Rat
}

// Section is a "% verse" comment label: Kind from the yue2 synonym table, Label the kind numbered when repeated, StartBar
// and Bars counted in bars of the first voice.
type Section struct {
	Label, Kind string
	StartBar    int
	Bars        int
}

// Feature records syntax that changes how the tune is timed or edited: repeat, volta, inline_field.
type Feature struct {
	Code   string
	Line   int
	Detail string // for inline_field: the field letter
}

// SyntaxError is the error Parse returns for text it refuses.
type SyntaxError struct {
	Line, Col int
	Rule, Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("abc: line %d col %d: %s: %s", e.Line, e.Col, e.Rule, e.Msg)
}

// Parse reads src. It never panics and returns *SyntaxError for the rules of C352.1.
func Parse(src []byte, o Options) (t *Tune, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = nil, &SyntaxError{Line: 1, Col: 1, Rule: "internal", Msg: fmt.Sprint("parser fault: ", r)}
		}
	}()
	if len(src) > MaxBytes {
		return nil, &SyntaxError{Line: 1, Col: 1, Rule: "too_large", Msg: "score exceeds the size limit"}
	}
	if d := o.dialect(); d != DialectStandard && d != DialectYuE2 {
		return nil, &SyntaxError{Line: 1, Col: 1, Rule: "bad_dialect", Msg: "unknown dialect"}
	}
	p := newParser(src, o)
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.t, nil
}
