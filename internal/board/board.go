// Package board is the Muse Board read model (C97): rows = Muses, columns = angles, cells = Song chips, row-paged under
// the 90 000-byte page cap. It reads Muse text (the only package besides muses that may, C99.2) and never spends.
package board

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Query is the input of Get (C96 get_muse_board).
type Query struct {
	Detail          string // chips | counts (default counts)
	MuseID          string
	SessionID       string
	Angle           string
	States          []string
	Sounds          map[string][]string
	Q               string
	IncludeHidden   bool
	Limit           int
	Cursor          string
	UnassignedAfter string
}

// Column is one angle column.
type Column struct {
	Angle *string
	Songs int
}

// Counts are the per-row or total state counts.
type Counts struct{ Draft, Rendered, Judged, Picked, Queued int }

// Chip is one Song on the Board.
type Chip struct {
	ID, Title, State string
	Takes            int
	PickSource       *string
	PickTakeID       *string
	Queued           bool
	DraftID          *string
	TempoMismatch    bool
}

// Cell is one non-empty (Muse, angle) cell.
type Cell struct {
	Angle *string
	Chips []Chip
}

// Row is one Muse row, or the Unassigned pseudo-row when Muse is nil.
type Row struct {
	Muse                 *muses.MuseRef
	SongsTotal           int
	Counts               Counts
	Cells                []Cell
	ChipsMore            bool
	NextUnassignedCursor *string
}

// Totals are over the whole filter, not the page.
type Totals struct {
	Muses, Songs                            int
	Draft, Rendered, Judged, Picked, Queued int
}

// Board is the answer of Get.
type Board struct {
	GeneratedAt     string
	TaxonomyVersion int
	Columns         []Column
	Rows            []Row
	Totals          Totals
	NextCursor      *string
}

// Service builds the Board. The method set is frozen by L1-B2; the body is L1-B5's (STUB(L1-B5)).
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the Service.
func New(st *store.Store, now func() time.Time) *Service { return &Service{st: st, now: now} }

// Get assembles one Board page (C97).
func (s *Service) Get(ctx context.Context, q Query) (Board, error) {
	return Board{}, errors.ErrUnsupported // STUB(L1-B5)
}
