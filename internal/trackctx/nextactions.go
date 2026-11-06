package trackctx

import (
	"context"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// NextActionReader supplies the Muse-aware next-action strings of one take (L3 C139.4). The strings are already
// scope-filtered, rendered and ordered by priority, and each has priority at least nextactions.ContextMuseMinPriority.
// trackctx imports no L3 package; app adapts *nextactions.Service. The reader applies the priority floor and returns at most limit strings; the assembly below owns the room arithmetic (lane NA1).
type NextActionReader interface {
	ForTake(ctx context.Context, p auth.Principal, trackID string, limit int) ([]string, error)
}

// UseNextActions sets the reader; nil (the default) leaves /context byte-identical to the M3 behaviour.
func (b *Builder) UseNextActions(r NextActionReader) { b.nextact = r }

// contextMuseMax mirrors nextactions.ContextMuseMax (trackctx may not import it).
const contextMuseMax = 2

// museAwareActions appends up to min(contextMuseMax, maxNextActions - len(na) - 1) strings after the base rules; the -1
// reserves the critique line. A missing reader, no room, or a reader that fails (including one not implemented yet) leave na unchanged.
func (b *Builder) museAwareActions(ctx context.Context, p auth.Principal, trackID string, na []string) []string {
	if b.nextact == nil {
		return na
	}
	room := min(contextMuseMax, maxNextActions-len(na)-1)
	if room <= 0 {
		return na
	}
	extra, err := b.nextact.ForTake(ctx, p, trackID, room)
	if err != nil {
		return na // next actions are advice: a failing or not yet implemented reader never fails /context
	}
	if len(extra) > room {
		extra = extra[:room]
	}
	return append(na, extra...)
}

// ---- E2 block: ACE-Step next actions (C160, C149.4; lane E2-OP) -------------------------------------------------------------
// Everything below is the ACE rules and nothing else, so M5-A1 and the L3 rules merge around it.

// ACERuleInput is what the ACE-Step rules need: the facts the app's advisor reads (adoption, effective caps, the caller's scopes).
type ACERuleInput struct {
	Adopted      bool // the engine's latest adoption row is adopted (D-E2-15)
	CanGenerate  bool // the caller holds the generate scope
	RepaintOK    bool // Caps.RepaintInterval and "repaint" in Caps.Tasks
	ExtendOK     bool // Caps.Extend and "extend" in Caps.Tasks
	LyricEditOK  bool // "lyric_edit" in Caps.Tasks
	ReferenceOK  bool // Caps.ReferenceAudio and "reference" in Caps.Tasks
	TaskIsRender bool // true for any ACE-Step take; false for a take of another engine (no rule fires)
}

const (
	aceExtendRepaintHint = "extend or repaint this take (estimate_generation mode=extend or mode=repaint; the estimate is free, spending needs create_generation)"
	aceExtendHint        = "extend this take (estimate_generation mode=extend; the estimate is free, spending needs create_generation)"
	aceRepaintHint       = "repaint a section of this take (estimate_generation mode=repaint; the estimate is free, spending needs create_generation)"
	aceLyricEditHint     = "rewrite the lyrics and keep the style (estimate_variant mode=lyric_edit; the estimate is free)"
	aceReferenceHint     = "render a new song that follows this take's sound (estimate_generation mode=reference; the estimate is free)"
)

// ACENextActions are the engine-aware next actions of an ACE-Step take, highest priority first. "extend or repaint" appears only
// for an adopted engine with the capability on and a caller who can generate; lyric_edit only when the engine's Caps.Tasks lists
// it (C149.4). The YuE2 draft editor and lint_recipe are never suggested for an ACE-Step take (see dropYuE2Actions).
func ACENextActions(in ACERuleInput) []string {
	if !in.TaskIsRender || !in.Adopted || !in.CanGenerate {
		return nil
	}
	var out []string
	switch {
	case in.ExtendOK && in.RepaintOK:
		out = append(out, aceExtendRepaintHint)
	case in.ExtendOK:
		out = append(out, aceExtendHint)
	case in.RepaintOK:
		out = append(out, aceRepaintHint)
	}
	if in.LyricEditOK {
		out = append(out, aceLyricEditHint)
	}
	if in.ReferenceOK {
		out = append(out, aceReferenceHint)
	}
	return out
}

// engineAdvice asks the advisor about one take; a missing advisor or a failing one gives the zero advice (advice never fails
// /context), which leaves the bundle byte-identical to the yue2-only behaviour.
func (b *Builder) engineAdvice(ctx context.Context, p auth.Principal, trackID string) EngineAdvice {
	if b.engines == nil {
		return EngineAdvice{}
	}
	adv, err := b.engines.Advise(ctx, p, trackID, maxNextActions)
	if err != nil {
		return EngineAdvice{}
	}
	return adv
}

// engineProvenance adds the engine keys to the provenance section, only for a take the advisor has facts for.
func engineProvenance(prov map[string]any, adv EngineAdvice) {
	if adv.Engine == "" {
		return
	}
	prov[ProvenanceEngine] = adv.Engine
	if adv.Task != "" {
		prov[ProvenanceTask] = adv.Task
	}
	if adv.CrossEngine {
		prov[ProvenanceCrossEngine] = true
	}
	if len(adv.Stems) > 0 {
		prov[ProvenanceStems] = adv.Stems
	}
}

// dropYuE2Actions removes the YuE2-only lines (draft editor, lint_recipe) from the list.
func dropYuE2Actions(na []string) []string {
	out := na[:0:0]
	for _, a := range na {
		if strings.Contains(a, "lint_recipe") || strings.Contains(a, "draft editor") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// engineAwareActions applies the advice after the base and Muse-aware rules: YuE2-only lines are dropped for a take of another
// engine, then the advisor's actions fill the room that is left (the critique line keeps the last slot).
func engineAwareActions(na []string, adv EngineAdvice) []string {
	if adv.BlockYue2Actions {
		na = dropYuE2Actions(na)
	}
	room := maxNextActions - len(na) - 1
	if room <= 0 || len(adv.Actions) == 0 {
		return na
	}
	return append(na, adv.Actions[:min(room, len(adv.Actions))]...)
}
