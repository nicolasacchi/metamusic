package synth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Retag copies src to dst with its tags replaced by comments ("KEY=value");
// the audio frames are unchanged, so the audio identity stays the same.
func Retag(ctx context.Context, t mediatool.Tools, src, dst string, comments []string) error {
	if err := needAbs(src); err != nil {
		return err
	}
	if err := needAbs(dst); err != nil {
		return err
	}
	for _, c := range comments {
		if !strings.Contains(c, "=") || strings.ContainsAny(c, "\r\n\x00") {
			return errors.New("synth: comment must be a single KEY=value line")
		}
	}
	tmp := dst + ".tmp"
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	err := run(ctx, t.Metaflac, "--remove-all-tags", tmp)
	if err == nil && len(comments) > 0 {
		tags := tmp + ".tags.txt"
		defer func() { _ = os.Remove(tags) }()
		if err = os.WriteFile(tags, []byte(strings.Join(comments, "\n")+"\n"), 0o600); err == nil {
			err = run(ctx, t.Metaflac, "--no-utf8-convert", "--import-tags-from="+tags, tmp)
		}
	}
	return publish(tmp, dst, err)
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) // #nosec G304 -- caller-supplied absolute path, validated by needAbs
	if err != nil {
		return fmt.Errorf("synth: opening source: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) // #nosec G302 G304 -- synthetic test audio; world-readable is fine
	if err != nil {
		return fmt.Errorf("synth: creating %s: %w", filepath.Base(dst), err)
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
