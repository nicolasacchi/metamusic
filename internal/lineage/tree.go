package lineage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/view"
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
	Transform     *TransformRef   `json:"transform"`
	AlsoFrom      []AlsoFrom      `json:"also_from"`

	// E2 (C149, D-E2-12, D-E2-25): omitted for a yue2 take without them, so the node's wire bytes do not move.
	Engine      string         `json:"engine,omitempty"`
	Reference   *NodeReference `json:"reference,omitempty"`
	Stems       int            `json:"stems,omitempty"`
	CrossEngine bool           `json:"cross_engine,omitempty"`
}

// NodeReference is the reference edge of a node: the take whose clip window steered the render, in seconds.
type NodeReference struct {
	TrackID string  `json:"track_id"`
	FromS   float64 `json:"from_s"`
	ToS     float64 `json:"to_s"`
}

// TransformRef marks a node that is a transform output (D-T1-4).
type TransformRef struct {
	Kind        string `json:"kind"`
	TransformID string `json:"transform_id"`
	SpecHash    string `json:"spec_hash"`
}

// AlsoFrom is a further input of a transform: an annotation, never a tree edge (D-T1-5).
type AlsoFrom struct {
	TrackID string `json:"track_id"`
	Role    string `json:"role"`
}

// Tree is the lineage of one track's root, nodes in pre-order (generation, created_at).
type Tree struct {
	RootID    string
	Nodes     []Node
	Truncated bool
	Total     int
}

// treeSQL's first placeholder is the viewer's rater (store.ViewerRatingJoin), the second the root track.
//
//nolint:gosec // G202: the viewer join is a fixed fragment of store.ViewerRatingJoin
var treeSQL = `SELECT t.id, t.title, t.status, COALESCE(m.operator, ''), r.request_id, t.parent_track_id, t.generation, r.seed, r.engine,
	t.lossy, COALESCE(m.inferred, 0), m.rationale, hr.stars, ar.stars,
	CASE WHEN t.transform_id IS NOT NULL THEN 'transform' ELSE t.origin END, tr.kind, tr.spec_hash, j.outcome, j.id, t.created_at,
	j.expected_micro_usd, b.n_takes, b.reconciled_at IS NOT NULL,
	(SELECT SUM(l.micro_usd) FROM ledger l WHERE l.job_id = j.id AND l.kind = 'settle' AND l.reason IN ('execution','execution_unknown')),
	(SELECT SUM(l.micro_usd) FROM ledger l WHERE l.batch_id = j.batch_id AND l.kind = 'settle' AND l.reason = 'overhead'),
	json_extract(r.request_json, '$.ref.track_id'), json_extract(r.request_json, '$.ref.from_ds'), json_extract(r.request_json, '$.ref.to_ds'),
	(SELECT COUNT(*) FROM artifacts a WHERE a.track_id = t.id AND a.name GLOB 'stem-*.flac'),
	COALESCE(json_extract(m.params_json, '$.cross_engine'), 0)
	FROM tracks t JOIN recipes r ON r.id = t.recipe_id
	LEFT JOIN mutations m ON m.child_track_id = t.id
	` + store.ViewerRatingJoin("hr", "t.id") + `
	LEFT JOIN critic_ratings ar ON ar.track_id = t.id
	LEFT JOIN transforms tr ON tr.id = t.transform_id
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
	var root, status string
	switch err := s.st.R.QueryRowContext(ctx, `SELECT root_track_id, status FROM tracks WHERE id = ?`, trackID).Scan(&root, &status); {
	case errors.Is(err, sql.ErrNoRows):
		return Tree{}, ErrNotFound
	case err != nil:
		return Tree{}, fmt.Errorf("lineage: tree: %w", err)
	}
	// A viewer that hides discarded takes gets 404 for a discarded id and a tree without discarded nodes: the visible
	// children of a hidden node hang from its nearest visible ancestor (C308.2).
	v := view.FromContext(ctx)
	hide := v.HideDiscarded
	if hide && status == "discarded" {
		return Tree{}, ErrNotFound
	}
	var hidden map[string]bool
	if hide {
		var err error
		if hidden, err = s.discardedSet(ctx); err != nil {
			return Tree{}, err
		}
	}
	rows, err := s.st.R.QueryContext(ctx, treeSQL, auth.DisplayRater(auth.FromContext(ctx)), root)
	if err != nil {
		return Tree{}, fmt.Errorf("lineage: tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	nodes := map[string]*Node{}
	children := map[string][]*Node{}
	var rootNode *Node
	hiddenParent := map[string]string{} // hidden node id -> its raw parent id ("" for none)
	for rows.Next() {
		n := &Node{}
		var parent, rationale, outcome, jobID, tKind, tHash sql.NullString
		var seed int64
		var engine string
		var stars, critic sql.NullInt64
		var lossy, inferred int
		var expected, nTakes, execSettled, overheadSettled sql.NullInt64
		var reconciled sql.NullBool
		var refTrack sql.NullString
		var refFrom, refTo sql.NullInt64
		var stems, cross int
		if err := rows.Scan(&n.ID, &n.Title, &n.Status, &n.Operator, &n.RequestID, &parent, &n.Generation, &seed, &engine,
			&lossy, &inferred, &rationale, &stars, &critic, &n.Origin, &tKind, &tHash, &outcome, &jobID, &n.CreatedAt,
			&expected, &nTakes, &reconciled, &execSettled, &overheadSettled, &refTrack, &refFrom, &refTo, &stems, &cross); err != nil {
			return Tree{}, fmt.Errorf("lineage: tree: %w", err)
		}
		if hide && hidden[n.ID] {
			hiddenParent[n.ID] = parent.String
			continue
		}
		n.RecipeKnown = engine != "unknown"
		if engine != "yue2" && engine != "unknown" {
			n.Engine = engine
		}
		if refTrack.Valid && !(hide && hidden[refTrack.String]) {
			n.Reference = &NodeReference{TrackID: refTrack.String, FromS: float64(refFrom.Int64) / 10, ToS: float64(refTo.Int64) / 10}
		}
		n.Stems, n.CrossEngine = stems, cross == 1
		if n.RecipeKnown {
			v := seed
			n.Seed = &v
		}
		n.Lossy, n.Inferred = lossy == 1, inferred == 1
		n.AlsoFrom = []AlsoFrom{}
		if tKind.Valid {
			n.Transform = &TransformRef{Kind: tKind.String, TransformID: n.ID, SpecHash: tHash.String}
		}
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
		if n.ID == root {
			rootNode = n
		}
	}
	if err := rows.Err(); err != nil {
		return Tree{}, err
	}
	if rootNode == nil && !hide {
		return Tree{}, ErrNotFound
	}
	for _, n := range nodes {
		if n.ParentID == nil {
			continue
		}
		pid := *n.ParentID
		for hidden[pid] { // climb past hidden ancestors (the loop ends: a hidden node's chain is finite)
			pid = hiddenParent[pid]
		}
		if pid == "" {
			n.ParentID = nil
		} else {
			n.ParentID = &pid
		}
		if n.ParentID != nil {
			children[pid] = append(children[pid], n)
		}
	}
	if rootNode == nil { // the root is hidden: start at the topmost visible ancestor of the requested take
		top := nodes[trackID]
		for top != nil && top.ParentID != nil && nodes[*top.ParentID] != nil {
			top = nodes[*top.ParentID]
		}
		if top == nil {
			return Tree{}, ErrNotFound
		}
		rootNode, root = top, top.ID
		reach := map[string]*Node{}
		var mark func(n *Node)
		mark = func(n *Node) {
			reach[n.ID] = n
			for _, k := range children[n.ID] {
				mark(k)
			}
		}
		mark(top)
		nodes = reach
	}
	if err := s.alsoFrom(ctx, root, nodes, hidden); err != nil {
		return Tree{}, err
	}
	for id, kids := range children {
		if nodes[id] == nil { // a branch outside the re-rooted subtree
			delete(children, id)
			continue
		}
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

// alsoFrom annotates the transform nodes of one tree with the inputs that are not their ledger parent (D-T1-5). The
// annotated track gains no child edge: the tree is built from parent_track_id alone.
func (s *Service) alsoFrom(ctx context.Context, root string, nodes map[string]*Node, hidden map[string]bool) error {
	rows, err := s.st.R.QueryContext(ctx, `SELECT ti.transform_id, ti.track_id, ti.role
		FROM transform_inputs ti JOIN tracks t ON t.id = ti.transform_id
		WHERE t.root_track_id = ? AND (t.parent_track_id IS NULL OR ti.track_id <> t.parent_track_id)
		ORDER BY ti.transform_id, ti.ord`, root)
	if err != nil {
		return fmt.Errorf("lineage: also_from: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var a AlsoFrom
		if err := rows.Scan(&id, &a.TrackID, &a.Role); err != nil {
			return fmt.Errorf("lineage: also_from: %w", err)
		}
		if hidden[a.TrackID] {
			continue
		}
		if n := nodes[id]; n != nil {
			n.AlsoFrom = append(n.AlsoFrom, a)
		}
	}
	return rows.Err()
}

// discardedSet is the id set of every discarded take (the hidden nodes of a Listener's tree).
func (s *Service) discardedSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id FROM tracks WHERE status = 'discarded'`)
	if err != nil {
		return nil, fmt.Errorf("lineage: discarded set: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
