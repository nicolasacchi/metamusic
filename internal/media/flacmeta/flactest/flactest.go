// Package flactest is TEST-ONLY: it builds header-valid FLAC files for tests.
// Import it only from _test.go files (see TestNoTestHelpersInBinary).
package flactest

import (
	"encoding/binary"
	"os"
	"testing"
)

// Spec describes a synthetic FLAC file. The file is header-valid, not decodable audio.
type Spec struct {
	SampleRate    uint32
	Channels      uint8
	BitsPerSample uint8
	TotalSamples  uint64
	MD5           [16]byte
	Vendor        string
	Comments      []string
	// SeekPoints is the number of real seek points; SeekPlaceholders adds
	// placeholder points (sample 0xFFFFFFFFFFFFFFFF) after them.
	SeekPoints, SeekPlaceholders int
	PaddingBytes, PictureBytes   int
	Frames                       []byte // opaque bytes after the metadata
}

type block struct {
	typ  byte
	data []byte
}

// Build returns the file bytes: fLaC, STREAMINFO, [SEEKTABLE], [VORBIS_COMMENT]
// (when Vendor or Comments are set), [PICTURE], [PADDING], then Frames.
func Build(s Spec) []byte {
	if s.Channels == 0 {
		s.Channels = 2
	}
	if s.BitsPerSample == 0 {
		s.BitsPerSample = 24
	}
	if s.SampleRate == 0 {
		s.SampleRate = 48000
	}
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], 4096)
	binary.BigEndian.PutUint16(si[2:], 4096)
	packed := uint64(s.SampleRate)<<44 | uint64(s.Channels-1)<<41 | uint64(s.BitsPerSample-1)<<36 | s.TotalSamples&(1<<36-1)
	binary.BigEndian.PutUint64(si[10:], packed)
	copy(si[18:], s.MD5[:])
	blocks := []block{{0, si}}
	if n := s.SeekPoints + s.SeekPlaceholders; n > 0 {
		st := make([]byte, 18*n)
		for i := 0; i < n; i++ {
			sample := ^uint64(0)
			if i < s.SeekPoints {
				sample = uint64(i) * 96000
			}
			binary.BigEndian.PutUint64(st[18*i:], sample)
			binary.BigEndian.PutUint64(st[18*i+8:], uint64(i)*1000)
			binary.BigEndian.PutUint16(st[18*i+16:], 4096)
		}
		blocks = append(blocks, block{3, st})
	}
	if s.Vendor != "" || len(s.Comments) > 0 {
		vc := binary.LittleEndian.AppendUint32(nil, uint32(len(s.Vendor)))
		vc = append(vc, s.Vendor...)
		vc = binary.LittleEndian.AppendUint32(vc, uint32(len(s.Comments)))
		for _, c := range s.Comments {
			vc = binary.LittleEndian.AppendUint32(vc, uint32(len(c)))
			vc = append(vc, c...)
		}
		blocks = append(blocks, block{4, vc})
	}
	if s.PictureBytes > 0 {
		blocks = append(blocks, block{6, make([]byte, s.PictureBytes)})
	}
	if s.PaddingBytes > 0 {
		blocks = append(blocks, block{1, make([]byte, s.PaddingBytes)})
	}
	out := []byte("fLaC")
	for i, b := range blocks {
		h := b.typ
		if i == len(blocks)-1 {
			h |= 0x80
		}
		l := len(b.data)
		out = append(out, h, byte(l>>16), byte(l>>8), byte(l))
		out = append(out, b.data...)
	}
	return append(out, s.Frames...)
}

// WriteFile writes Build(s) to path with mode 0644.
func WriteFile(tb testing.TB, path string, s Spec) {
	tb.Helper()
	if err := os.WriteFile(path, Build(s), 0o644); err != nil {
		tb.Fatal(err)
	}
}
