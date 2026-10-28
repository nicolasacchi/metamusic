package curation

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// Settings are the two opt-ins and who last changed them. UpdatedAt and UpdatedBy are empty until the first write.
type Settings struct {
	ExportMuseNames, SubsonicMuseNames bool
	UpdatedAt, UpdatedBy               string
}

// SettingsPatch changes the set fields only; an empty patch is invalid (422).
type SettingsPatch struct{ ExportMuseNames, SubsonicMuseNames *bool }

// Stats is the curation block of get_stats (C130.5): plain COUNTs, no Muse text.
type Stats struct {
	Records RecordStats `json:"records"`
	Crates  CrateStats  `json:"crates"`
	Tapes   TapeStats   `json:"tapes"`
	OptIns  OptInStats  `json:"opt_ins"`
	Pending int         `json:"pending" doc:"Slots with Pick events the changelog has not consumed yet"`
	Review  ReviewStats `json:"review"`
}

type statsShape Stats

// Schema implements huma.SchemaProvider: get_stats carries the block as null while the curation services are absent, and
// Huma refuses nullable on an object $ref, so the schema is the inline nullable one.
func (Stats) Schema(r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[statsShape]())
	cp.Nullable = true
	return &cp
}

// RecordStats counts Records by status (hidden ones included).
type RecordStats struct {
	Total     int `json:"total"`
	Draft     int `json:"draft"`
	Proposed  int `json:"proposed"`
	Published int `json:"published"`
}

// CrateStats counts Crates by kind.
type CrateStats struct {
	Manual int `json:"manual"`
	Smart  int `json:"smart"`
}

// TapeStats counts tapes by state.
type TapeStats struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Ready   int `json:"ready"`
	Failed  int `json:"failed"`
}

// OptInStats repeats the two flags so the operator sees what is on.
type OptInStats struct {
	ExportMuseNames   bool `json:"export_muse_names"`
	SubsonicMuseNames bool `json:"subsonic_muse_names"`
}

// ReviewStats counts live slots of published Records that entered without a human look: an undecided Pick (newest or stars)
// or a pin whose Take was discarded.
type ReviewStats struct {
	Undecided     int `json:"undecided"`
	PinIneligible int `json:"pin_ineligible"`
}
