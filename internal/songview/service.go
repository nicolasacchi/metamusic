// Package songview assembles the read views that need several L1 leaves at once (C90.8): get_song and the Muse
// summaries. It reads Muse text only through muses.Service.Ref/Get, never through SQL (C99.2).
package songview

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
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
}

// Coverage is the per-Muse Song count by state.
type Coverage struct{ Songs, Draft, Rendered, Judged, Picked, QueuedJobs int }

// MuseSummary is a Muse with its coverage.
type MuseSummary struct {
	Muse muses.Muse
	Coverage
}

// Service assembles views. The method set is frozen by L1-B2; the bodies are L1-B5's (STUB(L1-B5)).
type Service struct {
	st       *store.Store
	muses    *muses.Service
	songs    *songs.Service
	sessions *sessions.Service
	picks    *picks.Service
	sounds   *sounds.Service
	ladder   Ladder
	flags    Flags
	now      func() time.Time
}

// New builds the Service (C90.8).
func New(st *store.Store, m *muses.Service, so *songs.Service, se *sessions.Service, pi *picks.Service, sd *sounds.Service, l Ladder, f Flags, now func() time.Time) *Service {
	return &Service{st: st, muses: m, songs: so, sessions: se, picks: pi, sounds: sd, ladder: l, flags: f, now: now}
}

// Get assembles the view of one Song (C90.6).
func (s *Service) Get(ctx context.Context, p auth.Principal, id string, o GetOptions) (View, error) {
	return View{}, errors.ErrUnsupported // STUB(L1-B5)
}

// MuseSummaries adds the coverage counts to a page of Muses with one grouped query.
func (s *Service) MuseSummaries(ctx context.Context, ms []muses.Muse) ([]MuseSummary, error) {
	return nil, errors.ErrUnsupported // STUB(L1-B5)
}

// MuseCoverage is the coverage of one Muse.
func (s *Service) MuseCoverage(ctx context.Context, museID string) (Coverage, error) {
	return Coverage{}, errors.ErrUnsupported // STUB(L1-B5)
}
