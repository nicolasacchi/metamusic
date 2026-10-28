package crates

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Kind says how a Crate is filled: by explicit items or by a saved filter (D-L2-12).
type Kind string

// The two kinds.
const (
	KindManual Kind = "manual"
	KindSmart  Kind = "smart"
)

// Subject says what a Crate holds, fixed at creation; a Crate never mixes subjects.
type Subject string

// The two subjects.
const (
	SubjectSong Subject = "song"
	SubjectTake Subject = "take"
)

// Crate is one row of crates.
type Crate struct {
	ID, Name, Description string
	Kind                  Kind
	Subject               Subject
	Filter                json.RawMessage // smart Crates only
	Revision              int
	CreatedBy, CreatedAt  string
	UpdatedBy, UpdatedAt  string
	HiddenAt              *string
}

// Member is one item of a manual Crate: exactly one of SongID and TrackID is set, matching the Crate's subject.
type Member struct{ SongID, TrackID string }

// NewCrate is the input of Create.
type NewCrate struct {
	Name, Description string
	Kind              Kind
	Subject           Subject
	Filter            json.RawMessage
	Items             []Member
}

// Patch is the input of Update; Filter is for smart Crates only.
type Patch struct {
	Revision          int
	Name, Description *string
	Filter            json.RawMessage
	Hidden            *bool
}

// ItemsEdit adds and removes members of a manual Crate (each at most MaxEditBatch per call).
type ItemsEdit struct{ Add, Remove []Member }

// ItemsResult counts what an edit did.
type ItemsResult struct{ Added, Already, Removed, NotMember, Count int }

// ListFilter selects Crates; hidden ones are excluded unless IncludeHidden, and smart counts are computed only with Counts.
type ListFilter struct {
	Kind          Kind
	Subject       Subject
	Q             string
	IncludeHidden bool
	Counts        bool
	Limit         int
	Cursor        string
}

// Summary is one list row: a Crate and its count (manual always, smart only with ListFilter.Counts).
type Summary struct {
	Crate
	Count *int
}

// Page is one page of Summaries.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

// Counter counts what a saved filter matches now, through the same filter structs the list operations use, so a Crate view
// and an agent's ad-hoc query can never disagree (D-L2-13). app.Build adapts songs.Service and library.Service to it.
type Counter interface {
	CountSongs(ctx context.Context, f songs.ListFilter) (int, error)
	CountTracks(ctx context.Context, p library.SearchParams) (int, error)
}

// Filter is a parsed smart filter: ParseFilter validates it against the vocabulary, Songs and Tracks map it onto the Go
// filter structs of the list operations.
type Filter struct {
	subject Subject
	songs   songs.ListFilter
	tracks  library.SearchParams
}

// Service is the Crates service. The method set is frozen by F2; lane K replaces stub_k.go.
type Service struct {
	st    *store.Store
	lib   Counter
	now   func() time.Time
	newID func() string
}

// New builds the service. A nil now means time.Now.
func New(st *store.Store, lib Counter, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

// Subject is the subject the filter was parsed for.
func (f Filter) Subject() Subject { return f.subject }
