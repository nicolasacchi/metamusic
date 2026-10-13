package clip

import (
	"context"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

type cacheFile struct {
	rel, key string
	size     int64
	mod      time.Time
}

// evict trims derived/clips/ when it exceeds the cap (C46 step 4): oldest-mtime
// unpinned files go first, down to 90 % of the cap. Keys of open duels (Pinned)
// and of renders in flight are kept; a Pinned error skips eviction. The cap is
// soft: pinned files alone may exceed it. Errors are logged, never returned.
func (s *Service) evict(ctx context.Context) {
	var files []cacheFile
	var total int64
	err := fs.WalkDir(s.data.FS(), clipsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(p, ".flac") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // vanished mid-walk
		}
		name := p[strings.LastIndexByte(p, '/')+1:]
		files = append(files, cacheFile{p, strings.TrimSuffix(name, ".flac"), info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		if !isNotExist(err) {
			slog.Warn("clip cache walk failed", "err", redact.Error(err))
		}
		return
	}
	if total <= s.opts.CacheBytes {
		return
	}
	pinned := map[string]struct{}{}
	if s.opts.Pinned != nil {
		p, err := s.opts.Pinned(ctx)
		if err != nil {
			slog.Warn("clip cache: pinned keys unavailable, skipping eviction", "err", redact.Error(err))
			return
		}
		for k := range p {
			pinned[k] = struct{}{}
		}
	}
	s.mu.Lock()
	for k := range s.flights {
		pinned[k] = struct{}{}
	}
	s.mu.Unlock()
	slices.SortFunc(files, func(a, b cacheFile) int {
		if c := a.mod.Compare(b.mod); c != 0 {
			return c
		}
		return strings.Compare(a.key, b.key)
	})
	goal := s.opts.CacheBytes / 10 * 9
	for _, f := range files {
		if total <= goal {
			break
		}
		if _, ok := pinned[f.key]; ok {
			continue
		}
		if err := s.data.Remove(f.rel); err != nil && !isNotExist(err) {
			slog.Warn("clip cache: remove failed", "err", redact.Error(err))
			continue
		}
		total -= f.size
	}
}
