package lineage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// Tree limits (C71.6).
const (
	MaxTreeLimit   = 500
	DefaultLimit   = 200
	maxPageBytes   = 90000 // M1 C17; each package keeps its own constant
	envelopeSlack  = 400   // root_id, total, truncated and braces
	reconciledCost = "reconciled"
)

// Node is one track in a lineage tree. The JSON tags are the wire names of api.LineageNode: the byte cap
// is measured on this form.
type Node struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Status        string          `json:"status"`
	Operator      string          `json:"operator"`
	RequestID     string          `json:"request_id"`
	ParentID      *string         `json:"parent_id"`
	Generation    int             `json:"generation"`
	Seed          *int64          `json:"seed"` // nil <=> !RecipeKnown
	RecipeKnown   bool            `json:"recipe_known"`
	Lossy         bool            `json:"lossy"` // M2c: a reference node has a stub request id and no seed
	Inferred      bool            `json:"inferred"`
	Rationale     *string         `json:"rationale"`
	Stars         *int            `json:"stars"`
	CriticStars   *int            `json:"critic_stars"`
	Origin        string          `json:"origin"`
	Outcome       *string         `json:"outcome"`
	CostMicroUSD  *money.MicroUSD `json:"cost_micro_usd"`
	CostBasis     *string         `json:"cost_basis"`
	JobID         *string         `json:"job_id"`
	CreatedAt     string          `json:"created_at"`
	ChildrenCount int             `json:"children_count"`
}

// Tree is the lineage of one track's root, nodes in pre-order (generation, created_at).
type Tree struct {
	RootID    string
	Nodes     []Node
	Truncated bool
	Total     int
}

const treeSQL = `SELECT t.id, t.title, t.status, COALESCE(m.operator, ''), r.request_id, t.parent_track_id, t.generation, r.seed, r.engine,
	t.lossy, COALESCE(m.inferred, 0), m.rationale, hr.stars, ar.stars, t.origin, j.outcome, j.id, t.created_at,
	j.expected_micro_usd, b.n_takes, b.reconciled_at IS NOT NULL,
	(SELECT SUM(l.micro_usd) FROM ledger l WHERE l.job_id = j.id AND l.kind = 'settle' AND l.reason IN ('execution','execution_unknown')),
	(SELECT SUM(l.micro_usd) FROM ledger l WHERE l.batch_id = j.batch_id AND l.kind = 'settle' AND l.reason = 'overhead')
	FROM tracks t JOIN recipes r ON r.id = t.recipe_id
	LEFT JOIN mutations m ON m.child_track_id = t.id
	LEFT JOIN ratings hr ON hr.track_id = t.id AND hr.source = 'human'
	LEFT JOIN ratings ar ON ar.track_id = t.id AND ar.source = 'agent'
	LEFT JOIN jobs j ON j.id = t.job_id
	LEFT JOIN batches b ON b.id = j.batch_id
	WHERE t.root_track_id = ?`

// Tree reads the lineage tree around trackID; limit <= 500 (C71.6). Nodes beyond limit or beyond the
// 90 000-byte page cap are cut at a node boundary in pre-order and Truncated is set; Total still counts
// every node.
func (s *Service) Tree(ctx context.Context, trackID string, limit int) (Tree, error) {
	if s.st == nil {
		return Tree{}, ErrUnconfigured
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxTreeLimit)
	var root string
	switch err := s.st.R.QueryRowContext(ctx, `SELECT root_track_id FROM tracks WHERE id = ?`, trackID).Scan(&root); {
	case errors.Is(err, sql.ErrNoRows):
		return Tree{}, ErrNotFound
	case err != nil:
		return Tree{}, fmt.Errorf("lineage: tree: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, treeSQL, root)
	if err != nil {
		return Tree{}, fmt.Errorf("lineage: tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	nodes := map[string]*Node{}
	children := map[string][]*Node{}
	var rootNode *Node
	for rows.Next() {
		n := &Node{}
		var parent, rationale, outcome, jobID sql.NullString
		var seed int64
		var engine string
		var stars, critic sql.NullInt64
		var lossy, inferred int
		var expected, nTakes, execSettled, overheadSettled sql.NullInt64
		var reconciled sql.NullBool
		if err := rows.Scan(&n.ID, &n.Title, &n.Status, &n.Operator, &n.RequestID, &parent, &n.Generation, &seed, &engine,
			&lossy, &inferred, &rationale, &stars, &critic, &n.Origin, &outcome, &jobID, &n.CreatedAt,
			&expected, &nTakes, &reconciled, &execSettled, &overheadSettled); err != nil {
			return Tree{}, fmt.Errorf("lineage: tree: %w", err)
		}
		n.RecipeKnown = engine != "unknown"
		if n.RecipeKnown {
			v := seed
			n.Seed = &v
		}
		n.Lossy, n.Inferred = lossy == 1, inferred == 1
		if parent.Valid {
			n.ParentID = &parent.String
		}
		if rationale.Valid {
			n.Rationale = &rationale.String
		}
		if stars.Valid {
			v := int(stars.Int64)
			n.Stars = &v
		}
		if critic.Valid {
			v := int(critic.Int64)
			n.CriticStars = &v
		}
		if outcome.Valid {
			n.Outcome = &outcome.String
		}
		if jobID.Valid {
			n.JobID = &jobID.String
			cost, basis := DisplayCost(expected.Int64, nTakes.Int64, execSettled, overheadSettled, reconciled.Bool)
			n.CostMicroUSD, n.CostBasis = &cost, &basis
		}
		nodes[n.ID] = n
		if parent.Valid {
			children[parent.String] = append(children[parent.String], n)
		}
		if n.ID == root {
			rootNode = n
		}
	}
	if err := rows.Err(); err != nil {
		return Tree{}, err
	}
	if rootNode == nil {
		return Tree{}, ErrNotFound
	}
	for id, kids := range children {
		sort.SliceStable(kids, func(a, b int) bool {
			if kids[a].CreatedAt != kids[b].CreatedAt {
				return kids[a].CreatedAt < kids[b].CreatedAt
			}
			return kids[a].ID < kids[b].ID
		})
		nodes[id].ChildrenCount = len(kids)
	}
	t := Tree{RootID: root, Total: len(nodes)}
	var walk func(n *Node) bool // false: stop
	size := envelopeSlack
	walk = func(n *Node) bool {
		if len(t.Nodes) >= limit {
			return false
		}
		b, _ := json.Marshal(n)
		if len(t.Nodes) > 0 && size+len(b)+1 > s.pageBytes {
			return false
		}
		size += len(b) + 1
		t.Nodes = append(t.Nodes, *n)
		for _, k := range children[n.ID] {
			if !walk(k) {
				return false
			}
		}
		return true
	}
	walk(rootNode)
	t.Truncated = len(t.Nodes) < t.Total
	return t, nil
}

// DisplayCost is the cost shown for a generated take (D-M3-17): its settled execution plus its share of
// the batch's settled overhead (ceil(overhead / takes)); the estimate before anything settled; reconciled
// once billing matched the batch.
func DisplayCost(expected, nTakes int64, exec, overhead sql.NullInt64, reconciled bool) (money.MicroUSD, string) {
	if !exec.Valid {
		return money.MicroUSD(expected), "estimate"
	}
	share := int64(0)
	if nTakes > 0 && overhead.Valid {
		share = (overhead.Int64 + nTakes - 1) / nTakes
	}
	basis := "provisional"
	if reconciled {
		basis = reconciledCost
	}
	return money.MicroUSD(exec.Int64 + share), basis
}
