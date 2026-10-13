// Package id3 writes the ID3v2.4 tag of an exported MP3 (C33). It only encodes: the
// audio bytes that follow the tag are never touched.
package id3

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// Tag is the content of one exported MP3's tag.
type Tag struct {
	Title, Artist, Album, Track string
	Stars                       int // 0 = none
	TXXX                        [][2]string
}

var popmRating = map[int]byte{1: 1, 2: 64, 3: 128, 4: 196, 5: 255}

// clean replaces invalid UTF-8 with U+FFFD and controls (C0, DEL, C1) with a space.
func clean(s string) string {
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
		b.WriteRune(r)
	}
	return b.String()
}

func synchsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
}

func mkFrame(id string, data []byte) []byte {
	out := make([]byte, 0, 10+len(data))
	out = append(out, id...)
	out = append(out, synchsafe(len(data))...)
	out = append(out, 0, 0)
	return append(out, data...)
}

func textFrame(id, v string) []byte { return mkFrame(id, append([]byte{0x03}, clean(v)...)) }

// Encode returns an ID3v2.4 tag: UTF-8 text frames (encoding 0x03), synchsafe sizes, no padding and no
// unsynchronisation. Frames: TIT2, TPE1, TALB, TRCK, POPM (only when Stars is 1-5), then one TXXX per entry.
func Encode(t Tag) []byte {
	var body bytes.Buffer
	body.Write(textFrame("TIT2", t.Title))
	body.Write(textFrame("TPE1", t.Artist))
	body.Write(textFrame("TALB", t.Album))
	body.Write(textFrame("TRCK", t.Track))
	if r, ok := popmRating[t.Stars]; ok {
		body.Write(mkFrame("POPM", append([]byte("metamusic\x00"), r)))
	}
	for _, kv := range t.TXXX {
		d := append([]byte{0x03}, clean(kv[0])...)
		d = append(d, 0)
		d = append(d, clean(kv[1])...)
		body.Write(mkFrame("TXXX", d))
	}
	out := make([]byte, 0, 10+body.Len())
	out = append(out, 'I', 'D', '3', 4, 0, 0)
	out = append(out, synchsafe(body.Len())...)
	return append(out, body.Bytes()...)
}
