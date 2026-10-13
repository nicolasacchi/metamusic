package mp3info

import (
	"bytes"
	"testing"
)

// FuzzMp3info: Parse never panics and never reads more than 128 KiB.
func FuzzMp3info(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xFF, 0xFB, 0x90, 0x00})
	f.Add(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x10"), bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 200)...))
	f.Add(bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x64}, 300))
	f.Fuzz(func(t *testing.T, b []byte) {
		c := &countingReaderAt{b: b}
		info, err := Parse(c, int64(len(b)))
		if got := c.total.Load(); got > 128<<10 {
			t.Fatalf("read %d bytes", got)
		}
		if err == nil && (info.Channels < 1 || info.Channels > 2 || info.SampleRate <= 0 || info.BitrateKbps <= 0) {
			t.Fatalf("bad info %+v", info)
		}
	})
}
