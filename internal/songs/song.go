// Package songs is the Song model (C90): one idea, however many takes were rendered from it. A live Song exists for
// every lineage root not attached to another Song and for every motherless draft not yet rendered (D-L1-3). The
// structural reconcile functions here are called by the Reconciler only (internal/provenance, C95); human edits go
// through Service.Update on disjoint columns.
package songs

import "github.com/nicolasacchi/metamusic/internal/store"

// Env is the Reconciler environment (C87).
type Env = store.Env

// State of a Song (D-L1-18), precedence picked > judged > rendered > draft.
type State string

// The four Song states.
const (
	StateDraft    State = "draft"
	StateRendered State = "rendered"
	StateJudged   State = "judged"
	StatePicked   State = "picked"
)

// Origins of a Song row.
const (
	OriginDraft = "draft"
	OriginRoot  = "root"
)

// Sources of the angle and the Muse of a Song (D-L1-37).
const (
	AngleReference = "reference"
	AngleRequestID = "request_id"
	AngleManual    = "manual"
	AngleCleared   = "cleared"
	MuseReference  = "reference"
	MuseManual     = "manual"
)

// Song is one row of songs.
type Song struct {
	ID                               string
	RootTrackID, DraftID, MuseID     *string
	Angle, AngleSource               *string
	Title, Origin                    string
	Revision                         int
	MuseSource                       *string
	CreatedBy, CreatedAt, UpdatedBy  string
	UpdatedAt                        string
	HiddenAt, RetiredAt, RetiredInto *string
}

// PickRef is the current Pick of a Song as list rows show it.
type PickRef struct{ TakeID, Source string }

// Summary is the list-row / Board-chip core.
type Summary struct {
	ID, Title                           string
	Angle, MuseID, DraftID, RootTrackID *string
	State                               State
	Takes                               int
	Pick                                *PickRef
	QueuedJobs, OpenProposals           int
	Hidden, Archived                    bool
	UpdatedAt                           string
}

// ListFilter selects Songs (C90.7).
type ListFilter struct {
	MuseID           string
	NoMuse           bool
	Angle            string
	States           []State
	Queued           *bool
	SessionID        string
	Sounds           map[string][]string // facet -> values (OR inside a facet, AND across facets)
	DraftID, TrackID string              // the single Song of a draft / of a take (any generation)
	HasOpenProposals *bool
	TempoMismatch    *bool
	Q                string
	HasPick          *bool
	IncludeHidden    bool
	IncludeArchived  bool
	Limit            int
	Cursor           string
}

// Page is one page of Songs.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

// OptString is a patch field that is unset, explicitly null, or a value.
type OptString struct {
	Set   bool
	Value *string // nil with Set = explicit null
}

// Patch is the input of Update (C90.5).
type Patch struct {
	Revision int
	Title    *string
	Angle    OptString
	MuseID   OptString
	Hidden   *bool
}

// Anomaly is a structural fault reported by a reconcile function or by Verify (C95.5). Transient anomalies re-queue
// their key instead of dequeuing it (C95.1).
type Anomaly struct {
	Code, Subject, Detail string
	Transient             bool
}

// Changes is what one reconcile call did: the Reconciler adds up the counts and collects the affected Songs.
type Changes struct {
	Created, Attached, Retired []string // Song ids
	Touched                    []string // other Songs whose structure or Muse changed (retire targets, fills)
	MusesCreated               int
	Anomalies                  []Anomaly
}

// Merge appends o to c.
func (c *Changes) Merge(o Changes) {
	c.Created = append(c.Created, o.Created...)
	c.Attached = append(c.Attached, o.Attached...)
	c.Retired = append(c.Retired, o.Retired...)
	c.Touched = append(c.Touched, o.Touched...)
	c.MusesCreated += o.MusesCreated
	c.Anomalies = append(c.Anomalies, o.Anomalies...)
}
