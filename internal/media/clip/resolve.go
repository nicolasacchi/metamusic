package clip

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

func isKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Resolve serves rendered clips ("flac" keys under derived/clips/); unknown keys are not found.
func (s *Service) Resolve(_ context.Context, sha, ext string) (serve.Object, error) {
	if ext != "flac" || !isKey(sha) {
		return serve.Object{}, serve.ErrNotFound
	}
	rel := relPath(sha)
	st, err := s.data.Lstat(rel)
	switch {
	case isNotExist(err):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("clip: resolve: %w", err)
	case !st.Mode().IsRegular():
		return serve.Object{}, serve.ErrNotFound
	}
	return serve.Object{RelPath: rel, ContentType: "audio/flac", ETag: sha, ModTime: st.ModTime()}, nil
}
