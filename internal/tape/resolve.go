package tape

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// Resolve serves a ready tape by the sha256 of its file (C128.7); any other sha, an expired tape or a missing file is
// serve.ErrNotFound, so the media chain falls through to the next resolver. Master and tape shas are distinct by
// construction, so this resolver and the library's never answer the same address.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	if ext != "flac" || s.o.Store == nil || len(sha) != 64 {
		return serve.Object{}, serve.ErrNotFound
	}
	var rel, name, fin string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT path, filename, finished_at FROM tapes WHERE sha256 = ? AND state = 'ready'
		ORDER BY finished_at DESC, id DESC LIMIT 1`, sha).Scan(&rel, &name, &fin)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, fmt.Errorf("tape: resolve: %w", err)
	}
	if s.o.Data != nil {
		if _, err := s.o.Data.Stat(rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return serve.Object{}, serve.ErrNotFound
			}
			return serve.Object{}, fmt.Errorf("tape: stat: %w", err)
		}
	}
	mt, _ := time.Parse(time.RFC3339Nano, fin)
	return serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: sha, ModTime: mt, DownloadName: name}, nil
}
