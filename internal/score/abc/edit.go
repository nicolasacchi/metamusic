package abc

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Edit operation names and limits (C352.5).
const (
	OpReplaceBars   = "replace_bars"
	OpTransposeBars = "transpose_bars"
	OpSetTempo      = "set_tempo"

	MaxOps         = 20
	MaxReplaceBars = 64
	MaxSemitones   = 12
	MinBPM         = 40
	MaxBPM         = 240
)

// Op is one edit. Bars are 0-based and inclusive. replace_bars uses Voice (required), BarFrom, BarTo and ABC;
// transpose_bars uses Voice ("" is every voice), BarFrom, BarTo and Semitones; set_tempo uses BPM.
type Op struct {
	Op        string `json:"op"`
	Voice     string `json:"voice,omitempty"`
	BarFrom   int    `json:"bar_from,omitempty"`
	BarTo     int    `json:"bar_to,omitempty"`
	ABC       string `json:"abc,omitempty"`
	Semitones int    `json:"semitones,omitempty"`
	BPM       int    `json:"bpm,omitempty"`
}

// EditError is an operation Edit refuses or whose result does not parse. Op is the index of the operation (-1 when it is
// about the call); Rule is a stable lower_snake code.
type EditError struct {
	Op        int
	Rule, Msg string
	Line, Col int // of the parse error for result_unparsable
}

func (e *EditError) Error() string {
	return fmt.Sprintf("abc edit: op %d: %s: %s", e.Op, e.Rule, e.Msg)
}

func editErr(op int, rule, f string, args ...any) *EditError {
	return &EditError{Op: op, Rule: rule, Msg: fmt.Sprintf(f, args...)}
}

// splice replaces src[s:e] with r.
type splice struct {
	s, e int
	r    []byte
}

func applySplices(src []byte, sp []splice) ([]byte, error) {
	sort.SliceStable(sp, func(i, j int) bool { return sp[i].s < sp[j].s })
	out := make([]byte, 0, len(src)+64)
	pos := 0
	for _, x := range sp {
		if x.s < pos || x.e < x.s || x.e > len(src) {
			return nil, fmt.Errorf("overlapping or out of range splice")
		}
		out = append(out, src[pos:x.s]...)
		out = append(out, x.r...)
		pos = x.e
	}
	return append(out, src[pos:]...), nil
}

// Edit applies ops in order to src (t is its parse, or nil) and returns the new text, its parse and the invariant
// violations of the result against t (CheckInvariants). Bar indexes of each operation refer to the text its predecessors
// produced; replace_bars keeps the bar count by construction (it fixes the segment count; CheckInvariants, not this
// comment, guards it). Every byte outside the edited bars' spans
// (and the Q: line of set_tempo) is unchanged. A malformed operation, or a result that does not parse, returns an
// *EditError and no output; violations return the output with the list. Edit never panics.
func Edit(src []byte, t *Tune, ops []Op, o Options) (out []byte, edited *Tune, v []Violation, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, edited, v, err = nil, nil, nil, editErr(-1, "internal", "edit fault: %v", r)
		}
	}()
	if len(ops) == 0 {
		return nil, nil, nil, editErr(-1, "no_ops", "no operations")
	}
	if len(ops) > MaxOps {
		return nil, nil, nil, editErr(-1, "too_many_ops", "more than %d operations", MaxOps)
	}
	base := t
	if base == nil || !bytes.Equal(base.Src, src) {
		var perr error
		if base, perr = Parse(src, o); perr != nil {
			return nil, nil, nil, parseEditErr(-1, "source_unparsable", perr)
		}
	}
	cur, text := base, slices.Clone(base.Src)
	var allow Allow
	for i, op := range ops {
		var sp []splice
		var verify func(*Tune) error
		switch op.Op {
		case OpReplaceBars:
			sp, err = replaceSplices(cur, op, i)
		case OpTransposeBars:
			sp, verify, err = transposeSplices(cur, op, i)
		case OpSetTempo:
			allow.Tempo = true
			sp, err = tempoSplices(cur, op, i)
		default:
			err = editErr(i, "bad_op", "unknown operation %q", op.Op)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		next, aerr := applySplices(text, sp)
		if aerr != nil {
			return nil, nil, nil, editErr(i, "overlap", "%v", aerr)
		}
		nt, perr := Parse(next, o)
		if perr != nil {
			return nil, nil, nil, parseEditErr(i, "result_unparsable", perr)
		}
		if verify != nil {
			vt := nt
			if editVerifyHook != nil {
				vt = editVerifyHook(nt)
			}
			if verr := verify(vt); verr != nil {
				return nil, nil, nil, editErr(i, "transpose_unverified", "%v", verr)
			}
		}
		text, cur = next, nt
	}
	return text, cur, CheckInvariants(base, cur, allow), nil
}

// editVerifyHook is a test seam: it may substitute the re-parsed tune that a transpose verifier sees. Nil in production.
var editVerifyHook func(*Tune) *Tune

func parseEditErr(op int, rule string, err error) *EditError {
	e := editErr(op, rule, "%v", err)
	var se *SyntaxError
	if errors.As(err, &se) {
		e.Line, e.Col = se.Line, se.Col
		e.Msg = se.Rule + ": " + se.Msg
	}
	return e
}

func findVoice(t *Tune, id string) int {
	for i, v := range t.Voices {
		if v.ID == id {
			return i
		}
	}
	return -1
}

// sharedSpan reports whether a bar in [from, to] shares its source span with a neighbour (a multi-bar rest Z4 makes
// several bars of one span), which an edit cannot address separately.
func sharedSpan(bars []Bar, from, to int) bool {
	same := func(a, b Bar) bool { return a.Start == b.Start && a.End == b.End }
	for i := from; i <= to; i++ {
		if (i > 0 && same(bars[i], bars[i-1])) || (i+1 < len(bars) && same(bars[i], bars[i+1])) {
			return true
		}
	}
	return false
}

var fieldStartRe = regexp.MustCompile(`^[A-Za-z]:`)

// splitBars splits on '|' outside double-quoted strings (chord symbols, annotations).
func splitBars(s string) []string {
	var out []string
	q, from := false, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			q = !q
		case '|':
			if !q {
				out = append(out, s[from:i])
				from = i + 1
			}
		}
	}
	return append(out, s[from:])
}

func replaceSplices(t *Tune, op Op, oi int) ([]splice, error) {
	vi := -1
	if op.Voice != "" {
		vi = findVoice(t, op.Voice)
	}
	if vi < 0 {
		return nil, editErr(oi, "bad_voice", "replace_bars needs an existing voice")
	}
	bars := t.Voices[vi].Bars
	n := op.BarTo - op.BarFrom + 1
	if op.BarFrom < 0 || op.BarTo < op.BarFrom || op.BarTo >= len(bars) || n > MaxReplaceBars {
		return nil, editErr(oi, "bad_range", "bars %d..%d are not a range of at most %d bars inside the voice's %d", op.BarFrom, op.BarTo, MaxReplaceBars, len(bars))
	}
	if strings.ContainsAny(op.ABC, "\r\n%") {
		return nil, editErr(oi, "bad_abc", "the replacement must be one line without a comment")
	}
	if t := strings.TrimSpace(op.ABC); fieldStartRe.MatchString(t) || strings.HasSuffix(t, "\\") {
		return nil, editErr(oi, "bad_abc", "the replacement must not be a field line or end in a line continuation")
	}
	segs := splitBars(op.ABC)
	if len(segs) != n {
		return nil, editErr(oi, "bad_abc", "the replacement holds %d bars, the range %d", len(segs), n)
	}
	if sharedSpan(bars, op.BarFrom, op.BarTo) {
		return nil, editErr(oi, "shared_span", "a bar in the range shares its source with another (multi-bar rest)")
	}
	sp := make([]splice, 0, n)
	for i, s := range segs {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, editErr(oi, "bad_abc", "bar %d of the replacement is empty", i)
		}
		b := bars[op.BarFrom+i]
		sp = append(sp, splice{b.Start, b.End, []byte(s)})
	}
	return sp, nil
}

func tempoSplices(t *Tune, op Op, oi int) ([]splice, error) {
	if op.BPM < MinBPM || op.BPM > MaxBPM {
		return nil, editErr(oi, "bad_tempo", "bpm %d is outside %d..%d", op.BPM, MinBPM, MaxBPM)
	}
	line := []byte(fmt.Sprintf("Q:1/4=%d", op.BPM))
	src := t.Src
	end := min(t.Header.BodyStart, len(src))
	pos := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		pos = 3
	}
	qs, qe, ks := -1, -1, -1
	for pos < end {
		le := bytes.IndexByte(src[pos:end], '\n')
		next := end
		to := end
		if le >= 0 {
			to, next = pos+le, pos+le+1
		}
		for to > pos && (src[to-1] == '\r' || src[to-1] == ' ' || src[to-1] == '\t') {
			to--
		}
		switch {
		case bytes.HasPrefix(src[pos:to], []byte("Q:")):
			qs, qe = pos, to // a later Q: wins, as in the parser
		case bytes.HasPrefix(src[pos:to], []byte("K:")):
			ks = pos
		}
		pos = next
	}
	if qs >= 0 {
		return []splice{{qs, qe, line}}, nil
	}
	if ks < 0 {
		return nil, editErr(oi, "no_key", "the header has no K: line to put Q: before")
	}
	nl := "\n"
	if ks > 0 && src[ks-1] == '\n' && ks > 1 && src[ks-2] == '\r' {
		nl = "\r\n"
	}
	return []splice{{ks, ks, append(line, nl...)}}, nil
}
