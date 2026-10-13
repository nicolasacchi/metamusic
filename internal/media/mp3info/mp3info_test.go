package mp3info

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

var (
	// bitrate tables (kbps) by index, as in the standard; 0 and 15 are invalid.
	brV1L3 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	brV2L3 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
)

// frameHdr builds a 4-byte frame frameHdr. version: 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5; layer: 1 = III, 2 = II, 3 = I.
func hdr(version, layer, brIdx, srIdx int, pad, mode int) []byte {
	b1 := byte(0xE0 | version<<3 | layer<<1 | 1) // sync, version, layer, no CRC
	b2 := byte(brIdx<<4 | srIdx<<2 | pad<<1)
	b3 := byte(mode << 6)
	return []byte{0xFF, b1, b2, b3}
}

// frame builds one full layer III frame (zero body) with the given parameters.
func frame(t *testing.T, version, brIdx, srIdx, mode int) []byte {
	t.Helper()
	sr := map[int][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}[version][srIdx]
	var kbps, n int
	if version == 3 {
		kbps = brV1L3[brIdx]
		n = 144 * kbps * 1000 / sr
	} else {
		kbps = brV2L3[brIdx]
		n = 72 * kbps * 1000 / sr
	}
	f := make([]byte, n)
	copy(f, hdr(version, 1, brIdx, srIdx, 0, mode))
	return f
}

func stream(frames ...[]byte) []byte { return bytes.Join(frames, nil) }

func parse(t *testing.T, b []byte) (Info, error) {
	t.Helper()
	return Parse(bytes.NewReader(b), int64(len(b)))
}

func id3(size int, footer bool) []byte {
	h := []byte{'I', 'D', '3', 4, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	if footer {
		h[5] = 0x10
	}
	out := append(h, make([]byte, size)...)
	if footer {
		out = append(out, make([]byte, 10)...)
	}
	return out
}

func TestMp3info_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		want Info
	}{
		{"mpeg1 44.1k stereo 128k", stream(frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
		{"mpeg1 48k joint 320k", stream(frame(t, 3, 14, 1, 1), frame(t, 3, 14, 1, 1)), Info{"1", 48000, 2, 320}},
		{"mpeg2 22.05k mono 128k", stream(frame(t, 2, 12, 0, 3), frame(t, 2, 12, 0, 3)), Info{"2", 22050, 1, 128}},
		{"mpeg2.5 8k", stream(frame(t, 0, 4, 2, 3), frame(t, 0, 4, 2, 3)), Info{"2.5", 8000, 1, 32}},
		{"id3v2 2048 with footer", stream(id3(2048, true), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
		{"id3v2 no footer", stream(id3(300, false), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
		{"100 junk bytes resynced", stream(bytes.Repeat([]byte{0x11}, 100), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
		{"false sync in junk", stream([]byte{0xFF, 0xFB, 0xFF, 0xFF, 0xFF}, bytes.Repeat([]byte{0x22}, 50), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
		{"padding bit", stream(append(frame(t, 3, 9, 0, 0)[:0:0], func() []byte { f := make([]byte, 418); copy(f, hdr(3, 1, 9, 0, 1, 0)); return f }()...), frame(t, 3, 9, 0, 0)), Info{"1", 44100, 2, 128}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := parse(t, c.data)
			if err != nil || got != c.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

func TestMp3info_Invalid(t *testing.T) {
	t.Parallel()
	good := frame(t, 3, 9, 0, 0)
	cases := []struct {
		name string
		data []byte
	}{
		{"layer II", stream(append(hdr(3, 2, 9, 0, 0, 0), make([]byte, 400)...), append(hdr(3, 2, 9, 0, 0, 0), make([]byte, 400)...))},
		{"layer I", stream(append(hdr(3, 3, 9, 0, 0, 0), make([]byte, 400)...), append(hdr(3, 3, 9, 0, 0, 0), make([]byte, 400)...))},
		{"all ones", bytes.Repeat([]byte{0xFF}, 3)},
		{"all ones long", bytes.Repeat([]byte{0xFF}, 5000)},
		{"bitrate index 15", stream(append(hdr(3, 1, 15, 0, 0, 0), make([]byte, 400)...), append(hdr(3, 1, 15, 0, 0, 0), make([]byte, 400)...))},
		{"free format", stream(append(hdr(3, 1, 0, 0, 0, 0), make([]byte, 400)...), append(hdr(3, 1, 0, 0, 0, 0), make([]byte, 400)...))},
		{"sample rate index 3", stream(append(hdr(3, 1, 9, 3, 0, 0), make([]byte, 400)...), append(hdr(3, 1, 9, 3, 0, 0), make([]byte, 400)...))},
		{"reserved version", stream(append(hdr(1, 1, 9, 0, 0, 0), make([]byte, 400)...), append(hdr(1, 1, 9, 0, 0, 0), make([]byte, 400)...))},
		{"single frameHdr", good},
		{"single frameHdr then junk", stream(good, bytes.Repeat([]byte{0x33}, 200))},
		{"empty", nil},
		{"short", []byte{0xFF, 0xFB}},
		{"id3 zero size then text", append(id3(0, false), []byte("synthetic")...)},
		{"junk only", bytes.Repeat([]byte("hello world "), 100)},
		{"flac", []byte("fLaC\x00\x00\x00\x22" + strings.Repeat("\x00", 64))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(t, c.data); !errors.Is(err, ErrNotMP3) {
				t.Fatalf("err=%v, want ErrNotMP3", err)
			}
		})
	}
}

type countingReaderAt struct {
	b     []byte
	total atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(c.b)) {
		return 0, errEOF
	}
	n := copy(p, c.b[off:])
	c.total.Add(int64(n))
	if n < len(p) {
		return n, errEOF
	}
	return n, nil
}

var errEOF = io.EOF

func TestMp3info_ReadsAtMost128KiB(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"1 MiB junk":        bytes.Repeat([]byte{0x44}, 1<<20),
		"1 MiB sync noise":  bytes.Repeat([]byte{0xFF, 0xFB, 0x90}, 400000),
		"big id3 then junk": stream(id3(500000, false), bytes.Repeat([]byte{0x44}, 1<<20)),
		"valid after 60KiB": stream(bytes.Repeat([]byte{0x44}, 60000), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0), bytes.Repeat([]byte{0}, 1<<20)),
	} {
		c := &countingReaderAt{b: data}
		_, err := Parse(c, int64(len(data)))
		if name == "valid after 60KiB" || name == "1 MiB sync noise" { // the repeated pattern is itself two valid headers
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		} else if !errors.Is(err, ErrNotMP3) {
			t.Errorf("%s: err=%v", name, err)
		}
		if got := c.total.Load(); got > 128<<10 {
			t.Errorf("%s: read %d bytes, limit %d", name, got, 128<<10)
		}
	}
}

func TestMp3info_JunkBeyondWindow(t *testing.T) {
	t.Parallel()
	// the first frame starts after the 64 KiB resync window: not accepted
	data := stream(bytes.Repeat([]byte{0x44}, 70000), frame(t, 3, 9, 0, 0), frame(t, 3, 9, 0, 0))
	if _, err := parse(t, data); !errors.Is(err, ErrNotMP3) {
		t.Fatalf("err=%v", err)
	}
}
