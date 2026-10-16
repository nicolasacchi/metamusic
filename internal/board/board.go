// Package board is the Muse Board read model (C97): rows = Muses, columns = angles, cells = Song chips, row-paged under
// the 90 000-byte page cap. It reads Muse text (the only package besides muses that may, C99.2) and never spends.
package board

import (
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
	Angle *string `json:"angle"`
	Songs int     `json:"songs"`
}

// Counts are the per-row or total state counts.
type Counts struct {
	Draft    int `json:"draft"`
	Rendered int `json:"rendered"`
	Judged   int `json:"judged"`
	Picked   int `json:"picked"`
	Queued   int `json:"queued"`
}

// Chip is one Song on the Board.
type Chip struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	State         string  `json:"state"`
	Takes         int     `json:"takes"`
	PickSource    *string `json:"pick_source"`
	PickTakeID    *string `json:"pick_take_id"`
	Queued        bool    `json:"queued"`
	DraftID       *string `json:"draft_id"`
	TempoMismatch bool    `json:"tempo_mismatch"`
}

// Cell is one non-empty (Muse, angle) cell.
type Cell struct {
	Angle *string `json:"angle"`
	Chips []Chip  `json:"chips"`
}

// Row is one Muse row, or the Unassigned pseudo-row when Muse is nil.
type Row struct {
	Muse                 *muses.MuseRef `json:"muse"`
	SongsTotal           int            `json:"songs_total"`
	Counts               Counts         `json:"counts"`
	Cells                []Cell         `json:"cells"`
	ChipsMore            bool           `json:"chips_more"`
	NextUnassignedCursor *string        `json:"next_unassigned_cursor"`
}

// Totals are over the whole filter, not the page.
type Totals struct {
	Muses    int `json:"muses"`
	Songs    int `json:"songs"`
	Draft    int `json:"draft"`
	Rendered int `json:"rendered"`
	Judged   int `json:"judged"`
	Picked   int `json:"picked"`
	Queued   int `json:"queued"`
}

// Board is the answer of Get.
type Board struct {
	GeneratedAt     string   `json:"generated_at"`
	TaxonomyVersion int      `json:"taxonomy_version"`
	Columns         []Column `json:"columns"`
	Rows            []Row    `json:"rows"`
	Totals          Totals   `json:"totals"`
	NextCursor      *string  `json:"next_cursor"`
}

// Service builds the Board (C97).
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the Service.
func New(st *store.Store, now func() time.Time) *Service { return &Service{st: st, now: now} }
