// Package sessions is the Session model (C92): auto-created, renameable, hideable groupings of drafts and takes by
// import run, draft collection or generation batch (D-L1-13). Sessions are never required and never deleted.
package sessions

import "github.com/nicolasacchi/metamusic/internal/store"

// Env is the Reconciler environment (C87).
type Env = store.Env

// Kinds of a Session.
const (
	KindImport = "import"
	KindUpload = "upload"
	KindBatch  = "batch"
	KindNight  = "night" // reserved: no L1 writer
)

// Member kinds.
const (
	MemberDraft = "draft"
	MemberTrack = "track"
)

// Session is one row of sessions.
type Session struct {
	ID, Kind                        string
	SourceKey                       *string
	Name, StartedAt                 string
	EndedAt                         *string
	CreatedBy, CreatedAt, UpdatedBy string
	UpdatedAt                       string
	Revision                        int
	HiddenAt                        *string
}

// Summary is a list row (C92.2).
type Summary struct {
	Session
	Drafts, Takes, Songs int
}

// ListFilter selects Sessions.
type ListFilter struct {
	Kinds         []string
	Q             string
	IncludeHidden bool
	Limit         int
	Cursor        string
}

// Page is one page of Sessions.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

// Patch is the input of Update.
type Patch struct {
	Revision int
	Name     *string
	Hidden   *bool
}

// Key identifies an auto session by its source key.
type Key struct {
	Kind, SourceKey, Name, StartedAt string
	EndedAt                          *string
}
