package transform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
)

// This file is the parameter side of the five T1a kinds (C202.3): one table per kind drives the validation, the written-out
// defaults and (through capabilities.go) the capabilities answer, so a listed range is an enforced range
// (TestCapabilities_MatchesValidation). The hi-fi kinds register from their own files (C225.4) with their own tables.

// Rules of 422 INVALID_TRANSFORM that codes.go (frozen by F1) does not name.
const (
	RuleUnknownField = "unknown_field"   // a parameter the kind does not have
	RuleType         = "type"            // wrong JSON type for the parameter
	RuleATooShort    = "a_too_short"     // the transition would run past the end of A (C205.1)
	RuleBTooShort    = "b_too_short"     // the tail would run past the end of B (C205.1)
	RuleStartTooLate = "start_after_end" // start_s lies at or after the end of the input
)

// pdef is one parameter of a kind.
type pdef struct {
	Name     string
	Type     string // number | integer | string | enum | track | json
	Default  any    // written into the spec when the parameter is absent or null; nil means "no static default"
	Min, Max *float64
	Decimals int // numbers are rounded to this many decimals (the integer rule, C218.3); -1 keeps the value as sent
	Enum     []string
	Required bool
	Derived  bool // the default depends on the inputs and is filled by resolution (resolve.go)
	NullIsV  bool // an explicit null is a value (ensemble dry_db: minus infinity), not "the default"
}

func fp(v float64) *float64 { return &v }

var morphParams = []pdef{
	{Name: "track_a_id", Type: "track", Required: true, Decimals: -1},
	{Name: "track_b_id", Type: "track", Required: true, Decimals: -1},
	{Name: "bars", Type: "integer", Default: 8, Min: fp(1), Max: fp(32), Decimals: -1},
	{Name: "a_at", Type: "json", Derived: true, Decimals: -1},
	{Name: "b_at", Type: "json", Derived: true, Decimals: -1},
	{Name: "bpm_a", Type: "number", Min: fp(40), Max: fp(240), Decimals: 2, Derived: true},
	{Name: "bpm_b", Type: "number", Min: fp(40), Max: fp(240), Decimals: 2, Derived: true},
	{Name: "beats_per_bar", Type: "integer", Default: 4, Min: fp(2), Max: fp(7), Decimals: -1},
	{Name: "downbeat_a_s", Type: "json", Derived: true, Decimals: -1},
	{Name: "downbeat_b_s", Type: "json", Derived: true, Decimals: -1},
	{Name: "key_policy", Type: "enum", Default: "to_a", Enum: []string{"to_a", "none"}, Decimals: -1},
	{Name: "semitones_b", Type: "number", Min: fp(-12), Max: fp(12), Decimals: 2, Derived: true},
}

var shepardParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "direction", Type: "enum", Default: "up", Enum: []string{"up", "down"}, Decimals: -1},
	{Name: "loop_bars", Type: "integer", Min: fp(1), Max: fp(8), Decimals: -1, Derived: true},
	{Name: "loop_s", Type: "number", Min: fp(1), Max: fp(20), Decimals: 3, Derived: true},
	{Name: "start_s", Type: "number", Min: fp(0), Decimals: 3, Derived: true},
	{Name: "layers", Type: "integer", Default: 5, Min: fp(3), Max: fp(7), Decimals: -1},
	{Name: "period_s", Type: "number", Default: 24.0, Min: fp(8), Max: fp(120), Decimals: 3},
	{Name: "duration_s", Type: "number", Default: 120.0, Min: fp(8), Max: fp(600), Decimals: 3},
	{Name: "bell_sigma_octaves", Type: "number", Min: fp(0.4), Max: fp(2), Decimals: 2, Derived: true},
}

var glacierParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "factor", Type: "number", Default: 20.0, Min: fp(10), Max: fp(50), Decimals: 2},
	{Name: "start_s", Type: "number", Default: 0.0, Min: fp(0), Decimals: 3},
	{Name: "length_s", Type: "number", Min: fp(0.001), Decimals: 3, Derived: true},
	{Name: "window_s", Type: "number", Default: 0.5, Min: fp(0.1), Max: fp(2), Decimals: 3},
	{Name: "fade_in_s", Type: "number", Default: 4.0, Min: fp(0), Max: fp(30), Decimals: 3},
	{Name: "fade_out_s", Type: "number", Default: 4.0, Min: fp(0), Max: fp(30), Decimals: 3},
}

var polytempoParams = []pdef{
	{Name: "track_a_id", Type: "track", Required: true, Decimals: -1},
	{Name: "track_b_id", Type: "track", Required: true, Decimals: -1},
	{Name: "bpm_a", Type: "number", Min: fp(40), Max: fp(240), Decimals: 2, Derived: true},
	{Name: "bpm_b", Type: "number", Min: fp(40), Max: fp(240), Decimals: 2, Derived: true},
	{Name: "beats_per_bar", Type: "integer", Default: 4, Min: fp(2), Max: fp(7), Decimals: -1},
	{Name: "downbeat_a_s", Type: "json", Derived: true, Decimals: -1},
	{Name: "downbeat_b_s", Type: "json", Derived: true, Decimals: -1},
	{Name: "converge_bar", Type: "integer", Min: fp(4), Decimals: -1, Derived: true},
	{Name: "unison_bars", Type: "integer", Default: 8, Min: fp(0), Max: fp(64), Decimals: -1},
	{Name: "pan", Type: "enum", Default: "split", Enum: []string{"split", "centre"}, Decimals: -1},
}

// ensembleSpaces are the fixed room presets with the rt60 of C202.3 (the estimate's tail bound uses it).
var ensembleSpaces = []string{"cathedral", "cistern", "hangar", "canyon"}

var ensembleParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "space", Type: "enum", Default: "cathedral", Enum: ensembleSpaces, Decimals: -1},
	{Name: "wet_db", Type: "number", Default: -6.0, Min: fp(-40), Max: fp(0), Decimals: 1},
	{Name: "dry_db", Type: "number", Default: -12.0, Min: fp(-40), Max: fp(0), Decimals: 1, NullIsV: true},
	{Name: "tail_s", Type: "number", Default: 20.0, Min: fp(0), Max: fp(40), Decimals: 3},
}

var sectionRE = regexp.MustCompile(`^[a-z_]+#[0-9]+$`)

// rule builds the refusal of one field.
func rule(field, r string) error { return &RuleError{Field: field, Rule: r} }

// decoded holds the checked values of one parameter object: float64 for numbers, int for integers, string, or the raw JSON
// of a "json" parameter; a present null of a NullIsV parameter is stored as nil.
type decoded map[string]any

func (d decoded) str(name string) (string, bool) { s, ok := d[name].(string); return s, ok }

// decode checks raw against table: unknown fields, JSON types, enums and ranges. An absent or null parameter is left out
// (the default is written by the caller) unless it is NullIsV. Errors are *RuleError naming the field.
func decode(table []pdef, raw json.RawMessage) (decoded, error) {
	out := decoded{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, rule("params", RuleType)
	}
	byName := map[string]pdef{}
	for _, p := range table {
		byName[p.Name] = p
	}
	names := make([]string, 0, len(members))
	for k := range members {
		names = append(names, k)
	}
	sort.Strings(names) // a deterministic first refusal
	for _, k := range names {
		if _, ok := byName[k]; !ok {
			return nil, rule(k, RuleUnknownField)
		}
	}
	for _, p := range table {
		v, present := members[p.Name]
		if !present {
			continue
		}
		if string(bytes.TrimSpace(v)) == "null" {
			if p.NullIsV {
				out[p.Name] = nil
			}
			continue
		}
		val, err := decodeOne(p, v)
		if err != nil {
			return nil, err
		}
		out[p.Name] = val
	}
	for _, p := range table {
		if _, ok := out[p.Name]; p.Required && !ok {
			return nil, rule(p.Name, RuleRequired)
		}
	}
	return out, nil
}

func decodeOne(p pdef, raw json.RawMessage) (any, error) {
	switch p.Type {
	case "number", "integer":
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, rule(p.Name, RuleType)
		}
		if p.Type == "integer" && f != math.Trunc(f) {
			return nil, rule(p.Name, RuleType)
		}
		if p.Decimals >= 0 {
			f = round(f, p.Decimals)
		}
		if (p.Min != nil && f < *p.Min) || (p.Max != nil && f > *p.Max) {
			return nil, rule(p.Name, RuleRange)
		}
		if p.Type == "integer" {
			return int(f), nil
		}
		return f, nil
	case "string", "track":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, rule(p.Name, RuleType)
		}
		if s == "" || len(s) > 64 {
			return nil, rule(p.Name, RuleRange)
		}
		return s, nil
	case "enum":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, rule(p.Name, RuleType)
		}
		for _, e := range p.Enum {
			if e == s {
				return s, nil
			}
		}
		return nil, rule(p.Name, RuleRange)
	case "json":
		return json.RawMessage(bytes.TrimSpace(raw)), nil
	}
	return nil, fmt.Errorf("transform: parameter %s has unknown type %q", p.Name, p.Type)
}

// withDefaults returns the resolved map: the checked values plus every static default; Derived parameters without a value
// stay absent for resolution to fill.
func withDefaults(table []pdef, d decoded) map[string]any {
	out := map[string]any{}
	for _, p := range table {
		if v, ok := d[p.Name]; ok {
			out[p.Name] = v
			continue
		}
		if p.Default != nil && !p.Derived {
			out[p.Name] = p.Default
		}
	}
	return out
}

func marshalResolved(m map[string]any) (json.RawMessage, error) {
	// json.Marshal writes map keys sorted; canonjson re-sorts at hashing anyway.
	return json.Marshal(m)
}

// downbeatParam checks a downbeat_*_s value: a number of seconds >= 0 (rounded to a millisecond) or the string "auto".
func downbeatParam(name string, raw any) (any, error) {
	r, ok := raw.(json.RawMessage)
	if !ok {
		return nil, nil
	}
	if string(r) == `"auto"` {
		return "auto", nil
	}
	var f float64
	if err := json.Unmarshal(r, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, rule(name, RuleType)
	}
	if f < 0 {
		return nil, rule(name, RuleRange)
	}
	return round(f, 3), nil
}

// atParam checks an a_at / b_at value: {"bar": n >= 0} or {"section": "kind#ordinal"}, exactly one.
func atParam(name string, raw any) (map[string]any, error) {
	r, ok := raw.(json.RawMessage)
	if !ok {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r, &m); err != nil {
		return nil, rule(name, RuleType)
	}
	bar, hasBar := m["bar"]
	sec, hasSec := m["section"]
	for k := range m {
		if k != "bar" && k != "section" {
			return nil, rule(name+"."+k, RuleUnknownField)
		}
	}
	hasBar = hasBar && string(bytes.TrimSpace(bar)) != "null"
	hasSec = hasSec && string(bytes.TrimSpace(sec)) != "null"
	switch {
	case hasBar == hasSec:
		return nil, rule(name, RuleRequired) // exactly one of bar and section
	case hasBar:
		var f float64
		if err := json.Unmarshal(bar, &f); err != nil || f != math.Trunc(f) {
			return nil, rule(name+".bar", RuleType)
		}
		if f < 0 {
			return nil, rule(name+".bar", RuleRange)
		}
		return map[string]any{"bar": int(f)}, nil
	}
	var s string
	if err := json.Unmarshal(sec, &s); err != nil {
		return nil, rule(name+".section", RuleType)
	}
	if !sectionRE.MatchString(s) {
		return nil, rule(name+".section", RuleRange)
	}
	return map[string]any{"section": s}, nil
}

// pairValidate is the shared tail of morph and polytempo: the downbeat and tempo cross-checks of the checked values.
func pairValidate(table []pdef, raw json.RawMessage, extra func(d decoded, out map[string]any) error) (json.RawMessage, error) {
	d, err := decode(table, raw)
	if err != nil {
		return nil, err
	}
	a, _ := d.str("track_a_id")
	b, _ := d.str("track_b_id")
	if a == b {
		return nil, rule("track_b_id", RuleSameInput)
	}
	out := withDefaults(table, d)
	for _, n := range []string{"downbeat_a_s", "downbeat_b_s"} {
		if v, ok := d[n]; ok {
			dv, err := downbeatParam(n, v)
			if err != nil {
				return nil, err
			}
			out[n] = dv
		}
	}
	if extra != nil {
		if err := extra(d, out); err != nil {
			return nil, err
		}
	}
	return marshalResolved(out)
}

func validateMorph(raw json.RawMessage) (json.RawMessage, error) {
	return pairValidate(morphParams, raw, func(d decoded, out map[string]any) error {
		for _, n := range []string{"a_at", "b_at"} {
			if v, ok := d[n]; ok {
				at, err := atParam(n, v)
				if err != nil {
					return err
				}
				out[n] = at
			}
		}
		return nil
	})
}

func validatePolytempo(raw json.RawMessage) (json.RawMessage, error) {
	return pairValidate(polytempoParams, raw, nil)
}

func validateShepard(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(shepardParams, raw)
	if err != nil {
		return nil, err
	}
	_, hasBars := d["loop_bars"]
	_, hasS := d["loop_s"]
	if hasBars && hasS {
		return nil, rule("loop_s", RuleLoopSourceRequired) // exactly one loop source
	}
	out := withDefaults(shepardParams, d)
	period := out["period_s"].(float64)
	dur := out["duration_s"].(float64)
	if dur < period {
		return nil, rule("duration_s", RuleRange)
	}
	if _, ok := d["bell_sigma_octaves"]; !ok {
		out["bell_sigma_octaves"] = round(float64(out["layers"].(int))/5, 2)
	}
	return marshalResolved(out)
}

func validateGlacier(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(glacierParams, raw)
	if err != nil {
		return nil, err
	}
	return marshalResolved(withDefaults(glacierParams, d))
}

func validateEnsemble(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(ensembleParams, raw)
	if err != nil {
		return nil, err
	}
	out := withDefaults(ensembleParams, d)
	if v, present := d["dry_db"]; present && v == nil {
		out["dry_db"] = nil // minus infinity
	}
	return marshalResolved(out)
}

// ---- helpers over resolved parameters ----

// paramsMap decodes a resolved parameter object.
func paramsMap(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m) // resolved parameters are written by this package
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m
}

func paramFloat(m map[string]json.RawMessage, name string, def float64) float64 {
	var f float64
	if raw, ok := m[name]; ok && json.Unmarshal(raw, &f) == nil {
		return f
	}
	return def
}

func paramInt(m map[string]json.RawMessage, name string, def int) int {
	return int(paramFloat(m, name, float64(def)))
}

func paramString(m map[string]json.RawMessage, name string) string {
	var s string
	if raw, ok := m[name]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// fmtNum renders a number the way titles show it: 20, 20.5, 92.
func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// inputIDs returns the input Takes named by the resolved parameters, in input order, with their roles: track_a_id/track_b_id
// give roles a and b, source_id gives source. A kind that names none (score_render, T1b) has no inputs here.
func inputIDs(params json.RawMessage) []InputRef {
	m := paramsMap(params)
	var out []InputRef
	if a := paramString(m, "track_a_id"); a != "" {
		out = append(out, InputRef{Role: "a", TrackID: a})
		if b := paramString(m, "track_b_id"); b != "" {
			out = append(out, InputRef{Role: "b", TrackID: b})
		}
		return out
	}
	if s := paramString(m, "source_id"); s != "" {
		out = append(out, InputRef{Role: "source", TrackID: s})
	}
	return out
}

func init() {
	for _, k := range []KindSpec{
		{Kind: KindMorph, Validate: validateMorph, Units: unitsMorph, Title: titleMorph, Capabilities: capsMorph, MemFactor: 3, Warnings: warnPair},
		{Kind: KindShepard, Validate: validateShepard, Units: unitsShepard, Title: titleShepard, Capabilities: capsShepard, MemFactor: 1, Warnings: warnShepard},
		{Kind: KindGlacier, Validate: validateGlacier, Units: unitsGlacier, Title: titleGlacier, Capabilities: capsGlacier, MemFactor: 0},
		{Kind: KindPolytempo, Validate: validatePolytempo, Units: unitsPolytempo, Title: titlePolytempo, Capabilities: capsPolytempo, MemFactor: 3, Warnings: warnPair},
		{Kind: KindEnsemble, Validate: validateEnsemble, Units: unitsEnsemble, Title: titleEnsemble, Capabilities: capsEnsemble, MemFactor: 3, Warnings: warnEnsemble},
	} {
		RegisterKind(k)
	}
}
