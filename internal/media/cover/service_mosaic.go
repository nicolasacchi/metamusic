package cover

import (
	"context"
	"image"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// EnsureMosaic returns the cached mosaic cover of in at ClampSize(size) pixels, rendering it on a miss, and its key. It shares
// the write path (and so Resolve and Sweep) with EnsureTrack.
func (s *Service) EnsureMosaic(ctx context.Context, in Input, size int) (serve.Object, string, error) {
	if err := ctx.Err(); err != nil {
		return serve.Object{}, "", err
	}
	size = ClampSize(size)
	return s.ensureImage(MosaicKey(in, size), func() *image.Paletted { return RenderMosaic(in, size) })
}
