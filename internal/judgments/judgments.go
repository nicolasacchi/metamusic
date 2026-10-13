// Package judgments owns ratings, track status and title, tags and comments
// (contract C31). M2a-B4a freezes the signatures; M2a-B4b implements them.
package judgments

import (
	"errors"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Shapes shared with the library views.
type (
	TrackRating = library.TrackRating
	RatingRow   = library.RatingRow
	TrackTags   = library.TrackTags
	TagRef      = library.TagRef
)

// ErrInvalidTransition is returned by Transition for a change update_comment may not make.
var ErrInvalidTransition = errors.New("judgments: invalid comment transition")

// TagCount is one tag with its track counts per source.
type TagCount struct {
	Name     string         `json:"name"`
	Tracks   int            `json:"tracks"`
	BySource TagSourceCount `json:"by_source"`
}

// TagSourceCount splits TagCount.Tracks by tag source.
type TagSourceCount struct {
	Human     int `json:"human"`
	Agent     int `json:"agent"`
	Inherited int `json:"inherited"`
}

// SectionRef is the stored section label of a comment (D-M2a-16).
type SectionRef struct {
	Label      string  `json:"label"`
	Source     string  `json:"source" enum:"manual,measured,plan"`
	Confidence float64 `json:"confidence"`
}

// Comment is one comment on a track.
type Comment struct {
	ID             string            `json:"id"`
	TrackID        string            `json:"track_id"`
	Body           string            `json:"body"`
	AtSecond       *float64          `json:"at_second"`
	Link           string            `json:"link" doc:"Relative SPA link, /tracks/<id>?t=<s>; empty without at_second"`
	URL            string            `json:"url" doc:"Absolute link"`
	Section        *SectionRef       `json:"section" doc:"Section label; null when no timed section covers at_second"`
	Kind           string            `json:"kind" enum:"note,fix,keep,marker"`
	State          string            `json:"state" enum:"open,addressed,resolved,regressed"`
	Source         string            `json:"source" enum:"web,api,mcp,stdio"`
	Author         auth.PrincipalRef `json:"author"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
	EditedAt       *string           `json:"edited_at"`
	StateChangedAt *string           `json:"state_changed_at"`
	Hidden         bool              `json:"hidden"`
}

// NewComment is the input of AddComment.
type NewComment struct {
	Body     string
	AtSecond *float64
	Kind     string
}

// CommentPatch is the input of UpdateComment.
type CommentPatch struct {
	Body, Kind, State *string
	Hidden            *bool
}

// CommentFilter selects comments.
type CommentFilter struct {
	State, Kind   []string
	IncludeHidden bool
	Order         string // newest (default) | time
	Limit         int
	Cursor        string
}

// CommentPage is one page of comments.
type CommentPage struct {
	Items      []Comment `json:"items" nullable:"false"`
	Total      int       `json:"total"`
	NextCursor *string   `json:"next_cursor"`
}

// Error is a judgments failure with a stable problem code; the API layer maps
// it to application/problem+json. Plain input errors are
// *library.ValidationError.
type Error struct {
	Status int
	Code   string
	Msg    string
	Field  string
	cause  error
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }
func (e *Error) Unwrap() error { return e.cause }

// Service implements C31.
type Service struct {
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string
}

// New builds the judgments service; nil now means time.Now.
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

// nullableObject renders T (a method-less shadow of the real type) as an inline
// object schema that may be null: Huma refuses nullable fields that point at a
// $ref object (see library/schema.go).
func nullableObject[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

type sectionRefShape SectionRef

// Schema implements huma.SchemaProvider: a comment's section may be null.
func (SectionRef) Schema(r huma.Registry) *huma.Schema { return nullableObject[sectionRefShape](r) }
