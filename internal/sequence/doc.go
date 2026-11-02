package sequence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// Planned is one planner run over loaded features: the input in order, the planner result and the document that describes it.
type Planned struct {
	Refs     []Ref
	Songs    []sequencer.Song
	Strategy string
	Result   sequencer.Result
	Doc      Document
}

// Prepare checks a sequence request, loads the features of refs (in the order given, which is the cost-input order) and runs
// the planner. An empty strategy means arc. It is the one code path of suggest_sequence and of the Record op, which is
// what makes TestSuggest_EqualsApply hold by construction. Errors are *problem.Error (422).
func Prepare(ctx context.Context, q Querier, refs []Ref, strategy, first, last string) (*Planned, error) {
	if len(refs) < 2 {
		return nil, tooSmall(len(refs))
	}
	if len(refs) > batchLimit {
		return nil, tooMany(len(refs))
	}
	if strategy == "" {
		strategy = string(sequencer.Strategies[0])
	}
	if _, ok := sequencer.SpecOf(sequencer.Strategy(strategy)); !ok {
		return nil, unknownStrategy(strategy)
	}
	in := map[string]bool{}
	for _, r := range refs {
		in[r.SongID] = true
	}
	if first != "" && !in[first] {
		return nil, endpointNotInSet("first_song_id", first)
	}
	if last != "" && !in[last] {
		return nil, endpointNotInSet("last_song_id", last)
	}
	songs, _, err := LoadFeatures(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	if noFeatures(songs) {
		return nil, problem.E(422, CodeNoFeatures, "none of the songs has a tempo, key or energy to sequence by", map[string]any{"songs": len(songs)})
	}
	res, err := sequencer.Plan(songs, sequencer.Options{Strategy: sequencer.Strategy(strategy), First: first, Last: last})
	switch {
	case errors.Is(err, sequencer.ErrTooMany):
		return nil, tooMany(len(refs))
	case errors.Is(err, sequencer.ErrUnknownStrategy):
		return nil, unknownStrategy(strategy)
	case errors.Is(err, sequencer.ErrEndpointMissing):
		return nil, endpointNotInSet("first_song_id", first)
	case errors.Is(err, sequencer.ErrEndpointsConflict):
		return nil, problem.E(422, CodeEndpointsSame, "first_song_id and last_song_id name the same song", map[string]any{"song_id": first})
	case err != nil:
		return nil, fmt.Errorf("sequence: plan: %w", err)
	}
	p := &Planned{Refs: refs, Songs: songs, Strategy: strategy, Result: res}
	p.Doc = NewDocument(strategy, refs, res)
	return p, nil
}

func noFeatures(songs []sequencer.Song) bool {
	for _, s := range songs {
		if s.BPMx100 > 0 || s.Energyx10000 >= 0 || s.Key != nil {
			return false
		}
	}
	return true
}

// Document is mm.sequence/1 as stored in record_sequences.explanation_json and as served: ids, integers and codes only (no
// titles, no Muse text; C138.6). Titles are added to the steps at read.
type Document struct {
	Def        string       `json:"def"`
	Strategy   string       `json:"strategy"`
	InputOrder []string     `json:"input_order"`
	Order      []string     `json:"order"`
	Steps      []DocStep    `json:"steps"`
	Cost       DocCost      `json:"cost"`
	Coverage   DocCoverage  `json:"coverage"`
	Exact      bool         `json:"exact"`
	Warnings   []DocWarning `json:"warnings"`
	Digest     string       `json:"digest"`
}

// DocStep is one planned position; TakeID is the Take whose features counted (absent: the Song's subject).
type DocStep struct {
	Position  int            `json:"position"`
	SongID    string         `json:"song_id"`
	TakeID    *string        `json:"take_id,omitempty"`
	EnergyRel int            `json:"energy_rel"`
	Target    int            `json:"target"`
	From      *DocTransition `json:"from,omitempty"`
	Reasons   []DocReason    `json:"reasons"`
	Missing   []string       `json:"missing"`
	Text      string         `json:"text"`
}

// DocTransition is how a step follows the previous one.
type DocTransition struct {
	Cost              int64    `json:"cost"`
	Parts             DocParts `json:"parts"`
	TempoDeltaPctX100 *int     `json:"tempo_delta_pct_x100"`
	KeyRelation       string   `json:"key_relation"`
	EnergyDelta       *int     `json:"energy_delta"`
}

// DocParts are the micro-costs of one transition.
type DocParts struct {
	Tempo  int64 `json:"tempo"`
	Key    int64 `json:"key"`
	Energy int64 `json:"energy"`
}

// DocReason is one coded reason; params are integers.
type DocReason struct {
	Code   string         `json:"code"`
	Params map[string]int `json:"params"`
}

// DocCost is the reported cost (J / 1e10, four decimals) and the signed improvement in percent (null when the input costs 0).
type DocCost struct {
	Input          string  `json:"input"`
	Output         string  `json:"output"`
	ImprovementPct *string `json:"improvement_pct"`
}

// DocCoverage is the percentage of Songs that carry each feature.
type DocCoverage struct {
	Tempo  int `json:"tempo"`
	Key    int `json:"key"`
	Energy int `json:"energy"`
}

// DocWarning is a coded caveat; only the fields a code uses are set.
type DocWarning struct {
	Code    string `json:"code"`
	Feature string `json:"feature,omitempty"`
	Song    string `json:"song,omitempty"`
	Known   *int   `json:"known,omitempty"`
	Total   *int   `json:"total,omitempty"`
	N       *int   `json:"n,omitempty"`
}

// FormatCost renders J (units of 1e-10) as a decimal with four places, rounded half up: 55 198 370 000 is "5.5198".
func FormatCost(j int64) string {
	t := (j + 500_000) / 1_000_000 // ten-thousandths
	return fmt.Sprintf("%d.%04d", t/10_000, t%10_000)
}

// FormatPctX10 renders signed tenths of a percent: 386 is "38.6", -25 is "-2.5".
func FormatPctX10(v int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%d", sign, v/10, v%10)
}

func intp(n int) *int { return &n }

// NewDocument builds the document of a planner result. refs are the input Refs (their Takes label the steps).
func NewDocument(strategy string, refs []Ref, res sequencer.Result) Document {
	take := make(map[string]*string, len(refs))
	input := make([]string, len(refs))
	for i, r := range refs {
		take[r.SongID] = r.TakeID
		input[i] = r.SongID
	}
	d := Document{Def: Def, Strategy: strategy, InputOrder: input, Order: nonNil(res.Order), Exact: res.Exact, Digest: res.Digest,
		Coverage: DocCoverage{Tempo: res.Coverage.Tempo, Key: res.Coverage.Key, Energy: res.Coverage.Energy},
		Cost:     DocCost{Input: FormatCost(res.CostInput), Output: FormatCost(res.CostOutput)},
		Steps:    make([]DocStep, 0, len(res.Steps)), Warnings: make([]DocWarning, 0, len(res.Warnings))}
	if res.ImprovementPctX10 != nil {
		s := FormatPctX10(*res.ImprovementPctX10)
		d.Cost.ImprovementPct = &s
	}
	for _, st := range res.Steps {
		ds := DocStep{Position: st.Position, SongID: st.SongID, TakeID: take[st.SongID], EnergyRel: st.EnergyRel, Target: st.Target,
			Reasons: make([]DocReason, 0, len(st.Reasons)), Missing: nonNil(st.Missing), Text: sequencer.ExplainText(st, sequencer.Strategy(strategy))}
		if st.From != nil {
			ds.From = &DocTransition{Cost: st.From.Cost, Parts: DocParts{Tempo: st.From.Parts.Tempo, Key: st.From.Parts.Key, Energy: st.From.Parts.Energy},
				TempoDeltaPctX100: st.From.TempoDeltaPctX100, KeyRelation: st.From.KeyRelation, EnergyDelta: st.From.EnergyDelta}
		}
		for _, r := range st.Reasons {
			p := r.Params
			if p == nil {
				p = map[string]int{}
			}
			ds.Reasons = append(ds.Reasons, DocReason{Code: r.Code, Params: p})
		}
		d.Steps = append(d.Steps, ds)
	}
	for _, w := range res.Warnings {
		d.Warnings = append(d.Warnings, docWarning(w))
	}
	return d
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// docWarning keeps only the fields a code uses (C137.5).
func docWarning(w sequencer.Warning) DocWarning {
	d := DocWarning{Code: w.Code}
	switch w.Code {
	case "low_coverage":
		d.Feature, d.Known, d.Total = w.Feature, intp(w.Known), intp(w.Total)
	case "predicted_energy":
		d.N = intp(w.N)
	case "no_features":
		d.Song = w.Song
	}
	return d
}

// JSON is the stored form of the document.
func (d Document) JSON() []byte {
	b, err := json.Marshal(d)
	if err != nil { // plain ints, strings and maps: cannot fail
		panic(fmt.Sprintf("sequence: marshal document: %v", err))
	}
	return b
}
