package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/atlas"
)

// Wire types of the seven L3 operations (C142). JSON is snake_case. Frozen by L3-F2: a lane fills handler bodies, never
// these shapes. No Muse text appears outside NextAction.text.

// ---- features block of get_song (C142, C135) ----

// FeaturesTempo is the Song's tempo: bpm is the position value and basis says where it came from.
type FeaturesTempo struct {
	BPM          *float64 `json:"bpm" nullable:"true"`
	Basis        *string  `json:"basis" nullable:"true"`
	RequestedBPM *float64 `json:"requested_bpm" nullable:"true"`
	MeasuredBPM  *float64 `json:"measured_bpm" nullable:"true"`
	Octave       bool     `json:"octave"`
}

// FeaturesEnergy is the Song's energy in 0..1.
type FeaturesEnergy struct {
	Value *float64 `json:"value" nullable:"true"`
	Basis *string  `json:"basis" nullable:"true"`
}

// FeaturesKey is the Song's key as a Camelot code and a name.
type FeaturesKey struct {
	Camelot *string `json:"camelot" nullable:"true"`
	Name    *string `json:"name" nullable:"true"`
	Basis   *string `json:"basis" nullable:"true"`
}

// FeaturesSubject says which take or draft the features were read from.
type FeaturesSubject struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	Note *string `json:"note" nullable:"true" doc:"all_takes_discarded"`
}

// FeaturesBlock is get_song.features: null while a Song has no subject. The basis fields hold measured, declared, plan or
// predicted (no enum: get_song is a first-class tool and every enum costs tools/list bytes).
type FeaturesBlock struct {
	Tempo         FeaturesTempo   `json:"tempo"`
	Energy        FeaturesEnergy  `json:"energy"`
	Key           FeaturesKey     `json:"key"`
	DurationRatio *float64        `json:"duration_ratio" nullable:"true"`
	Subject       FeaturesSubject `json:"subject"`
	Pending       bool            `json:"pending"`
}

type featuresBlockShape FeaturesBlock

// Schema implements huma.SchemaProvider.
func (FeaturesBlock) Schema(r huma.Registry) *huma.Schema {
	return nullableSchema[featuresBlockShape](r)
}

// ---- suggest_sequence (C138) ----

// SequenceParts are the micro-costs of one transition.
type SequenceParts struct {
	Tempo  int64 `json:"tempo"`
	Key    int64 `json:"key"`
	Energy int64 `json:"energy"`
}

// SequenceTransition is how a step follows the previous one.
type SequenceTransition struct {
	Cost              int64         `json:"cost"`
	Parts             SequenceParts `json:"parts"`
	TempoDeltaPctX100 *int          `json:"tempo_delta_pct_x100" nullable:"true"`
	KeyRelation       string        `json:"key_relation"`
	EnergyDelta       *int          `json:"energy_delta" nullable:"true"`
}

// SequenceReason is one coded reason of a step.
type SequenceReason struct {
	Code   string         `json:"code"`
	Params map[string]int `json:"params"`
}

// SequenceStep is one planned position, with titles injected at read.
type SequenceStep struct {
	Position  int                 `json:"position"`
	SongID    string              `json:"song_id"`
	Title     string              `json:"title"`
	TakeTitle string              `json:"take_title,omitempty"`
	EnergyRel int                 `json:"energy_rel"`
	Target    int                 `json:"target" doc:"-1 when the strategy has no shape"`
	From      *SequenceTransition `json:"from,omitempty"`
	Reasons   []SequenceReason    `json:"reasons" nullable:"false"`
	Missing   []string            `json:"missing" nullable:"false" doc:"bpm, key or energy"`
	Text      string              `json:"text"`
}

// SequenceCost is the reported cost (J / 1e10, four decimals) and the signed improvement in percent.
type SequenceCost struct {
	Input          string  `json:"input"`
	Output         string  `json:"output"`
	ImprovementPct *string `json:"improvement_pct" nullable:"true"`
}

// SequenceCoverage is the percentage of Songs that carry each feature.
type SequenceCoverage struct {
	Tempo  int `json:"tempo"`
	Key    int `json:"key"`
	Energy int `json:"energy"`
}

// SequenceWarning is a coded caveat; only the fields a code uses are set.
type SequenceWarning struct {
	Code    string `json:"code"`
	Feature string `json:"feature,omitempty"`
	Song    string `json:"song,omitempty"`
	Known   *int   `json:"known,omitempty"`
	Total   *int   `json:"total,omitempty"`
	N       *int   `json:"n,omitempty"`
}

// Suggestion is mm.sequence/1: the preview that suggest_sequence returns and the document the sequence slot op stores.
type Suggestion struct {
	Def        string            `json:"def"`
	Strategy   string            `json:"strategy"`
	InputOrder []string          `json:"input_order" nullable:"false"`
	Order      []string          `json:"order" nullable:"false"`
	Steps      []SequenceStep    `json:"steps" nullable:"false"`
	Cost       SequenceCost      `json:"cost"`
	Coverage   SequenceCoverage  `json:"coverage"`
	Exact      bool              `json:"exact"`
	Warnings   []SequenceWarning `json:"warnings" nullable:"false"`
	Digest     string            `json:"digest"`
	Pending    bool              `json:"pending"`
}

// SequenceExplanation is get_record include=sequence: the latest run of a Record.
type SequenceExplanation struct {
	ID       string            `json:"id"`
	Strategy string            `json:"strategy"`
	At       string            `json:"at"`
	Actor    PrincipalRef      `json:"actor"`
	Stale    bool              `json:"stale" doc:"The live order differs from the run's order; the only staleness signal"`
	Exact    bool              `json:"exact"`
	Cost     SequenceCost      `json:"cost"`
	Coverage SequenceCoverage  `json:"coverage"`
	Steps    []SequenceStep    `json:"steps" nullable:"false"`
	Warnings []SequenceWarning `json:"warnings" nullable:"false"`
	Digest   string            `json:"digest"`
}

// ChangeSequence links a changelog row to the run that caused it.
type ChangeSequence struct {
	ID       string `json:"id"`
	Strategy string `json:"strategy"`
}

// SequenceSpec is the optional sequencing step of propose_record and the fields of the slot op "sequence".
type SequenceSpec struct {
	Strategy    string `json:"strategy,omitempty" doc:"arc (default), ramp_up or steady"`
	FirstSongID string `json:"first_song_id,omitempty"`
	LastSongID  string `json:"last_song_id,omitempty"`
}

type suggestSequenceIn struct {
	RecordID    string   `query:"record_id" doc:"Exactly one of record_id, song_id (2 to 60) or crate_id"`
	SongIDs     []string `query:"song_id,explode" nullable:"false" maxItems:"60"`
	CrateID     string   `query:"crate_id"`
	Strategy    string   `query:"strategy" doc:"arc (default), ramp_up or steady"`
	FirstSongID string   `query:"first_song_id"`
	LastSongID  string   `query:"last_song_id"`
}
type suggestionOut struct{ Body Suggestion }

// ---- next actions (C139) ----

// NextActionSubject is what an action is about; id is null for library and taxonomy level actions.
type NextActionSubject struct {
	Kind string  `json:"kind" enum:"muse,song,record,taxonomy,library"`
	ID   *string `json:"id" nullable:"true"`
}

// NextActionHint names a free operation and its input fields.
type NextActionHint struct {
	Op   string         `json:"op"`
	Args map[string]any `json:"args"`
}

// NextAction is one thing the library needs. Text may contain a Muse title (D-L3-16); evidence is numbers and ids.
type NextAction struct {
	Code     string            `json:"code"`
	Priority int               `json:"priority"`
	Audience string            `json:"audience" enum:"agent,human,both"`
	Subject  NextActionSubject `json:"subject"`
	Text     string            `json:"text"`
	Hint     *NextActionHint   `json:"hint,omitempty"`
	Evidence map[string]any    `json:"evidence"`
}

// NextActionGroup counts the items of a code beyond the per-code cap.
type NextActionGroup struct {
	Code string `json:"code"`
	More int    `json:"more"`
}

// NextActionsPage is the answer of list_next_actions.
type NextActionsPage struct {
	Items       []NextAction      `json:"items" nullable:"false"`
	Groups      []NextActionGroup `json:"groups" nullable:"false"`
	Total       int               `json:"total"`
	NextCursor  *string           `json:"next_cursor" nullable:"true"`
	GeneratedAt string            `json:"generated_at"`
}

type listNextActionsIn struct {
	SubjectKind string `query:"subject_kind" enum:"muse,song,record,taxonomy,library"`
	SubjectID   string `query:"subject_id"`
	MinPriority int    `query:"min_priority" minimum:"0" maximum:"100"`
	Audience    string `query:"audience" enum:"agent,human,both" doc:"Default by principal"`
	Limit       int    `query:"limit" minimum:"1" maximum:"50" default:"20"`
	Cursor      string `query:"cursor"`
}
type nextActionsOut struct{ Body NextActionsPage }

// ---- taxonomy proposals (C140) ----

// TaxonomyMapping is one term mapping of a proposal: the shipped MapEntry.
type TaxonomyMapping = MapEntry

// ProposalFacetCount is a Song count for one facet value.
type ProposalFacetCount struct {
	Facet string `json:"facet"`
	Value string `json:"value"`
	Songs int    `json:"songs"`
}

// ProposalTermCount is a Song count for one unmapped term.
type ProposalTermCount struct {
	Term  string `json:"term"`
	Songs int    `json:"songs"`
}

// TaxonomyImpact is the snapshot of what a proposal would change.
type TaxonomyImpact struct {
	TaxonomyVersion int                  `json:"taxonomy_version"`
	SongsScanned    int                  `json:"songs_scanned"`
	Truncated       bool                 `json:"truncated"`
	Gain            []ProposalFacetCount `json:"gain" nullable:"false"`
	Loss            []ProposalFacetCount `json:"loss" nullable:"false"`
	UnmappedCleared []ProposalTermCount  `json:"unmapped_cleared" nullable:"false"`
}

// TaxonomySkipped names a mapping index that a decision did not apply.
type TaxonomySkipped struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// TaxonomyProposal is an agent's suggested set of mappings; the rationale and terms are agent-authored text.
type TaxonomyProposal struct {
	ID             string            `json:"id"`
	State          string            `json:"state" enum:"open,accepted,rejected,expired"`
	BaseVersion    int               `json:"base_version"`
	Rationale      string            `json:"rationale"`
	Mappings       []TaxonomyMapping `json:"mappings" nullable:"false"`
	Impact         TaxonomyImpact    `json:"impact"`
	ProposedBy     PrincipalRef      `json:"proposed_by"`
	CreatedAt      string            `json:"created_at"`
	DecidedAt      *string           `json:"decided_at" nullable:"true"`
	DecidedBy      *PrincipalRef     `json:"decided_by,omitempty"`
	DecidedNote    *string           `json:"decided_note" nullable:"true"`
	Accepted       []int             `json:"accepted" nullable:"true"`
	Skipped        []TaxonomySkipped `json:"skipped" nullable:"true"`
	AppliedVersion *int              `json:"applied_version" nullable:"true"`
	Reused         bool              `json:"reused" doc:"An identical open proposal of the same principal was returned"`
}

// DecideResult is the outcome of decide_taxonomy_proposal.
type DecideResult struct {
	Proposal TaxonomyProposal  `json:"proposal"`
	Applied  *MapResult        `json:"applied,omitempty"`
	Skipped  []TaxonomySkipped `json:"skipped" nullable:"false"`
}

type proposeTaxonomyIn struct {
	IdemHeader
	Body struct {
		Rationale string            `json:"rationale" minLength:"1" maxLength:"500"`
		Mappings  []TaxonomyMapping `json:"mappings" minItems:"1" maxItems:"50"`
	}
}
type taxonomyProposalOut struct {
	Status int // 201 for a new proposal, 200 when an identical open one is returned (reused)
	Body   TaxonomyProposal
}

type listTaxonomyProposalsIn struct {
	State         []string `query:"state,explode" nullable:"false" enum:"open,accepted,rejected,expired"`
	IncludeImpact bool     `query:"include_impact" doc:"Recompute the impact now (at most 10 proposals per call) instead of the snapshot"`
	Limit         int      `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor        string   `query:"cursor"`
}
type taxonomyProposalsOut struct {
	Body struct {
		Items      []TaxonomyProposal `json:"items" nullable:"false"`
		NextCursor *string            `json:"next_cursor" nullable:"true"`
		Total      int                `json:"total"`
	}
}

type decideTaxonomyIn struct {
	ID   string `path:"id"`
	Body struct {
		Decision       string  `json:"decision" enum:"accept,reject"`
		AcceptMappings []int   `json:"accept_mappings,omitempty" maxItems:"50" doc:"Indices to accept; absent accepts all"`
		Note           *string `json:"note,omitempty" minLength:"1" maxLength:"300"`
	}
}
type decideResultOut struct{ Body DecideResult }

// ---- Atlas and adherence (C141) ----

type atlasIn struct {
	SongFilterIn
	X          string `query:"x" default:"tempo" doc:"Axis name; see axes in the answer"`
	Y          string `query:"y" default:"energy"`
	ColorBy    string `query:"color_by" default:"none"`
	Dim        bool   `query:"dim" doc:"Keep the whole scope and mark non-matching points h=0"`
	NearSongID string `query:"near_song_id"`
	K          int    `query:"k" minimum:"1" maximum:"25"`
	Limit      int    `query:"limit" minimum:"1" maximum:"600" doc:"Default 400 for people, 100 for agents"`
	Cursor     string `query:"cursor"`
}
type atlasOut struct{ Body atlas.Atlas }

type adherenceIn struct {
	Facet       string            `query:"facet"`
	Value       string            `query:"value"`
	MinN        int               `query:"min_n" minimum:"1" maximum:"50" default:"3"`
	MinErrorPct optParam[float64] `query:"min_error_pct" minimum:"0"`
	Octave      optParam[bool]    `query:"octave"`
	Limit       int               `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor      string            `query:"cursor"`
}
type adherenceOut struct{ Body atlas.Adherence }
