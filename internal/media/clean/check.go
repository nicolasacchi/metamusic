package clean

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
)

// ErrLeak is wrapped by every Check failure: the file carries metadata outside the allow-list (or is not the file it
// claims to be) and must never be served.
var ErrLeak = errors.New("clean: metadata outside the allow-list")

func leak(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrLeak, fmt.Sprintf(format, a...))
}

// Check parses the metadata of a built rendition with the stdlib parsers of this repo (no external tool) and refuses
// anything beyond the allow-list: for FLAC the blocks STREAMINFO, SEEKTABLE, PADDING and one VORBIS_COMMENT holding
// only TITLE under this package's vendor string; for MP3 one ID3v2.4 tag of one TIT2 frame, then MPEG frames to the
// last byte (no ID3v1, APE or other trailer).
func Check(kind string, r io.ReaderAt, size int64) error {
	switch kind {
	case KindFLAC:
		return checkFLAC(r, size)
	case KindMP3:
		return checkMP3(r, size)
	}
	return fmt.Errorf("clean: unknown kind %q", kind)
}

func checkFLAC(r io.ReaderAt, size int64) error {
	info, err := flacmeta.Read(r, size)
	if err != nil {
		return fmt.Errorf("clean: read flac: %w", err)
	}
	vorbis := 0
	for _, b := range info.Blocks {
		switch b.Type {
		case flacmeta.BlockStreamInfo, flacmeta.BlockSeekTable, flacmeta.BlockPadding:
		case flacmeta.BlockVorbisComment:
			vorbis++
		default:
			return leak("metadata block type %d", b.Type)
		}
	}
	if vorbis > 1 {
		return leak("%d VORBIS_COMMENT blocks", vorbis)
	}
	if vorbis == 1 && info.Vendor != Vendor {
		return leak("vendor string")
	}
	for _, c := range info.Comments {
		k, _, _ := strings.Cut(c, "=")
		if !strings.EqualFold(k, "TITLE") {
			return leak("comment key %q", keyOf(k))
		}
	}
	return nil
}

// keyOf bounds a comment key quoted in an error (the value is never quoted).
func keyOf(k string) string {
	if len(k) > 32 {
		return k[:32]
	}
	return k
}

func checkMP3(r io.ReaderAt, size int64) error {
	var hdr [10]byte
	tagEnd := int64(0)
	if n, _ := r.ReadAt(hdr[:], 0); n == len(hdr) && string(hdr[:3]) == "ID3" {
		if hdr[3] != 4 || hdr[4] != 0 || hdr[5] != 0 {
			return leak("ID3 version or flags")
		}
		body, ok := syncsafe(hdr[6:10])
		if !ok || 10+body > size {
			return leak("ID3 size")
		}
		buf := make([]byte, body)
		if n, _ := r.ReadAt(buf, 10); int64(n) != body {
			return leak("ID3 truncated")
		}
		for len(buf) > 0 {
			if len(buf) < 10 {
				return leak("ID3 padding")
			}
			id := string(buf[:4])
			n, ok := syncsafe(buf[4:8])
			if !ok || buf[8] != 0 || buf[9] != 0 || 10+n > int64(len(buf)) {
				return leak("ID3 frame header")
			}
			if id != "TIT2" {
				return leak("ID3 frame %q", id)
			}
			buf = buf[10+n:]
		}
		tagEnd = 10 + body
	}
	start, end, err := mp3info.Span(r, size)
	if err != nil {
		return fmt.Errorf("clean: read mp3: %w", err)
	}
	if start != tagEnd {
		return leak("%d bytes between the tag and the first frame", start-tagEnd)
	}
	if end != size {
		return leak("%d bytes after the last frame", size-end)
	}
	return nil
}

func syncsafe(b []byte) (int64, bool) {
	if b[0]|b[1]|b[2]|b[3] >= 0x80 {
		return 0, false
	}
	return int64(b[0])<<21 | int64(b[1])<<14 | int64(b[2])<<7 | int64(b[3]), true
}
