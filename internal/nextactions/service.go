// Package nextactions is the rule engine behind list_next_actions and the /context next_actions strings (L3 C139). It is
// the one new reader of Muse text (C133, C144.2). Types and the method set are frozen by L3-F2; the engine is lane NA1
// (engine.go) and the rules are NA1 (rules_a.go), NA2 (rules_b.go) and NA3 (rules_c.go).
package nextactions

import (
	"context"
	"database/sql"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Audience says who an action is for.
type Audience string

// The audiences.
const (
	AudienceAgent Audience = "agent"
	AudienceHuman Audience = "human"
	AudienceBoth  Audience = "both"
)

// Subject kinds.
const (
	SubjectMuse     = "muse"
	SubjectSong     = "song"
	SubjectRecord   = "record"
	SubjectTaxonomy = "taxonomy"
	SubjectLibrary  = "library"
)

// Subject is what an action is about; ID is nil for library and taxonomy level actions.
type Subject struct {
	Kind string
	ID   *string
}

// Hint names a registry operation with cost free and its input fields (path parameters by name).
type Hint struct {
	Op   string
	Args map[string]any
}

// Action is one next action. Evidence holds numbers and ids only.
type Action struct {
	Code     string
	Priority int // 0..100
	Audience Audience
	Subject  Subject
	Text     string
	Hint     *Hint
	Evidence map[string]any
}

// Candidate is what a rule's Find returns for one subject.
type Candidate struct {
	Subject  Subject
	Evidence map[string]any
}

// Reader is the read side a rule's Find queries through.
type Reader interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Names resolves display names for rule texts, once per call.
type Names interface {
	Muse(id string) string
	Song(id string) string
	Record(id string) string
}

// Rule is one entry of the registry (extension move 3). Find answers with at most lim candidates ordered by subject id (nil
// last) in one batch statement; the rows after the cut are only counted, by an evidence key "_total" that holds the number of
// candidates before the cut (see the evidence conventions in engine.go). Hint is optional and sees the viewer, so it can pick an
// operation the viewer's scope allows.
type Rule struct {
	Code     string
	Priority int
	Audience Audience
	MinScope auth.Scope
	Find     func(ctx context.Context, q Reader, lim int) ([]Candidate, error)
	Text     func(c Candidate, n Names) string
	Hint     func(c Candidate, viewer auth.Principal) *Hint
}

// Filter selects and pages the actions of List.
type Filter struct {
	SubjectKind, SubjectID string
	MinPriority, Limit     int
	Cursor                 string
	Audience               Audience
}

// Group counts the items beyond PerCode for one code.
type Group struct {
	Code string
	More int
}

// Page is a page of actions.
type Page struct {
	Items       []Action
	Groups      []Group
	Total       int
	NextCursor  *string
	GeneratedAt string
}

// MuseRefs is the Muse read of nextactions, only through muses.Service: Refs for titles (not called until D-L3-16 is approved:
// item texts name a Muse by an id prefix) and LiveIDs, the non-hidden Muse ids that live-Muse rules receive as a parameter.
type MuseRefs interface {
	Refs(ctx context.Context, ids []string) (map[string]muses.MuseRef, error)
	LiveIDs(ctx context.Context) ([]string, error)
}

// SongSummaries is the Muse coverage read of songview.
type SongSummaries interface {
	MuseCoverage(ctx context.Context, museID string) (songview.Coverage, error)
	MuseAngles(ctx context.Context, museID string) ([]songview.AngleCount, error)
}

// Unmapped reads the unmapped style terms of the taxonomy.
type Unmapped interface {
	Taxonomy(ctx context.Context, q sounds.TaxonomyQuery) (sounds.TaxonomyPage, error)
}

// Service evaluates the rules.
type Service struct {
	st       *store.Store
	muses    MuseRefs
	songview SongSummaries
	sounds   Unmapped
	now      func() time.Time
	hook     func(query string) // tests: sees every statement of a call
}

// New builds the service. A nil now means time.Now.
func New(st *store.Store, m MuseRefs, sv SongSummaries, sd Unmapped, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, muses: m, songview: sv, sounds: sd, now: now}
}
