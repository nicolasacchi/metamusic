package generation

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// The M5 seams (C253.4, C255.5). generation never imports songpr or callbacks: the app injects implementations through
// Options.PatchSource and Options.BacklogGate. Both are nil-safe: a nil PatchSource refuses the song_pr mode with 503
// UNAVAILABLE {component:"songpr"}, a nil BacklogGate lets every request through. M5-F2 froze the interfaces; lanes M5-S1 and
// M5-C1 implement them and add the call sites.

// PatchRow is a stored Song PR as the create path needs it: the base take to render from and the compiled patch.
type PatchRow struct {
	ID          string
	BaseTrackID string
	Operator    string  // lyric_edit | style_edit | multi_edit
	Lyrics      *string // the compiled patch (M3 Patch fields): only the changed fields are set
	Style       *string
	CommentIDs  []string // the comments the patch addresses, carried into items_json
}

// PatchSource reads Song PRs for estimate and create (songpr.Service implements it).
type PatchSource interface {
	// PatchForEstimate loads the patch; an unknown id is a 404 NOT_FOUND.
	PatchForEstimate(ctx context.Context, q lineage.Querier, id string) (PatchRow, error)
	// RecheckComments fails with 409 ESTIMATE_STALE (reason comments_changed) when an addressed comment stopped being addressable.
	RecheckComments(ctx context.Context, q lineage.Querier, id string) error
	// RenderCount counts the patch's non-cancelled batches (songpr.PatchMaxRenders caps it: 422 PATCH_RENDER_LIMIT).
	RenderCount(ctx context.Context, q lineage.Querier, id string) (int, error)
}

// BacklogGate paces agent spend by the human's answers (D-M5-R3-3; callbacks.Service implements it).
type BacklogGate interface {
	// Backlog counts the callbacks of the lineage root that no human has answered yet.
	Backlog(ctx context.Context, q lineage.Querier, rootTrackID string) (int, error)
	// Limit is MM_CALLBACK_BACKLOG_MAX; 0 switches the gate off. Estimate and create of an agent item set that renders a
	// song_pr, or any item whose mother has the root, answer 409 CALLBACKS_BACKLOG {root_track_id, pending, limit} at pending >= Limit.
	Limit() int
}
