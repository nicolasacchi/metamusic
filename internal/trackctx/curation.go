package trackctx

import "context"

// CurationReader is what trackctx needs of the curation services (C130.4): the Records and Crates one take belongs to.
// trackctx declares it so it imports none of records, crates or recordview (the Muse-visibility rule); app.Build adapts
// the services to it. A nil reader (the default) leaves the section absent.
type CurationReader interface {
	ForTrack(ctx context.Context, trackID string) (map[string]any, error)
}

// UseCuration wires the curation reader. A nil reader leaves the `curation` section out of every bundle; the reader returns
// nil for a take that is in no Record and no Crate.
func (b *Builder) UseCuration(r CurationReader) { b.curation = r }

// curationSection is the nil-safe read of the section: nil without a reader or when the take has nothing to report.
func (b *Builder) curationSection(ctx context.Context, trackID string) (map[string]any, error) {
	if b.curation == nil {
		return nil, nil
	}
	return b.curation.ForTrack(ctx, trackID)
}
