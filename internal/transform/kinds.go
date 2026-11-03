package transform

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// RuleError is a parameter refusal: 422 INVALID_TRANSFORM {field, rule}.
type RuleError struct{ Field, Rule string }

func (e *RuleError) Error() string { return "transform: invalid " + e.Field + ": " + e.Rule }

// Warning is one coded note of an estimate or a finished render.
type Warning struct{ Code, Field, Message string }

// ParamSpec describes one parameter in the capabilities table; the same Go table validates (TestCapabilities_MatchesValidation).
type ParamSpec struct {
	Name     string
	Type     string // number | integer | string | boolean | enum | track | json
	Default  any
	Min, Max *float64
	Enum     []string
	Required bool
}

// KindCapabilities is the capabilities entry of one kind (C212.5, C223.2). Listening is "headphones" for binaural and empty
// otherwise; Dataset is the credit string of the HRTF dataset (binaural only).
type KindCapabilities struct {
	Kind       Kind
	Summary    string
	Inputs     int
	Params     []ParamSpec
	BlindSpots []string
	Example    string
	Listening  string
	Dataset    string
}

// UnitsIn feeds a kind's work-unit model (C209): the output frame count, channels and rate and the resolved parameters.
type UnitsIn struct {
	InFrames, OutFrames int64
	Channels, Rate      int
	Resolved            json.RawMessage
}

// TitleIn feeds a kind's default title label (C202.4): the primary and secondary parent titles and the resolved parameters.
type TitleIn struct {
	Primary, Secondary string
	Resolved           json.RawMessage
}

// WarnIn feeds a kind's estimate-time warnings, computed from database rows only: the resolved parameters and the kinds of
// the primary-parent chain (nearest first), so binaural can refuse and warn without reading audio.
type WarnIn struct {
	Resolved json.RawMessage
	Chain    []Kind
}

// KindSpec is everything the service needs to know about one kind. The T1a five register from B2's own files, the hi-fi kinds
// from kind_master.go, kind_widen.go and kind_binaural.go (C225.4); nothing else looks a kind up by name.
type KindSpec struct {
	Kind Kind
	// Validate checks the parameters of the create/estimate body and returns them with every default written out. A refusal
	// is a *RuleError.
	Validate func(params json.RawMessage) (resolved json.RawMessage, err error)
	// Units is the work-unit model of the operator (C209.1).
	Units func(UnitsIn) int64
	// Title is the label after the parent title ("glacier ×20").
	Title func(TitleIn) string
	// Capabilities generates the kind's capabilities entry from the same tables Validate enforces.
	Capabilities func() KindCapabilities
	// MemFactor is k in the memory estimate (C209.4).
	MemFactor float64
	// Warnings are the estimate-time warnings (nil for none).
	Warnings func(WarnIn) []Warning
}

var (
	kindsMu  sync.Mutex
	kindsReg = map[Kind]KindSpec{}
)

// RegisterKind adds a kind at init time. It panics on a kind outside the nine, a duplicate, or a spec without its required
// functions: a misregistration is a programming error found by every test run.
func RegisterKind(s KindSpec) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	switch {
	case !s.Kind.Valid():
		panic(fmt.Sprintf("transform: RegisterKind of unknown kind %q", s.Kind))
	case s.Validate == nil || s.Units == nil || s.Title == nil || s.Capabilities == nil:
		panic(fmt.Sprintf("transform: RegisterKind(%s) lacks Validate, Units, Title or Capabilities", s.Kind))
	case s.MemFactor < 0:
		panic(fmt.Sprintf("transform: RegisterKind(%s) has a negative MemFactor", s.Kind))
	}
	if _, dup := kindsReg[s.Kind]; dup {
		panic(fmt.Sprintf("transform: kind %q registered twice", s.Kind))
	}
	kindsReg[s.Kind] = s
}

// LookupKind returns the registered spec of k.
func LookupKind(k Kind) (KindSpec, bool) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	s, ok := kindsReg[k]
	return s, ok
}

// RegisteredKinds returns the registered kinds in canonical order (the order of AllKinds, which is the CHECK list), whatever
// the order of registration.
func RegisteredKinds() []Kind {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	out := make([]Kind, 0, len(kindsReg))
	for k := range kindsReg {
		out = append(out, k)
	}
	rank := map[Kind]int{}
	for i, k := range AllKinds {
		rank[k] = i
	}
	sort.Slice(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}
