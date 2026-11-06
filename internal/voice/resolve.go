package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

var _ serve.Resolver = (*Service)(nil)

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

// Resolve serves the MP3 rendition of a stored voice note. Anything else is not found: the uploaded original is never served,
// and an address that is not a registered rendition is unknown even when a file with that name exists.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	if s == nil || s.o.Store == nil || s.o.Data == nil || ext != "mp3" || !isKey(sha) {
		return serve.Object{}, serve.ErrNotFound
	}
	var rel string
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT mp3_path FROM voice_notes WHERE mp3_sha256 = ? LIMIT 1`, sha).Scan(&rel); {
	case errors.Is(err, sql.ErrNoRows):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("voice: resolve: %w", err)
	}
	st, err := s.o.Data.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return serve.Object{}, serve.ErrNotFound
	case err != nil:
		return serve.Object{}, fmt.Errorf("voice: resolve: %w", err)
	case !st.Mode().IsRegular():
		return serve.Object{}, serve.ErrNotFound
	}
	return serve.Object{RelPath: rel, ContentType: "audio/mpeg", ETag: sha, ModTime: st.ModTime()}, nil
}

// VoiceURL is the signed playback link of a rendition for the calling principal (judgments.VoiceLinker).
func (s *Service) VoiceURL(ctx context.Context, mp3SHA256 string) string {
	if s == nil || s.o.Signer == nil {
		return ""
	}
	ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
	return s.o.Signer.Sign(mp3SHA256, "mp3", ttl, bucket).URL
}
