package trackctx

import "context"

// LibraryReader is what trackctx needs of the L1 library services (C98): the Song, Muse and Sound sections of one
// take. trackctx declares it so it imports none of muses, songs, picks or sounds (the Muse-visibility rule, C99.1);
// app.Build adapts the L1 services to it. The name avoids the existing Bundle.Provenance section.
type LibraryReader interface {
	ForTrack(ctx context.Context, trackID string) (LibrarySections, error)
}

// LibrarySections are the three sections /context gains after `track`; each is nil when the take has no Song yet
// (the queue is pending) or the services are absent.
type LibrarySections struct {
	Song, Muse, Sounds map[string]any
}

// UseLibrary wires the L1 reader. A nil reader (the default) leaves the three sections null.
func (b *Builder) UseLibrary(r LibraryReader) { b.lib1 = r }
