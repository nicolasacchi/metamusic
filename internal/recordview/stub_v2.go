package recordview

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// STUB(L2-V2): the frozen method set of C124.2. Lane V2 deletes this file and adds get.go, snapshot.go, cover.go, energy.go
// and their tests; no signature changes. Every stub answers errors.ErrUnsupported, which the API maps to 501.

// Get assembles a Record for viewer. Muse text enters the View only through the liner, and only when o.Include has
// IncludeLiner, which only get_record passes.
func (s *Service) Get(ctx context.Context, viewer auth.Principal, id string, o GetOptions) (View, error) {
	return View{}, errors.ErrUnsupported
}

// Snapshot builds the tape input: resolved Takes and masters, slot warnings, loudness, the export-audience liner, the tag
// set and the cover input. It reads Muse text only when curation.export_muse_names is on.
func (s *Service) Snapshot(ctx context.Context, recordID string, o tape.SnapOptions) (tape.Snapshot, error) {
	return tape.Snapshot{}, errors.ErrUnsupported
}

// CoverInput builds the mosaic input of a Record: its id and seed, and the lineage of the first CoverParts slot Songs.
func (s *Service) CoverInput(ctx context.Context, recordID string) (cover.Input, error) {
	return cover.Input{}, errors.ErrUnsupported
}

// CoverRefs returns the signed cover links of Records for the list rows, in one batch.
func (s *Service) CoverRefs(ctx context.Context, viewer auth.Principal, ids []string) (map[string]CoverRef, error) {
	return nil, errors.ErrUnsupported
}
