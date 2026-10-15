package cover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

const coversDir = "derived/covers"

func relPath(key string) string { return coversDir + "/" + key[:2] + "/" + key + ".png" }

func isKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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

// EnsureTrack returns the cover object of a track plus its cache key. The cover is drawn from the track's lineage
// root and its recipe seed, at ClampSize(size) pixels. An unknown track is library.ErrNotFound.
func (s *Service) EnsureTrack(ctx context.Context, trackID string, size int) (serve.Object, string, error) {
	refs, err := s.lib.TrackRefs(ctx, []string{trackID})
	if err != nil {
		return serve.Object{}, "", err
	}
	ref, ok := refs[trackID]
	if !ok {
		return serve.Object{}, "", library.ErrNotFound
	}
	return s.ensure(ref.RootTrackID, ref.Seed, size)
}

// EnsureAlbum returns the cover of a lineage root: the root track's own cover.
func (s *Service) EnsureAlbum(ctx context.Context, rootID string, size int) (serve.Object, string, error) {
	return s.EnsureTrack(ctx, rootID, size)
}

func (s *Service) ensure(rootID string, seed int64, size int) (serve.Object, string, error) {
	size = ClampSize(size)
	key := Key(rootID, seed, size)
	if obj, err := s.Resolve(context.Background(), key, "png"); err == nil {
		return obj, key, nil
	} else if !errors.Is(err, serve.ErrNotFound) {
		return serve.Object{}, "", err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, Render(rootID, seed, size)); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: encode: %w", err)
	}
	if err := s.data.MkdirAll("incoming", 0o755); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: create incoming: %w", err)
	}
	work := "incoming/cover-" + uuid.NewV7().String()
	if err := s.data.Mkdir(work, 0o755); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: create work dir: %w", err)
	}
	defer func() { _ = s.data.RemoveAll(work) }()
	tmp := work + "/cover.png"
	if err := s.data.WriteFile(tmp, buf.Bytes(), 0o444); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: write: %w", err)
	}
	// the sweep ages a cover by its mtime, which is the creation time on the service clock
	if err := s.data.Chtimes(tmp, s.now(), s.now()); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: stamp: %w", err)
	}
	dst := relPath(key)
	if err := s.data.MkdirAll(path.Dir(dst), 0o755); err != nil {
		return serve.Object{}, "", fmt.Errorf("cover: create cache dir: %w", err)
	}
	// Link claims the name atomically; EEXIST means a concurrent call stored the identical content.
	if err := s.data.Link(tmp, dst); err != nil && !errors.Is(err, fs.ErrExist) {
		return serve.Object{}, "", fmt.Errorf("cover: link into place: %w", err)
	}
	obj, err := s.Resolve(context.Background(), key, "png")
	return obj, key, err
}

// Resolve implements serve.Resolver for ext "png": existing files only. The ETag is the key (the file is a pure
// function of it).
func (s *Service) Resolve(_ context.Context, key, ext string) (serve.Object, error) {
	if ext != "png" || !isKey(key) {
		return serve.Object{}, serve.ErrNotFound
	}
	rel := relPath(key)
	st, err := s.data.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("cover: resolve: %w", err)
	case !st.Mode().IsRegular():
		return serve.Object{}, serve.ErrNotFound
	}
	return serve.Object{RelPath: rel, ContentType: "image/png", ETag: key, ModTime: st.ModTime()}, nil
}

// Sweep removes cover files whose modification time is older than olderThan and returns how many. The next
// request recreates a swept cover under the same key.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := s.now().Add(-olderThan)
	var stale []string
	err := fs.WalkDir(s.data.FS(), coversDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() || !strings.HasSuffix(p, ".png") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			stale = append(stale, p)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("cover: sweep: %w", err)
	}
	n := 0
	for _, p := range stale {
		switch err := s.data.Remove(p); {
		case err == nil:
			n++
			_ = s.data.Remove(path.Dir(p)) // the prefix directory, only when now empty
		case errors.Is(err, fs.ErrNotExist):
		default:
			return n, fmt.Errorf("cover: sweep %s: %w", p, err)
		}
	}
	return n, nil
}
