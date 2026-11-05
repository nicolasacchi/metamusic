package trackctx

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// The E2 seam of /context (C149.4, C160). The contract freeze (E2-F2) fixed the shape; lane E2-OP implements the advisor in
// package app (over the engine registry, caps and adoption record) and adds the call sites in this package. A nil advisor
// leaves every bundle byte-identical to the yue2-only behaviour.

// Provenance keys the advisor adds to the bundle's provenance section, in this order, only for a take that has them.
const (
	ProvenanceEngine      = "engine"       // "yue2" | "acestep15" | "dsp" | "unknown"
	ProvenanceTask        = "task"         // acestep15: text2music | repaint | extend | complete | extract
	ProvenanceCrossEngine = "cross_engine" // true when an edit rendered a parent of the other engine
	ProvenanceStems       = "stems"        // names of the take's stem artifacts
	ProvenanceReference   = "reference"    // {track_id, from_s, to_s} of a reference render
)

// EngineAdvice is what the advisor knows about one take.
type EngineAdvice struct {
	Engine      string
	Task        string
	CrossEngine bool
	Stems       []string
	// Actions are engine-aware next actions, already scope-filtered and priority ordered (at most room strings): "extend
	// or repaint this take" only for an adopted engine whose effective caps allow it; never the YuE2 draft editor or
	// lint_recipe for an ACE-Step take; lyric_edit only when the engine's Caps.Tasks lists it.
	Actions []string
	// BlockYue2Actions is true for a take whose engine is not yue2: the builder drops the draft editor and lint_recipe lines.
	BlockYue2Actions bool
}

// EngineAdvisor supplies the engine facts of a take for /context.
type EngineAdvisor interface {
	Advise(ctx context.Context, p auth.Principal, trackID string, room int) (EngineAdvice, error)
}

// UseEngines sets the advisor; nil (the default) keeps /context byte-identical.
func (b *Builder) UseEngines(a EngineAdvisor) { b.engines = a }
