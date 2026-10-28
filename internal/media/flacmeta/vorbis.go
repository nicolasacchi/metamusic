package flacmeta

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// MaxBlockLen is the largest payload of one metadata block: the length field is 24 bits.
const MaxBlockLen = 1<<24 - 1

// u32 converts a length that the callers bound (a block is at most MaxBlockLen bytes) to a uint32.
func u32(n int) uint32 { return uint32(n) } //nolint:gosec // bounded by the callers

// BlockHeader returns the 4-byte header of a metadata block of type typ and payload length n; last sets the last-block flag.
func BlockHeader(typ BlockType, last bool, n int) []byte {
	b := byte(typ)
	if last {
		b |= 0x80
	}
	return []byte{b, byte(n >> 16 & 0xff), byte(n >> 8 & 0xff), byte(n & 0xff)}
}

// ValidKey reports whether k is a legal Vorbis comment field name: non-empty, ASCII 0x20 to 0x7D, no '='.
func ValidKey(k string) bool {
	if k == "" {
		return false
	}
	for i := range len(k) {
		if c := k[i]; c < 0x20 || c > 0x7d || c == '=' {
			return false
		}
	}
	return true
}

// ValidateComments checks every key with ValidKey and every value for valid UTF-8 without NUL.
func ValidateComments(c []Comment) error {
	for i, e := range c {
		if !ValidKey(e.Key) {
			return fmt.Errorf("flacmeta: comment %d has an invalid key", i)
		}
		if !utf8Valid(e.Value) {
			return fmt.Errorf("flacmeta: comment %d (%s) has an invalid value", i, e.Key)
		}
	}
	return nil
}

func utf8Valid(s string) bool {
	return strings.ToValidUTF8(s, "\x00") == s && !strings.ContainsRune(s, 0)
}

// VorbisComment builds the payload of a VORBIS_COMMENT block (type 4): the vendor string, then one "KEY=value" entry per
// comment, in order. Values are written byte for byte (an '=' in a value is not escaped, there is nothing to escape). The
// caller validates (ValidateComments) and checks the result against MaxBlockLen.
func VorbisComment(vendor string, c []Comment) []byte {
	n := 8 + len(vendor)
	for _, e := range c {
		n += 4 + len(e.Key) + 1 + len(e.Value)
	}
	b := make([]byte, 0, n)
	b = binary.LittleEndian.AppendUint32(b, u32(len(vendor)))
	b = append(b, vendor...)
	b = binary.LittleEndian.AppendUint32(b, u32(len(c)))
	for _, e := range c {
		b = binary.LittleEndian.AppendUint32(b, u32(len(e.Key)+1+len(e.Value)))
		b = append(b, e.Key...)
		b = append(b, '=')
		b = append(b, e.Value...)
	}
	return b
}
