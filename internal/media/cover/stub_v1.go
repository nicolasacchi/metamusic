package cover

import (
	"context"
	"errors"
	"image"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// STUB(L2-V1): the frozen signatures of the mosaic renderer (C123). Lane V1 deletes this file and adds mosaic.go,
// service_mosaic.go and their tests; the signatures do not change.

// RenderMosaic draws in at size x size pixels, deterministically and with integer arithmetic only.
func RenderMosaic(in Input, size int) *image.Paletted { return nil }

// MosaicKey is the cache key of in at size: hex sha256 over MosaicVersion, Kind, ID, Seed, size and the first parts.
func MosaicKey(in Input, size int) string { return "" }

// EnsureMosaic returns the cached cover of in at ClampSize(size) pixels, rendering it on a miss, and its key.
func (s *Service) EnsureMosaic(ctx context.Context, in Input, size int) (serve.Object, string, error) {
	return serve.Object{}, "", errors.ErrUnsupported
}
