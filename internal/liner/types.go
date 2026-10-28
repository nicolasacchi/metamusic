// Package liner renders liner notes from provenance: a pure function of an Input that recordview assembles (C124, D-L2-10).
// It imports only the standard library, so no Muse text can reach it except through the one field the app audience may carry.
package liner

// Audience says who reads the notes: the app (SPA and agents) or an export (the tape, anything that leaves the app).
type Audience string

// The two audiences.
const (
	AudienceApp    Audience = "app"
	AudienceExport Audience = "export"
)

// Input is everything Build renders from. Date is the caller's YYYY-MM-DD (nothing inside reads the clock).
type Input struct {
	Audience                    Audience
	Title, Subtitle, Note, Date string
	MuseNames                   bool // honoured for AudienceExport only (D-L2-16)
	Tracks                      []Track
}

// Track is one slot of the Record, in order.
type Track struct {
	Position                   int
	Side, Title, Angle, Engine string
	DurationS                  float64
	BPM                        *int
	BPMBasis                   string // "measured" | "declared" | "plan" | ""
	Families                   []string
	Why, Mode                  string // Why = records.WhyText of the Song's current Pick (no titles); Mode floating | pinned
	Lossy, Resampled, Upmixed  bool
	SessionLabel               string // export: "<kind> <date>" built by the caller, never a name

	// Private fields: Build drops them for the export audience by construction (C124.1).
	CostMicroUSD *int64
	SessionName  string
	Muse         *MuseLine
}

// MuseLine is the Muse text of a slot's Song; only the app audience, or the export audience with MuseNames, may print it.
type MuseLine struct {
	Title  string
	Artist *string
}

// Summary is the facts block at the top of the notes.
type Summary struct {
	Songs, Playable int
	Duration        string // m:ss or h:mm:ss
	Engines, Angles []string
	Families        []string // the top three Sound families
	ContainsLossy   bool
	LossyN          int
}

// Line is one rendered track line.
type Line struct {
	Position int
	Text     string
}

// Liner is the rendered notes before Markdown.
type Liner struct {
	Title   string
	Summary Summary
	Lines   []Line
	Notices []string
}
