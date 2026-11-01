package app

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// L3Options tunes the L3 services of Build (C133); every field is optional.
type L3Options struct {
	// RealFeatures wires the features service into the Reconciler's phase B and the sounds recompute hook. serve sets it;
	// rigs and the CLI leave it off (the no-op step) unless a test asks for it (servertest.Options.RealFeatures).
	RealFeatures bool
}

// l3Services are the L3 services Build constructs (C133). Features has no operation; it feeds the Reconciler, the sounds
// recompute hook and the readers. Taxonomy proposals live on the existing Sounds service.
type l3Services struct {
	Features    *features.Service
	Sequence    *sequence.Service
	NextActions *nextactions.Service
	Atlas       *atlas.Service
}

// l3Inputs is what the L3 wiring files draw on.
type l3Inputs struct {
	st       *store.Store
	now      func() time.Time
	muses    *muses.Service
	songview *songview.Service
	sounds   *sounds.Service
	prov     *provenance.Service
	ctxb     *trackctx.Builder
	o        L3Options
}

// buildL3 constructs the L3 services and wires their seams; each lane fills only its own l3_<lane>.go (C133 hot-spot
// rules). Nothing is started: features are drained by the L1 Run loop.
func buildL3(in l3Inputs) *l3Services {
	s := &l3Services{}
	buildL3Features(in, s)
	buildL3Seq(in, s)
	buildL3NextActions(in, s)
	buildL3Taxonomy(in, s)
	buildL3Atlas(in, s)
	return s
}
