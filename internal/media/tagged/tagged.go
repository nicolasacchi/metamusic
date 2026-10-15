// Package tagged writes downloadable FLAC copies carrying the current tags (C110.2, D-M4a-14): STREAMINFO and
// SEEKTABLE copied verbatim, a new VORBIS_COMMENT and PICTURE, then the master's audio frames byte for byte,
// cached input-addressed under derived/dl/.
//
// The exported API is frozen by M4a-B2; the bodies are STUB(M4a-M2) until the media lane implements them.
package tagged

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// Version is hashed into every cache key.
const Version = "mm-dl/1"

// ErrNotImplemented marks a STUB(M4a-M2) body.
var ErrNotImplemented = errors.New("tagged: not implemented")

// Tags are the values written into the VORBIS_COMMENT block.
type Tags struct {
	Title, Artist, Album, Date, Genre        string // Artist "metamusic"; Album = the root's current title; Date YYYY-MM-DD
	TrackNumber, TrackTotal                  int    // position in the lineage / lineage size
	ReplayGainTrackGain, ReplayGainTrackPeak string
	MM                                       [][2]string // MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_SEED (MM_REFERENCE=1), MM_STATUS
}

// Write copies master (size bytes, described by info) to dst with tags and cover. STUB(M4a-M2)
func Write(io.Writer, io.ReaderAt, int64, flacmeta.Info, Tags, []byte) (int64, error) {
	return 0, ErrNotImplemented
}

// Download is one servable tagged copy (or, for a lossy master, the master itself, D-M4a-25).
type Download struct {
	Object   serve.Object
	Key      string
	Filename string
	Bytes    int64
	Tags     Tags
}

// Service builds and caches tagged copies.
type Service struct {
	data   *os.Root
	lib    *library.Service
	covers *cover.Service
	now    func() time.Time
}

// New builds the download service; a nil now means time.Now.
func New(data *os.Root, lib *library.Service, covers *cover.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{data: data, lib: lib, covers: covers, now: now}
}

// Ensure returns the tagged copy of a track, writing it on first use. STUB(M4a-M2)
func (s *Service) Ensure(context.Context, string) (Download, error) {
	return Download{}, ErrNotImplemented
}

// Resolve implements serve.Resolver for ext "flac", files under derived/dl/ only. STUB(M4a-M2): never found.
func (s *Service) Resolve(context.Context, string, string) (serve.Object, error) {
	return serve.Object{}, serve.ErrNotFound
}

// Sweep removes download files older than olderThan and returns how many. STUB(M4a-M2)
func (s *Service) Sweep(context.Context, time.Duration) (int, error) { return 0, nil }
