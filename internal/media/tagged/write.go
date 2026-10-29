package tagged

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// Version is hashed into every cache key.
const Version = "mm-dl/1"

const (
	maxValueBytes = 1024
	pictureFront  = 3
	pictureColors = 16 // the covers are 16-colour paletted PNGs (C110.1)
)

// Tags are the values written into the VORBIS_COMMENT block.
type Tags struct {
	Title, Artist, Album, Date, Genre        string // Artist "metamusic"; Album = the root's current title; Date YYYY-MM-DD
	TrackNumber, TrackTotal                  int    // position in the lineage / lineage size
	ReplayGainTrackGain, ReplayGainTrackPeak string
	MM                                       [][2]string // MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_SEED (MM_REFERENCE=1), MM_STATUS
}

// sanitize makes a tag value safe: invalid UTF-8 becomes U+FFFD, C0 controls, DEL and C1 controls become a
// space (as at ingest), the result is trimmed and capped at 1024 bytes on a rune boundary.
func sanitize(s string) string {
	if len(s) <= maxValueBytes && plainASCII(s) {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		switch {
		case r == utf8.RuneError && w == 1:
			r = '�'
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			r = ' '
		}
		if b.Len()+utf8.RuneLen(r) > maxValueBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// plainASCII reports whether s holds only printable ASCII (the common case, which needs no rewriting).
func plainASCII(s string) bool {
	for i := range len(s) {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// comments lists the VORBIS_COMMENT entries in the contractual order; GENRE and the ReplayGain pair are
// omitted when empty.
func (t Tags) comments() []flacmeta.Comment {
	var out []flacmeta.Comment
	add := func(k, v string, omitEmpty bool) {
		if v = sanitize(v); v != "" || !omitEmpty {
			out = append(out, flacmeta.Comment{Key: k, Value: v})
		}
	}
	add("TITLE", t.Title, false)
	add("ARTIST", t.Artist, false)
	add("ALBUM", t.Album, false)
	add("TRACKNUMBER", strconv.Itoa(t.TrackNumber), false)
	add("TRACKTOTAL", strconv.Itoa(t.TrackTotal), false)
	add("DATE", t.Date, false)
	add("GENRE", t.Genre, true)
	add("REPLAYGAIN_TRACK_GAIN", t.ReplayGainTrackGain, true)
	add("REPLAYGAIN_TRACK_PEAK", t.ReplayGainTrackPeak, true)
	for _, kv := range t.MM {
		add(kv[0], kv[1], false)
	}
	return out
}

func vorbisBlock(c []flacmeta.Comment) ([]byte, error) {
	b := flacmeta.VorbisComment("metamusic "+Version, c)
	if len(b) > flacmeta.MaxBlockLen {
		return nil, errors.New("tagged: VORBIS_COMMENT exceeds the block size limit")
	}
	return b, nil
}

func pictureBlock(cover []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(cover))
	if err != nil {
		return nil, fmt.Errorf("tagged: cover is not a PNG: %w", err)
	}
	b := flacmeta.PictureBlock(flacmeta.Picture{
		Type: pictureFront, MIME: "image/png", W: uint32(cfg.Width), H: uint32(cfg.Height), //nolint:gosec // PNG dimensions are bounded by DecodeConfig
		Depth: 8, Colors: pictureColors, Data: cover,
	})
	if len(b) > flacmeta.MaxBlockLen {
		return nil, errors.New("tagged: PICTURE exceeds the block size limit")
	}
	return b, nil
}

// Write copies master (size bytes, described by info) to dst: STREAMINFO and SEEKTABLE verbatim, a new
// VORBIS_COMMENT built from tags, a PICTURE from cover (a PNG; omitted when nil), then the master's audio frames
// byte for byte (C110.2). It returns the number of bytes written.
func Write(dst io.Writer, master io.ReaderAt, size int64, info flacmeta.Info, tags Tags, cover []byte) (int64, error) {
	if len(info.Blocks) == 0 || info.Blocks[0].Type != flacmeta.BlockStreamInfo {
		return 0, errors.New("tagged: master metadata has no STREAMINFO")
	}
	if info.FramesOffset < 4 || info.FramesOffset > size {
		return 0, errors.New("tagged: master frames offset is outside the file")
	}
	var head bytes.Buffer
	head.WriteString("fLaC")
	copyBlock := func(bl flacmeta.Block) error {
		data := make([]byte, bl.Length)
		if n, err := master.ReadAt(data, bl.Offset+4); n < len(data) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("tagged: read master block: %w", err)
		}
		head.Write(flacmeta.BlockHeader(bl.Type, false, len(data)))
		head.Write(data)
		return nil
	}
	if err := copyBlock(info.Blocks[0]); err != nil {
		return 0, err
	}
	for _, bl := range info.Blocks[1:] {
		if bl.Type == flacmeta.BlockSeekTable {
			if err := copyBlock(bl); err != nil {
				return 0, err
			}
			break
		}
	}
	vc, err := vorbisBlock(tags.comments())
	if err != nil {
		return 0, err
	}
	var pic []byte
	if cover != nil {
		if pic, err = pictureBlock(cover); err != nil {
			return 0, err
		}
	}
	head.Write(flacmeta.BlockHeader(flacmeta.BlockVorbisComment, pic == nil, len(vc)))
	head.Write(vc)
	if pic != nil {
		head.Write(flacmeta.BlockHeader(flacmeta.BlockPicture, true, len(pic)))
		head.Write(pic)
	}
	n, err := dst.Write(head.Bytes())
	total := int64(n)
	if err != nil {
		return total, fmt.Errorf("tagged: write: %w", err)
	}
	want := size - info.FramesOffset
	m, err := io.Copy(dst, io.NewSectionReader(master, info.FramesOffset, want))
	total += m
	switch {
	case err != nil:
		return total, fmt.Errorf("tagged: copy frames: %w", err)
	case m != want:
		return total, fmt.Errorf("tagged: copy frames: %w", io.ErrUnexpectedEOF)
	}
	return total, nil
}
