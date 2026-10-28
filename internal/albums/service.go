// Package albums is the catalog the Subsonic server consumes: Record albums first, and (optional lane A2) per-Muse
// auto-albums and the Unfiled album with neutral names, plus a cheap change token (C126, D-L2-15). It cannot import muses:
// Muse facts arrive through MuseDirectory, whose Labels (real names) are only ever called when the operator opted in
// (D-L2-16, TestAlbums_MuseLabelsNotCalledWhenOff).
package albums

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Kind is the kind of an album.
type Kind string

// The three kinds: record (lane A), muse and unfiled (lane A2).
const (
	KindRecord  Kind = "record"
	KindMuse    Kind = "muse"
	KindUnfiled Kind = "unfiled"
)

// The album id forms: rec-<record id>, mus-<muse id> and the one unf.
const (
	PrefixRecord = "rec-"
	PrefixMuse   = "mus-"
	IDUnfiled    = "unf"
	NameUnfiled  = "Unfiled"
)

// Album is one album of the catalog. RecordID and MuseID are ids only, never text.
type Album struct {
	ID         string
	Kind       Kind
	Name       string
	SortName   string
	SongCount  int
	CoverInput cover.Input
	RecordID   *string
	MuseID     *string
}

// Song is one listed Take of an album. SlotID is set for a Record album.
type Song struct {
	TakeID         string
	Position, Disc int
	SlotID         *string
}

// MuseMeta is the neutral facts about a Muse: its frozen ordinal and whether it is hidden. No text.
type MuseMeta struct {
	Ordinal int
	Hidden  bool
}

// MuseDirectory is what the catalog may ask of the Muses; muses.Service implements it in internal/muses/directory.go
// (lane A2). It is nil until then, which leaves the record kind only.
type MuseDirectory interface {
	// Ordinals returns every Muse's neutral facts in ONE statement; List and Get call it, Token never does.
	Ordinals(ctx context.Context) (map[string]MuseMeta, error)
	// Version is COUNT(*) and MAX(updated_at) of muses (two indexed probes), the only directory call Token makes.
	Version(ctx context.Context) (string, error)
	// Labels returns real names; it is called ONLY when the subsonic_muse_names opt-in is on.
	Labels(ctx context.Context, ids []string) (map[string]string, error)
}

// Settings reads the one opt-in the catalog honours; curation.Service implements it.
type Settings interface {
	SubsonicMuseNames(ctx context.Context) (bool, error)
}

// Service is the album catalog. The method set is frozen by F2. membership.go and token.go carry the record kind and the
// change token (lane A); lane A2 adds the muse and unfiled kinds in muse.go and naming.go.
type Service struct {
	st    *store.Store
	rec   *records.Service
	muses MuseDirectory
	set   Settings
	now   func() time.Time
}

// New builds the catalog. muses may be nil (record albums only); a nil now means time.Now.
func New(st *store.Store, rec *records.Service, muses MuseDirectory, set Settings, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, rec: rec, muses: muses, set: set, now: now}
}

// NeutralName is the name of a Muse's auto-album when the operator has not opted in: "Inspiration " and the Muse's frozen
// ordinal, zero-padded to three digits (more when needed). It reveals nothing about the Muse (D-L2-15).
func NeutralName(ordinal int) string { return fmt.Sprintf("Inspiration %03d", ordinal) }

// ErrNotFound is returned by Get for an unknown, unpublished, hidden or empty album; Subsonic maps it to error 70.
var ErrNotFound = errors.New("albums: no such album")
