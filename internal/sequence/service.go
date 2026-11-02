// Package sequence loads feature rows for the sequencer and serves the suggest_sequence preview (L3 C138). The method set
// and types are frozen by L3-F2; the bodies are lane SR (load.go, doc.go, suggest.go).
package sequence

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/sequencer"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Querier is the read side shared by *sql.DB and *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Def is the document definition of a stored or previewed sequence.
const Def = "mm.sequence/1"

// Ref names a Song and, optionally, the Take whose features count (nil: the Song's subject).
type Ref struct {
	SongID string
	TakeID *string
}

// FeatureBasis is the provenance of a loaded Song's three values.
type FeatureBasis = sequencer.Bases

// SuggestInput selects what to sequence: exactly one of RecordID, SongIDs (2..60) or CrateID.
type SuggestInput struct {
	RecordID string
	SongIDs  []string
	CrateID  string
	Strategy sequencer.Strategy
	First    string
	Last     string
}

// StepView is a planned step with the Song and Take titles injected at read.
type StepView struct {
	Step      sequencer.Step
	Text      string
	Title     string
	TakeTitle string
}

// Cost is the reported cost (J / 1e10, four decimals) and the signed improvement in percent.
type Cost struct {
	Input          string
	Output         string
	ImprovementPct *string
}

// Suggestion is the mm.sequence/1 document the sequence slot op stores, plus titles.
type Suggestion struct {
	Def        string
	Strategy   string
	InputOrder []string
	Order      []string
	Steps      []StepView
	Cost       Cost
	Coverage   sequencer.Coverage
	Exact      bool
	Warnings   []sequencer.Warning
	Digest     string
	Pending    bool
}

// Service is the read-only sequencing service.
type Service struct {
	st     *store.Store
	crates CrateSongs
}

// New builds the service.
func New(st *store.Store) *Service { return &Service{st: st} }
