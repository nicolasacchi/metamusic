// Package records holds the Record model of the curation axis: a living, ordered sequence of Songs whose slots follow the
// Song's current Pick unless pinned (C120-C122). This file carries the row types and the cover seed; the Pick resolution is
// in resolve.go and the append-only changelog in changes.go, text.go and why.go. The package reads the L1 tables through SQL
// only and imports no L1 package, so no Muse text can reach it (C118).
package records

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// Status is the lifecycle state of a Record (D-L2-3).
type Status string

// The three states: draft, then proposed by an agent, then published by a human.
const (
	StatusDraft     Status = "draft"
	StatusProposed  Status = "proposed"
	StatusPublished Status = "published"
)

// Record is one row of records.
type Record struct {
	ID, Title, Subtitle, LinerNote string
	CoverSeed                      int64
	CoverRerolls                   int
	Status                         Status
	ProposalNote                   *string
	ProposedBy, ProposedAt         *string
	PublishedBy, PublishedAt       *string
	FirstPublishedAt               *string
	Revision                       int
	CreatedBy, CreatedAt           string
	UpdatedBy, UpdatedAt           string
	HiddenAt                       *string
}

// NewRecord is the input of Create; SongIDs become floating slots, in order.
type NewRecord struct {
	Title, Subtitle, LinerNote string
	SongIDs                    []string
}

// Patch is the input of Update: only the set fields change.
type Patch struct {
	Revision                   int
	Title, Subtitle, LinerNote *string
	CoverReroll                bool
	Hidden                     *bool
}

// ListFilter selects Records; hidden ones are excluded unless IncludeHidden.
type ListFilter struct {
	Statuses      []Status
	Q             string
	SongID        string
	IncludeHidden bool
	Limit         int
	Cursor        string
}

// Summary is one list row. Covers are built by recordview (cover inputs), never by this package.
type Summary struct {
	ID, Title, Subtitle string
	Status              Status
	Slots, Playable     int
	DurationS           float64
	Revision            int
	UpdatedAt           string
	Hidden              bool
	Warnings            int
}

// Page is one page of Summaries.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

// CoverSeed is the layout seed of a Record's cover: the first 53 bits of sha256("mm-cover-seed/1|" + id + "|" + rerolls),
// so it is a non-negative integer a JSON number and a double both hold exactly. Verify R6 recomputes it.
func CoverSeed(recordID string, rerolls int) int64 {
	sum := sha256.Sum256([]byte("mm-cover-seed/1|" + recordID + "|" + strconv.Itoa(rerolls)))
	return int64(binary.BigEndian.Uint64(sum[:8]) >> 11)
}
