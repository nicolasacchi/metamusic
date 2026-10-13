package lineage

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a STUB(M3-B4) body: the contract is frozen, the behaviour lands in M3-B4.
var ErrNotImplemented = errors.New("lineage: not implemented (STUB(M3-B4))")

// Options builds a Service.
type Options struct {
	Store *store.Store
	Now   func() time.Time // nil => time.Now
}

// Service reads the Operator Ledger (lineage trees) and keeps the recipe keys current (C71.6, C71.7).
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{st: o.Store, now: o.Now}
}

// Node is one track in a lineage tree.
type Node struct {
	ID, Title, Status, Operator, RequestID string
	ParentID                               *string
	Generation                             int
	Seed                                   *int64 // nil <=> !RecipeKnown
	RecipeKnown, Lossy                     bool   // M2c: a reference node has a stub request id and no seed
	Inferred                               bool
	Rationale                              *string
	Stars, CriticStars                     *int
	Origin                                 string
	Outcome                                *string
	CostMicroUSD                           *money.MicroUSD
	CostBasis                              *string
	JobID                                  *string
	CreatedAt                              string
	ChildrenCount                          int
}

// Tree is the lineage of one track's root, nodes in pre-order (generation, created_at).
type Tree struct {
	RootID    string
	Nodes     []Node
	Truncated bool
	Total     int
}

// Tree reads the lineage tree around trackID; limit <= 500 (C71.6). STUB(M3-B4).
func (s *Service) Tree(ctx context.Context, trackID string, limit int) (Tree, error) {
	return Tree{}, ErrNotImplemented
}

// BackfillRecipeKeys fills recipe_keys for yue2 recipes inserted before M3 and never for stubs
// (D-M3-34); idempotent (C71.7). STUB(M3-B4).
func (s *Service) BackfillRecipeKeys(ctx context.Context) (int, error) {
	return 0, ErrNotImplemented
}
