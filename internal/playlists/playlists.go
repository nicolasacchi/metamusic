// Package playlists owns ordered track lists (contract C33). M2a-B4a freezes
// the signatures; M2a-B6 implements them.
package playlists

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// NewPlaylist is the input of Create.
type NewPlaylist struct {
	Name, Description string
	TrackIDs          []string
}

// PlaylistPatch is the input of Update; Revision is required. TrackIDs and
// AddTrackIDs are mutually exclusive.
type PlaylistPatch struct {
	Revision          int
	Name, Description *string
	TrackIDs          []string // full ordered replacement
	AddTrackIDs       []string // appended; duplicates allowed
	Hidden            *bool
}

// PlaylistSummary is one row of the playlist list.
type PlaylistSummary struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	ItemsCount int     `json:"items_count"`
	DurationS  float64 `json:"duration_s"`
	Hidden     bool    `json:"hidden"`
	Revision   int     `json:"revision"`
	UpdatedAt  string  `json:"updated_at"`
}

// Item is one playlist position.
type Item struct {
	Position int                  `json:"position"`
	Track    library.TrackSummary `json:"track"`
}

// Playlist is the full playlist view.
type Playlist struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    int    `json:"revision"`
	Hidden      bool   `json:"hidden"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by" doc:"Principal key; masked for agents"`
	UpdatedBy   string `json:"updated_by" doc:"Principal key; masked for agents"`
	Items       []Item `json:"items" nullable:"false"`
}

// Service implements C33's playlists.
type Service struct {
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string
}

// New builds the playlists service; nil now means time.Now.
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

// Create stores a new playlist.
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewPlaylist) (Playlist, error) {
	// STUB(M2a-B6)
	return Playlist{}, errors.ErrUnsupported
}

// Update edits a playlist under optimistic locking.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in PlaylistPatch) (Playlist, error) {
	// STUB(M2a-B6)
	return Playlist{}, errors.ErrUnsupported
}

// Get returns one playlist with its tracks.
func (s *Service) Get(ctx context.Context, id string) (Playlist, error) {
	// STUB(M2a-B6)
	return Playlist{}, errors.ErrUnsupported
}

// List returns the playlists, hidden ones only on request.
func (s *Service) List(ctx context.Context, includeHidden bool) ([]PlaylistSummary, error) {
	// STUB(M2a-B6)
	return nil, errors.ErrUnsupported
}
