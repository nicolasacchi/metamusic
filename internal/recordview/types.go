// Package recordview assembles the read side of a Record: slots with their resolved Takes, totals and warnings, the cover,
// the app-audience liner notes, the energy curve, the changelog with titles, the tapes, the publish preview, and the tape
// snapshot (C124.2). It is the only L2 package allowed to read Muse text, and only through muses.Service.Refs (C118, D-L2-16).
//
// The View types here are plain Go; the API's wire types (internal/api/types_l2.go) mirror them and the Records operations
// map one to the other, rendering principals for the viewer with api.RefFor.
package recordview

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// Include names the optional parts of a View (GetOptions.Include).
const (
	IncludeLiner   = "liner"
	IncludeEnergy  = "energy"
	IncludeTapes   = "tapes"
	IncludeChanges = "changes"
	IncludePreview = "preview"
)

// Includes lists the valid Include values.
var Includes = []string{IncludeLiner, IncludeEnergy, IncludeTapes, IncludeChanges, IncludePreview}

// GetOptions tunes Get. An empty Include returns none of the optional parts: the five mutating operations call Get that
// way, so they carry no liner and no Muse text (D-L2-27); only get_record ever passes IncludeLiner.
type GetOptions struct {
	Include      []string
	ChangesLimit int // 0..50, default 10 when IncludeChanges is set
}

// View is one Record as the operations show it (C124.2, C129).
type View struct {
	Record   records.Record
	Slots    []SlotView
	Totals   Totals
	Warnings []Warning
	Cover    CoverRef
	Liner    *LinerView   // IncludeLiner only (Muse text)
	Energy   *Energy      // IncludeEnergy only
	Changes  []ChangeView // IncludeChanges only
	Tapes    []tape.Brief // IncludeTapes only
	Preview  *Preview     // IncludePreview only
	Pending  bool         // the changelog may lag (C122.6); the resolved Takes are never stale
}

// Totals are the counts at the top of a View.
type Totals struct {
	Slots, Playable int
	DurationS       float64
	Lossy           int
	Warnings        int
	Undecided       int
}

// Warning is a warning of the Record; SlotID is nil for a Record-level one.
type Warning struct {
	SlotID *string
	Code   string
}

// CoverRef is a pair of signed cover links.
type CoverRef struct {
	URL512, URL1024, ExpiresAt string
}

// SongRef is a slot's Song in a View. No Muse text.
type SongRef struct {
	ID, Title   string
	Angle       *string
	State       string
	Retired     bool
	RetiredInto *string
}

// TakeBrief is a Take in a View.
type TakeBrief struct {
	ID, Title, Status string
	DurationS         float64
	Lossy             bool
	Stars             *int // human stars
	RecipeKnown       bool
}

// PickRef is the Song's current Pick row.
type PickRef struct {
	TakeID *string
	Source string
}

// SlotView is one slot with everything the studio shows (C129).
type SlotView struct {
	ID         string
	Position   int
	Side       *string
	Mode       records.SlotMode
	State      records.SlotState
	Song       SongRef
	Take       *TakeBrief
	Pick       *PickRef
	PinDiffers bool
	GainDB     *float64 // the plan gain with the automatic target; nil when unmeasured
	Why        *string  // records.WhyText of the Song's current song_picks row
	Warnings   []string
}

// LinerView is the generated app-audience liner (Muse text: get_record only, C127.4).
type LinerView struct {
	Markdown string
	Summary  json.RawMessage // the liner.Summary as JSON, so the API maps it without importing liner
}

// Energy is the energy-curve preview (C124.3).
type Energy struct {
	TargetLUFS     float64
	TotalDurationS float64
	Slots          []EnergySlot
}

// EnergySlot is one slot's curve: LUFS is the stored short-term series after the plan gain, at most 120 points.
type EnergySlot struct {
	SlotID    string
	DurationS float64
	GainDB    *float64
	MeanLUFS  *float64
	BPM       *float64
	LUFS      []float64
}

// TakeRef is a Take in a ChangeView, with its title at read time (titles are never stored).
type TakeRef struct{ ID, Title string }

// ChangeView is one changelog row with the splice of titles and its text (C122.3).
type ChangeView struct {
	Change records.Change
	From   *TakeRef
	To     *TakeRef
	Text   string
}

// PreviewChapter is one chapter name as the tape would carry it.
type PreviewChapter struct {
	Position int
	Name     string
}

// PreviewTag is one tag as the tape would carry it.
type PreviewTag struct{ Key, Value string }

// Preview is exactly what Snapshot would build for the tape, without writing anything (D-L2-27 b).
type Preview struct {
	Chapters    []PreviewChapter
	Tags        []PreviewTag
	Description string
	Warnings    []tape.SlotWarning
}
