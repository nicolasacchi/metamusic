package songpr

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// BarChange is one replace_bars op as the reviewer reads it: the bars of the voice before and after, "|"-separated.
type BarChange struct {
	Voice   string `json:"voice"`
	BarFrom int    `json:"bar_from"`
	BarTo   int    `json:"bar_to"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// barsText reads the content of bars from..to of a voice, trimmed and "|"-separated.
func barsText(t *abc.Tune, vi, from, to int) string {
	parts := make([]string, 0, to-from+1)
	for _, b := range t.Voices[vi].Bars[from : to+1] {
		parts = append(parts, strings.TrimSpace(string(t.Src[b.Start:b.End])))
	}
	return strings.Join(parts, " | ")
}

// compileBars validates the replace_bars ops (opIdx indexes in.Ops) against the aligned base score and applies them with
// abc.Edit (D-DESK-13, C362): at most MaxBars (voice, bar) pairs, one voice per op, every bar inside a scoped section's bar
// range, and the structural invariants of the result. It adds every violation it finds and returns the patched score, the
// per-op diff and the number of distinct bars replaced. A nil text means nothing was produced (a violation, or no change).
func (c *compiler) compileBars(in CompileInput, secs []yue2.Section, scoped map[int]bool, opIdx []int) (patched *string, diff []BarChange, bars int) {
	first := opIdx[0]
	if len(in.BaseScore) == 0 {
		c.add(CodePatchInvalid, "no_score", "ops", first, "")
		return nil, nil, 0
	}
	opts := abc.Options{Dialect: abc.DialectYuE2}
	tune, err := abc.Parse(in.BaseScore, opts)
	if err != nil {
		c.add(CodePatchInvalid, "score_unparsable", "ops", first, "")
		return nil, nil, 0
	}
	if !in.BaseScoreAligned || len(tune.Sections) != len(secs) {
		c.add(CodePatchOutOfBounds, "score_unaligned", "ops", first, "")
		return nil, nil, 0
	}
	for i, s := range tune.Sections {
		if s.Kind != secs[i].Kind {
			c.add(CodePatchOutOfBounds, "score_unaligned", "ops", first, "")
			return nil, nil, 0
		}
	}
	inScope := func(bar int) bool {
		for i, s := range tune.Sections {
			if scoped[i] && bar >= s.StartBar && bar < s.StartBar+s.Bars {
				return true
			}
		}
		return false
	}

	type pair struct {
		voice string
		bar   int
	}
	seen := map[pair]bool{}
	ops := make([]abc.Op, 0, len(opIdx))
	voiceIdx := make([]int, 0, len(opIdx))
	ok, sound := true, true // sound: every op names a voice and a range, so Edit can run to list the invariants too
	for _, oi := range opIdx {
		op := in.Ops[oi]
		vi := slices.IndexFunc(tune.Voices, func(v abc.Voice) bool { return v.ID == op.Voice })
		if vi < 0 {
			c.add(CodePatchInvalid, "bad_voice", "voice", oi, "")
			ok, sound = false, false
			continue
		}
		if op.BarFrom < 0 || op.BarTo < op.BarFrom || op.BarTo >= len(tune.Voices[vi].Bars) {
			c.add(CodePatchInvalid, "bad_range", "bar_to", oi, "")
			ok, sound = false, false
			continue
		}
		for b := op.BarFrom; b <= op.BarTo; b++ {
			if !inScope(b) {
				c.add(CodePatchOutOfBounds, "bars_out_of_scope", "bar_from", oi, fmt.Sprintf("bar %d", b))
				ok = false
				break
			}
		}
		for b := op.BarFrom; b <= op.BarTo; b++ {
			seen[pair{op.Voice, b}] = true
		}
		ops = append(ops, abc.Op{Op: abc.OpReplaceBars, Voice: op.Voice, BarFrom: op.BarFrom, BarTo: op.BarTo, ABC: op.ABC})
		voiceIdx = append(voiceIdx, vi)
	}
	bars = len(seen)
	if bars > MaxBars {
		c.add(CodePatchOutOfBounds, "too_many_bars", "ops", -1, fmt.Sprintf("%d bars", bars))
		ok = false
	}
	if !sound {
		return nil, nil, bars
	}

	out, edited, vs, err := abc.Edit(in.BaseScore, tune, ops, opts)
	var ee *abc.EditError
	switch {
	case errors.As(err, &ee):
		at := -1
		if ee.Op >= 0 && ee.Op < len(opIdx) {
			at = opIdx[ee.Op]
		}
		c.add(CodePatchInvalid, "abc_"+ee.Rule, "abc", at, "")
		return nil, nil, bars
	case err != nil:
		c.add(CodePatchInvalid, "abc_internal", "abc", first, "")
		return nil, nil, bars
	}
	for _, v := range vs {
		d := v.Msg
		if v.Voice != "" {
			d = fmt.Sprintf("voice %s: %s", v.Voice, d)
		}
		if v.Bar >= 0 {
			d = fmt.Sprintf("bar %d: %s", v.Bar, d)
		}
		c.add(CodePatchInvalid, "abc_"+v.Code, "abc", -1, d)
	}
	if !ok || len(vs) > 0 || bytes.Equal(out, in.BaseScore) {
		return nil, nil, bars
	}
	for i, op := range ops {
		before, after := barsText(tune, voiceIdx[i], op.BarFrom, op.BarTo), barsText(edited, voiceIdx[i], op.BarFrom, op.BarTo)
		if before != after {
			diff = append(diff, BarChange{Voice: op.Voice, BarFrom: op.BarFrom, BarTo: op.BarTo, Before: before, After: after})
		}
	}
	text := string(out)
	return &text, diff, bars
}
