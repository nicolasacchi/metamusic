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
