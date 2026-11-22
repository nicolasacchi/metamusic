package transform

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// The registration of the score_render kind (T1b, C355.1). The parameter table, validation, unit model, title and capabilities
// live here; the pipeline (parse, SMF, fluidsynth, child) is score_render.go and the operator itself is ops.KindScoreRender.

// The score_render parameter limits (C355.1).
const (
	scoreBPMMin, scoreBPMMax   = 20.0, 300.0
	scoreTailDefault           = 3.0
	scoreTailMax               = 10.0
	scoreGainMin, scoreGainMax = 0.1, 1.5
	scoreGainDefault           = 0.6
)

// The dialects and chord modes of a score render, as the spec spells them.
const (
	dialectStandard = "standard"
	dialectYuE2     = "yue2"
	chordsNone      = "none"
	chordsBlock     = "block"
)

var soundfontNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// scoreParams is the C355.1 table. dialect and soundfont depend on the request and the host (the source take's engine, the
// configured fonts), so they are Derived: the capabilities row shows no static default and resolution writes them into the spec.
var scoreParams = []pdef{
	{Name: "score", Type: "json", Required: true, Decimals: -1},
	{Name: "dialect", Type: "enum", Enum: []string{dialectStandard, dialectYuE2}, Decimals: -1, Derived: true},
	{Name: "soundfont", Type: "string", Decimals: -1, Derived: true},
	{Name: "bpm", Type: "number", Min: fp(scoreBPMMin), Max: fp(scoreBPMMax), Decimals: 2},
	{Name: "tail_s", Type: "number", Default: scoreTailDefault, Min: fp(0), Max: fp(scoreTailMax), Decimals: 3},
	{Name: "gain", Type: "number", Default: scoreGainDefault, Min: fp(scoreGainMin), Max: fp(scoreGainMax), Decimals: 2},
	{Name: "chords", Type: "enum", Default: chordsNone, Enum: []string{chordsNone, chordsBlock}, Decimals: -1},
}

// scoreSourceOf checks the score member: exactly one of abc (non-empty text up to MaxScoreBytes) or from_track_id.
func scoreSourceOf(raw any) (map[string]any, error) {
	r, ok := raw.(json.RawMessage)
	if !ok {
		return nil, rule("score", RuleRequired)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r, &m); err != nil {
		return nil, rule("score", RuleType)
	}
	for k := range m {
		if k != "abc" && k != "from_track_id" {
			return nil, rule("score."+k, RuleUnknownField)
		}
	}
	present := func(k string) bool { v, ok := m[k]; return ok && string(bytes.TrimSpace(v)) != "null" }
	hasABC, hasTrack := present("abc"), present("from_track_id")
	if hasABC == hasTrack {
		return nil, rule("score", RuleRequired) // exactly one
	}
	if hasABC {
		var s string
		if err := json.Unmarshal(m["abc"], &s); err != nil {
			return nil, rule("score.abc", RuleType)
		}
		if strings.TrimSpace(s) == "" {
			return nil, rule("score.abc", RuleRequired)
		}
		if len(s) > MaxScoreBytes {
			return nil, rule("score.abc", RuleScoreTooLarge)
		}
		return map[string]any{"abc": s}, nil
	}
	var s string
	if err := json.Unmarshal(m["from_track_id"], &s); err != nil {
		return nil, rule("score.from_track_id", RuleType)
	}
	if s == "" || len(s) > 64 {
		return nil, rule("score.from_track_id", RuleRange)
	}
	return map[string]any{"from_track_id": s}, nil
}

func validateScore(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(scoreParams, raw)
	if err != nil {
		return nil, err
	}
	src, err := scoreSourceOf(d["score"])
	if err != nil {
		return nil, err
	}
	if sf, ok := d.str("soundfont"); ok && !soundfontNameRE.MatchString(sf) {
		return nil, rule("soundfont", RuleRange) // a name, never a path
	}
	out := withDefaults(scoreParams, d)
	out["score"] = src
	return marshalResolved(out)
}

// titleScore is the T: line of an inline score, else "Score render".
func titleScore(in TitleIn) string {
	var p struct {
		Score struct {
			ABC string `json:"abc"`
		} `json:"score"`
	}
	_ = json.Unmarshal(in.Resolved, &p)
	for _, line := range strings.Split(p.Score.ABC, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "T:"); ok {
			if t = strings.TrimSpace(t); t != "" {
				return t
			}
		}
	}
	return "Score render"
}

func capsScore() KindCapabilities {
	return KindCapabilities{
		Kind:    KindScoreRender,
		Summary: "A deterministic render of an ABC score with a General MIDI soundfont: the notes as written, no generation.",
		Inputs:  0,
		Params:  paramSpecs(scoreParams),
		BlindSpots: []string{
			"the render is exactly the score: no swing, no expression and no lyrics, and the instruments are those of the soundfont",
			"a score from a YuE2 plan is read with the yue2 accidental rule; the default dialect follows the source take",
			"chord symbols are silent unless chords is block, which adds a plain close-position chord voice",
			"the result is reproducible for the same score, soundfont and fluidsynth version",
		},
		Example: `{"kind":"score_render","score_render":{"score":{"abc":"X:1\nT:Example\nM:4/4\nL:1/4\nQ:1/4=100\nK:C\nCDEF|G4|"},"tail_s":3}}`,
	}
}

func init() {
	RegisterKind(KindSpec{Kind: KindScoreRender, Validate: validateScore, Units: opsUnits(KindScoreRender), Title: titleScore, Capabilities: capsScore, MemFactor: 1})
}
