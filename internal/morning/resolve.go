package morning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Resolve serves a side by the sha256 of its file (C380). Only a side that has a row, whose path is under derived/morning/, whose file
// is not marked removed and still exists answers; every other (sha, ext) is serve.ErrNotFound so the media chain falls through. A
// master sha is never answered here: sides and masters are distinct files.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	if ext != "flac" || s.o.Store == nil || len(sha) != 64 {
		return serve.Object{}, serve.ErrNotFound
	}
	var rel, built string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT s.path, COALESCE(c.built_at, c.created_at) FROM morning_crate_sides s JOIN morning_crates c ON c.id = s.crate_id
		WHERE s.sha256 = ? AND s.removed_at IS NULL ORDER BY c.created_at DESC, c.id DESC LIMIT 1`, sha).Scan(&rel, &built)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, fmt.Errorf("morning: resolve: %w", err)
	}
	if !strings.HasPrefix(rel, morningDir+"/") || strings.Contains(rel, "..") {
		return serve.Object{}, serve.ErrNotFound
	}
	if s.o.Data != nil {
		if _, err := s.o.Data.Stat(rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return serve.Object{}, serve.ErrNotFound
			}
			return serve.Object{}, fmt.Errorf("morning: stat: %w", err)
		}
	}
	mt, _ := store.ParseInstant(built)
	if mt.IsZero() {
		mt = time.Unix(0, 0)
	}
	return serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: sha, ModTime: mt}, nil
}
