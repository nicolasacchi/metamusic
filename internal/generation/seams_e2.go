package generation

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The E2 seams (C149, C151.3, C165, D-E2-15). The contract freeze (E2-F2) fixed the shapes; lane E2-G2 fills expand_e2.go,
// inputs.go and the estimate branches, lane E2-G1 owns the lineage rows they call. generation reaches the adoption table and
// the clip renderer only through the interfaces below and never imports enginetrial or suites.

// Edit holds the mode inputs of an item in seconds (API values, 0.1 s resolution; the planner stores deciseconds).
type Edit struct {
	FromS, ToS, ExtendS *float64
	Tracks              []string // complete 1..4, stems 1..6 names (one job per name)
}

// Reference is the reference input of a compose_new or reference item: a window of a take, default its last 20 s.
type Reference struct {
	TrackID    string
	FromS, ToS *float64
}

// PlannedEdit echoes what a take edits, in seconds, with the stem name of an extract take.
type PlannedEdit struct {
	FromS, ToS, ExtendS *float64
	Tracks              []string
	Stem                string
}

// PlannedRef echoes the pinned reference clip of a take.
type PlannedRef struct {
	TrackID    string
	FromS, ToS float64
	ClipSHA256 string
}

// Warning codes of E2 estimates (C149.2), in addition to M3's. Stable API values.
const (
	WarnCrossEngineEdit          = "cross_engine_edit"
	WarnABCDropped               = "abc_dropped"
	WarnCotIgnored               = "cot_ignored"
	WarnDurationEstimated        = "duration_estimated"
	WarnCaptionTruncated         = "caption_truncated"
	WarnExtendSeamExperimental   = "extend_seam_experimental"
	WarnReferenceWindowShort     = "reference_window_short"
	WarnReferenceNotReproducible = "reference_not_reproducible"
	WarnBaseClassCost            = "base_class_cost"
	WarnParamsVersionChanged     = "params_version_changed"
	WarnStemExists               = "stem_exists"
	WarnABWindowTruncated        = "ab_window_truncated"
)

// QueryReader is the read side of a pool or transaction, spelled as an alias of an unnamed interface so that
// engineadopt.Querier is the identical type.
type QueryReader = interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AdoptionGate decides whether an engine may receive money (D-E2-15). engineadopt.Service implements it.
type AdoptionGate interface {
	// Admitted returns nil for an adopted engine (yue2 always), else 409 ENGINE_NOT_ADOPTED {engine, hint}. trialID is the
	// estimate's options.trial_id, honoured from E2c on.
	Admitted(ctx context.Context, q QueryReader, engine, trialID string) error
}

// ReferenceClipper renders reference clips (clip.Service implements it, C165). Estimate resolves the clip and freezes its
// digest; create re-ensures it and answers 409 ESTIMATE_STALE reason reference_changed on a different digest.
type ReferenceClipper interface {
	Reference(ctx context.Context, sp clip.RefSpec) (clip.RefObject, error)
	ToolVersion() string
}

// EnginePatch carries the acestep15-only fields of an item's patch (the lineage Patch is yue2's type): any use with a yue2
// parent is 422 INVALID_ITEM rule <field>_forbidden.
type EnginePatch struct {
	BPM           *int
	KeyScale      *string
	TimeSignature *string
	DurationS     *float64
}

// e2Seams are the E2 collaborators, all nil-safe: with none set the service behaves exactly as under M3 (the single Gate).
type e2Seams struct {
	backends *provider.Set
	gates    *provider.GateSet
	adoption AdoptionGate
	refClips ReferenceClipper

	batchHook BatchHook // set by UseBatchHook (hooks.go); UseEngines keeps it
}

// UseEngines wires the per-engine routing (C151.3), the adoption gate (D-E2-15) and the reference clip renderer (C165). The
// app calls it once after every service exists (a setter, not an Options field, because the backend set is built after the
// service). Any argument may be nil. Lane E2-G2 reads these in the estimate and create paths.
func (s *Service) UseEngines(backends *provider.Set, gates *provider.GateSet, adoption AdoptionGate, refClips ReferenceClipper) {
	s.e2.backends, s.e2.gates, s.e2.adoption, s.e2.refClips = backends, gates, adoption, refClips
}
