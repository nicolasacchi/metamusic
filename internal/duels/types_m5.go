package duels

// Wire shapes of the M5 section-locked duels (C258). Frozen by M5-F2; lane M5-D1 fills the behaviour.

// SectionSelector picks the section both slots are aligned on: exactly one of Label, or Kind with Ordinal.
type SectionSelector struct {
	Label   *string `json:"label,omitempty" minLength:"1" maxLength:"60"`
	Kind    *string `json:"kind,omitempty" minLength:"1" maxLength:"30"`
	Ordinal *int    `json:"ordinal,omitempty" minimum:"1" maximum:"99"`
}

// DuelSection says which section a duel is locked to and where its timing came from.
type DuelSection struct {
	Label  string `json:"label"`
	Source string `json:"source"` // manual, measured or plan (no enum: tools/list budget)
}

// SlotWindow is one slot's window of a section-locked duel.
type SlotWindow struct {
	FromS float64 `json:"from_s"`
	ToS   float64 `json:"to_s"`
}

// SuggestQuery is the suggest_duel_pairs request (C258). K <= 0 means 6; the service caps it at 15.
type SuggestQuery struct {
	Scope       string // lineage (default) | global
	RootTrackID string // required for lineage
	Section     *SectionSelector
	K           int
}

// PairSuggestion is one suggested pair: the two takes, the expected information of one more verdict and what it is made of.
type PairSuggestion struct {
	ATrackID, BTrackID string
	ATitle, BTitle     string
	Score              float64
	PAWinsB            float64
	SDA, SDB           float64
	HumanVerdict       int
}

// PairSuggestions is the suggest_duel_pairs answer.
type PairSuggestions struct {
	Scope       string
	RootTrackID *string
	Section     *string // the canonical section label both takes are timed on, when one was asked for
	Model       SuggestModel
	Pairs       []PairSuggestion
}

// SuggestModel reports the ladder fit the suggestions rest on.
type SuggestModel struct {
	Nu              float64
	VerdictsCounted int
}
