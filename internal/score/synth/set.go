package synth

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// maxSoundfontBytes bounds a soundfont file (2 GiB); a variable so a test can lower it.
var maxSoundfontBytes int64 = 2 << 30

// Font is one configured soundfont: the operator's name and the absolute file path (the field layout of config.Soundfont, so
// synth.Font(sf) converts it without this package importing config).
type Font struct{ Name, Path string }

type fontEntry struct {
	path, sha string
	size      int64
}

// Set is the soundfonts that were usable at startup, in configured order. It is immutable.
type Set struct {
	names []string
	by    map[string]fontEntry
}

// NewSet validates and hashes fonts: absolute regular files of at most 2 GiB, hashed by streaming. A missing, unreadable,
// non-regular, relative or oversized file disables that name only and is logged without its path.
func NewSet(fonts []Font, log *slog.Logger) *Set {
	s := &Set{by: map[string]fontEntry{}}
	for _, f := range fonts {
		e, reason := hashFont(f.Path)
		if reason != "" {
			if log != nil {
				log.Warn("soundfont disabled", "name", f.Name, "reason", reason)
			}
			continue
		}
		if _, dup := s.by[f.Name]; dup {
			continue
		}
		s.names = append(s.names, f.Name)
		s.by[f.Name] = e
	}
	return s
}

func hashFont(path string) (fontEntry, string) {
	if !filepath.IsAbs(path) {
		return fontEntry{}, "path is not absolute"
	}
	fh, err := os.Open(path) // #nosec G304 -- operator-configured soundfont path (MM_SOUNDFONTS)
	if err != nil {
		return fontEntry{}, "cannot open file"
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return fontEntry{}, "not a regular file"
	}
	if st.Size() > maxSoundfontBytes {
		return fontEntry{}, "file too large"
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(fh, maxSoundfontBytes+1))
	if err != nil {
		return fontEntry{}, "cannot read file"
	}
	if n > maxSoundfontBytes {
		return fontEntry{}, "file too large"
	}
	return fontEntry{path: path, sha: hex.EncodeToString(h.Sum(nil)), size: n}, ""
}

// Names lists the usable soundfont names in configured order.
func (s *Set) Names() []string { return append([]string(nil), s.names...) }

// Lookup returns a usable soundfont's path, sha256 (hex) and size in bytes.
func (s *Set) Lookup(name string) (path, sha256hex string, bytes int64, ok bool) {
	e, ok := s.by[name]
	if !ok {
		return "", "", 0, false
	}
	return e.path, e.sha, e.size, true
}
