// Package songview assembles the read views that need several L1 leaves at once (C90.8): get_song and the Muse
// summaries. It reads Muse text only through muses.Service.Ref/Get, never through SQL (C99.2).
package songview

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Ladder is the ladder source (picks.Ladder's method set); nil yields null ladder fields, never an error.
type Ladder interface {
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// Flags reads Ears flag codes in one read-only batch, starting no analysis; nil yields empty flags.
type Flags interface {
	FlagCodes(ctx context.Context, ids []string) (map[string][]string, error)
}

// GetOptions are the options of Get (C90.6): By is song (default), draft or track; TakeLimit 1..100 (24);
// IncludeEvents 0..50 (10).
type GetOptions struct {
	By            string
	TakeLimit     int
	IncludeEvents int
}

// SongCore is the identity block of a View.
type SongCore struct {
	ID, Title            string
	Angle, AngleSource   *string
	Origin               string
	Revision             int
	State                songs.State
	Hidden, Archived     bool
	QueuedJobs           int
	CreatedAt, UpdatedAt string
	RootTrackID, DraftID *string
	RootTrackIDs         []string
	Retired              bool
	RetiredInto          *string
}

// DraftBlock is the draft of an unrendered Song.
type DraftBlock struct {
	ID, Status, Label, RequestID string
	Revision                     int
	GroupKey                     *string
}

// TakeCard is one take of a Song (C90.6).
type TakeCard struct {
	ID, Title, Status         string
	Generation                int
	ParentTrackID             *string
	Origin                    string
	DurationS                 float64
	RecordedAt                string
	Lossy, RecipeKnown        bool
	Stars, CriticStars        *int
	IsPick, Pinned, HasEars   bool
	Flags                     []string
	Ladder                    *ladder.Strength
	RequestedBPM, MeasuredBPM *float64
	CostDisplayUSD            *string
}

// SoundsBlock is the Sound facets of a Song.
type SoundsBlock struct {
	TaxonomyVersion int
	Facets          map[string][]FacetEntry
	Tempo           *sounds.TempoDetail
	Unmapped        []string
}

// FacetEntry is one facet value with its origin.
type FacetEntry struct{ Value, Origin string }

// SessionRef names a Session a Song belongs to.
type SessionRef struct{ ID, Kind, Name string }

// LineageBlock summarises the lineage of the primary root.
type LineageBlock struct {
	RootTrackID   string
	Generations   int
	ChildrenTotal int
}

// View is the get_song assembly (C90.6).
type View struct {
	Song       SongCore
	Muse       *muses.MuseRef
	Draft      *DraftBlock
	Takes      []TakeCard
	TakesTotal int
	Pick       *picks.PickView
	Proposals  []picks.Proposal
	Events     []picks.PickEvent
	Sounds     SoundsBlock
	Sessions   []SessionRef
	Lineage    *LineageBlock
	Pending    bool
	// L2 (C130.3): the Records and manual Crates that hold the Song, at most MaxCurationRefs each; empty without UseCuration.
	Records []RecordRef
	Crates  []CrateRef
}

// MaxCurationRefs bounds get_song.records and get_song.crates.
const MaxCurationRefs = 10

// RecordRef is a non-hidden Record with a live slot of the Song.
type RecordRef struct{ ID, Title, Status, Mode string }

// CrateRef is a manual Crate holding the Song.
type CrateRef struct{ ID, Name string }

// Curation reads the L2 memberships of a Song. songview declares it so it imports neither records nor crates (C118); app.Build
// adapts the tables to it. RecordsOf lists newest first, CratesOf includes the Crates that hold a Song retired into this one.
type Curation interface {
	RecordsOf(ctx context.Context, songID string, limit int) ([]RecordRef, error)
	CratesOf(ctx context.Context, songID string, limit int) ([]CrateRef, error)
}

// Coverage is the per-Muse Song count by state.
type Coverage struct{ Songs, Draft, Rendered, Judged, Picked, QueuedJobs int }

// MuseSummary is a Muse with its coverage.
type MuseSummary struct {
	Muse muses.Muse
	Coverage
}

// Service assembles views (C90.8).
type Service struct {
	st       *store.Store
	muses    *muses.Service
	songs    *songs.Service
	sessions *sessions.Service
	picks    *picks.Service
	sounds   *sounds.Service
	ladder   Ladder
	flags    Flags
	curation Curation // L2 (C130.3); nil => get_song.records and .crates are empty
	now      func() time.Time
}

// UseCuration wires the L2 membership reader. A nil reader leaves records and crates empty.
func (s *Service) UseCuration(c Curation) { s.curation = c }

// New builds the Service (C90.8).
func New(st *store.Store, m *muses.Service, so *songs.Service, se *sessions.Service, pi *picks.Service, sd *sounds.Service, l Ladder, f Flags, now func() time.Time) *Service {
	return &Service{st: st, muses: m, songs: so, sessions: se, picks: pi, sounds: sd, ladder: l, flags: f, now: now}
}
