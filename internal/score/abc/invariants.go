package abc

import (
	"bytes"
	"fmt"
	"regexp"
)

// Violation is one broken structural invariant of an edit (C352.4). Bar is the 0-based bar index in Voice, or -1 when the
// violation is not about one bar; Voice is empty when it is not about one voice.
type Violation struct {
	Code  string `json:"code"`
	Voice string `json:"voice,omitempty"`
	Bar   int    `json:"bar"`
	Msg   string `json:"msg"`
}

// Allow names the header fields an edit may change.
type Allow struct {
	Tempo bool // Q: may differ (set_tempo)
}

// Violation codes.
const (
	VoicesChanged      = "voices_changed"
	BarCountChanged    = "bar_count_changed"
	BarLength          = "bar_length"
	HeaderChanged      = "header_changed"
	SectionsChanged    = "sections_changed"
	UnsupportedFeature = "unsupported_feature"
)

var inlineFieldRe = regexp.MustCompile(`\[[A-Za-z]:`)

// CheckInvariants compares an edited tune with its base and lists every way the edit changed the structure: the voices,
// the number of bars of each voice, the header (M:, L:, K:, and Q: unless allowed), the sections, an edited bar whose
// length is not the meter (the first and last bar of a voice may keep the original's length), and an edited bar that
// contains a repeat, volta or inline field. It never stops at the first violation. A bar is edited when its content or
// its bar-line markup differs from the base's bar of the same index.
func CheckInvariants(base, edited *Tune, a Allow) []Violation {
	if base == nil || edited == nil {
		return []Violation{{Code: VoicesChanged, Bar: -1, Msg: "missing tune"}}
	}
	var out []Violation
	add := func(code, voice string, bar int, f string, args ...any) {
		out = append(out, Violation{Code: code, Voice: voice, Bar: bar, Msg: fmt.Sprintf(f, args...)})
	}
	if !sameVoices(base, edited) {
		add(VoicesChanged, "", -1, "the voices (ids, names, programs or channels) changed")
	}
	byID := map[string]*Voice{}
	for i := range edited.Voices {
		byID[edited.Voices[i].ID] = &edited.Voices[i]
	}
	for _, bv := range base.Voices {
		ev := byID[bv.ID]
		if ev == nil {
			continue
		}
		if len(ev.Bars) != len(bv.Bars) {
			add(BarCountChanged, bv.ID, -1, "voice has %d bars, the original %d", len(ev.Bars), len(bv.Bars))
			continue
		}
		for i := range bv.Bars {
			ob, nb := bv.Bars[i], ev.Bars[i]
			if !barEdited(base.Src, edited.Src, ob, nb) {
				continue
			}
			if nb.RepeatStart || nb.RepeatEnd || nb.Volta != 0 || len(nb.Changes) > 0 || inlineFieldRe.Match(edited.Src[nb.Start:nb.End]) {
				add(UnsupportedFeature, bv.ID, i, "an edited bar holds a repeat, volta or inline field")
			}
			if nb.Length.Cmp(edited.Header.Meter) != 0 {
				ends := i == 0 || i == len(bv.Bars)-1
				if !(ends && nb.Length.Cmp(ob.Length) == 0) {
					add(BarLength, bv.ID, i, "bar is %d/%d whole notes long, the meter %d/%d is %d/%d", nb.Length.Num, nb.Length.Den,
						edited.Header.MeterNum, edited.Header.MeterDen, edited.Header.Meter.Num, edited.Header.Meter.Den)
				}
			}
		}
	}
	bh, eh := base.Header, edited.Header
	if bh.MeterNum != eh.MeterNum || bh.MeterDen != eh.MeterDen {
		add(HeaderChanged, "", -1, "M: changed")
	}
	if bh.Unit.Cmp(eh.Unit) != 0 {
		add(HeaderChanged, "", -1, "L: changed")
	}
	if bh.Key.Tonic != eh.Key.Tonic || bh.Key.Mode != eh.Key.Mode || bh.Key.Sharps != eh.Key.Sharps || bh.Key.Accidentals != eh.Key.Accidentals {
		add(HeaderChanged, "", -1, "K: changed")
	}
	if !a.Tempo && !sameTempo(bh.Tempo, eh.Tempo) {
		add(HeaderChanged, "", -1, "Q: changed")
	}
	if !sameSections(base.Sections, edited.Sections) {
		add(SectionsChanged, "", -1, "section labels or start bars changed")
	}
	return out
}

func barEdited(bsrc, esrc []byte, a, b Bar) bool {
	return !bytes.Equal(bsrc[a.Start:a.End], esrc[b.Start:b.End]) || a.Left != b.Left || a.Right != b.Right ||
		a.Volta != b.Volta || a.RepeatStart != b.RepeatStart || a.RepeatEnd != b.RepeatEnd
}

func sameVoices(a, b *Tune) bool {
	if len(a.Voices) != len(b.Voices) {
		return false
	}
	for i, v := range a.Voices {
		w := b.Voices[i]
		if v.ID != w.ID || v.Name != w.Name || !sameIntPtr(v.Program, w.Program) || !sameIntPtr(v.Channel, w.Channel) {
			return false
		}
	}
	return true
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameTempo(a, b *Tempo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Unit.Cmp(b.Unit) == 0 && a.BPM == b.BPM
}

func sameSections(a, b []Section) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Label != b[i].Label || a[i].StartBar != b[i].StartBar {
			return false
		}
	}
	return true
}
