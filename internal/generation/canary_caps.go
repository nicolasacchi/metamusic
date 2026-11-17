package generation

import (
	"context"
	"log/slog"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// The canary-only capability switch (MM_ACESTEP_CANARY_CAPS). The real-GPU canary is the only way to verify an ACE-Step
// capability, so a batch made entirely of canary requests is planned against the verified features plus the switched-on
// capabilities. Every other batch sees the engine exactly as registered, and the registry itself is never changed:
// list_engines and the SPA keep reporting those capabilities as unverified.

// canarySwitch is what a canary batch was planned with.
type canarySwitch struct {
	caps     []string         // the switched-on capability names
	verified acestep.Features // the registered (verified) features, to tell which switched capability a take really uses
}

// canaryEngine returns the engine to plan the batch with. It is eng itself, and the switch nil, unless the switch is set, the
// engine is ACE-Step and every item is a canary request.
func (s *Service) canaryEngine(ctx context.Context, q lineage.Querier, eng engine.Engine, items []Item) (engine.Engine, *canarySwitch) {
	if len(s.o.ACEStepCanaryCaps) == 0 || len(items) == 0 {
		return eng, nil
	}
	ae, ok := eng.(acestep.Engine)
	if !ok {
		return eng, nil
	}
	for _, it := range items {
		if !acestep.IsCanaryID(itemRequestID(ctx, q, it)) {
			return eng, nil
		}
	}
	sw := &canarySwitch{caps: s.o.ACEStepCanaryCaps, verified: ae.Verified}
	ae.Verified = ae.Verified.WithCanary(sw.caps, s.o.ACEStepCanaryBase)
	return ae, sw
}

// itemRequestID is the request id an item's takes derive from: the id of a compose request, else the request id of the
// parent take. Anything else (drafts, templates, unknown parents) has none and so is never a canary item.
func itemRequestID(ctx context.Context, q lineage.Querier, it Item) string {
	switch {
	case len(it.Recipe) > 0:
		return recipeIDOf(it.Recipe)
	case it.ParentTrackID != nil && *it.ParentTrackID != "":
		t, ok, err := loadTrack(ctx, q, *it.ParentTrackID)
		if err != nil || !ok {
			return ""
		}
		return recipeIDOf([]byte(t.RequestJSON))
	}
	return ""
}

// usedCaps names the switched-on capabilities a planned take uses, for the audit trail.
func (sw *canarySwitch) usedCaps(r acestep.Request) []string {
	if sw == nil {
		return nil
	}
	return sw.verified.CanaryCapsUsed(r, sw.caps)
}

// logCanaryCaps leaves one line per canary take that uses a switched-on capability (no request body, no secret).
func logCanaryCaps(requestID string, caps []string) {
	for _, c := range caps {
		slog.Warn("canary capability used", "canary_cap", c, "request_id", requestID)
	}
}
