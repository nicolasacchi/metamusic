// Package cover renders deterministic cover art (C110.1, D-M4a-15): a pure-Go paletted PNG derived from a
// track's lineage root and seed, cached input-addressed under derived/covers/.
//
// The exported API is frozen by M4a-B2. ClampSize and Key are final; the renderer and the service are
// STUB(M4a-M1) until the media lane implements them.
package cover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"os"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// Version is hashed into every cache key; bump it when the algorithm changes.
const Version = "mm-cover/1"

// ErrNotImplemented marks a STUB(M4a-M1) body.
var ErrNotImplemented = errors.New("cover: not implemented")

// ClampSize maps a requested size to a rendered one: n <= 0 gives 512, else n is clamped to [32, 1024] and
// rounded up to a multiple of 32.
func ClampSize(n int) int {
	if n <= 0 {
		return 512
	}
	n = min(max(n, 32), 1024)
	return (n + 31) / 32 * 32
}

// Key is the hex sha256 of Version, root id, seed and size, newline separated.
func Key(rootID string, seed int64, size int) string {
	sum := sha256.Sum256([]byte(Version + "\n" + rootID + "\n" + strconv.FormatInt(seed, 10) + "\n" + strconv.Itoa(size)))
	return hex.EncodeToString(sum[:])
}

// Render draws the cover of (rootID, seed) at size x size pixels. STUB(M4a-M1): a flat 16-colour image.
func Render(_ string, _ int64, size int) *image.Paletted {
	pal := make(color.Palette, 16)
	for i := range pal {
		pal[i] = color.Gray{Y: uint8(i * 17)}
	}
	return image.NewPaletted(image.Rect(0, 0, size, size), pal)
}

// Service renders covers on demand into the file cache.
type Service struct {
	data *os.Root
	lib  *library.Service
	now  func() time.Time
}

// New builds the cover service; a nil now means time.Now.
func New(data *os.Root, lib *library.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{data: data, lib: lib, now: now}
}

// EnsureTrack returns the cover object of a track plus its (root id, seed). STUB(M4a-M1)
func (s *Service) EnsureTrack(context.Context, string, int) (serve.Object, string, error) {
	return serve.Object{}, "", ErrNotImplemented
}

// EnsureAlbum returns the cover of a lineage root (the root track's cover). STUB(M4a-M1)
func (s *Service) EnsureAlbum(context.Context, string, int) (serve.Object, string, error) {
	return serve.Object{}, "", ErrNotImplemented
}

// Resolve implements serve.Resolver for ext "png": existing files only. STUB(M4a-M1): never found.
func (s *Service) Resolve(context.Context, string, string) (serve.Object, error) {
	return serve.Object{}, serve.ErrNotFound
}

// Sweep removes cover files older than olderThan and returns how many. STUB(M4a-M1)
func (s *Service) Sweep(context.Context, time.Duration) (int, error) { return 0, nil }
