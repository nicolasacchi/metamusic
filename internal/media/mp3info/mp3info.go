// Package mp3info parses the first MPEG audio layer III frame header of a file
// (C60). It is a stdlib-only leaf used to tell an MP3 from other audio before
// ffmpeg is involved; it never decodes.
package mp3info

import (
	"errors"
	"io"
)

// ErrNotMP3 reports a stream that is not MPEG audio layer III.
var ErrNotMP3 = errors.New("mp3info: not an MPEG audio layer III stream")

// Info describes the first valid frame.
type Info struct {
	MPEGVersion string // "1", "2" or "2.5"
	SampleRate  int
	Channels    int // 1 or 2
	BitrateKbps int // of the first frame (the stream may be VBR)
}

const (
	resyncWindow = 64 << 10 // how far into the stream the first frame may start
	maxFrame     = 1441     // longest layer III frame: 144*320000/32000 + padding
	maxRead      = 128 << 10
)

var (
	bitrateV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	bitrateV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	rateV1    = [4]int{44100, 48000, 32000, 0}
	rateV2    = [4]int{22050, 24000, 16000, 0}
	rateV25   = [4]int{11025, 12000, 8000, 0}
)

type header struct {
	Info
	version  int // 1, 2 or 25
	frameLen int
}

// parseHeader decodes four bytes; ok is false for anything that is not a valid
// layer III header (reserved fields, free format, bad bitrate or sample rate).
func parseHeader(b []byte) (h header, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	var rates [4]int
	switch b[1] >> 3 & 3 {
	case 3:
		h.version, h.MPEGVersion, rates = 1, "1", rateV1
	case 2:
		h.version, h.MPEGVersion, rates = 2, "2", rateV2
	case 0:
		h.version, h.MPEGVersion, rates = 25, "2.5", rateV25
	default:
		return h, false // reserved
	}
	if b[1]>>1&3 != 1 { // layer III only
		return h, false
	}
	bi, si := int(b[2]>>4), int(b[2]>>2&3)
	table := bitrateV2
	if h.version == 1 {
		table = bitrateV1
	}
	h.BitrateKbps, h.SampleRate = table[bi], rates[si]
	if h.BitrateKbps == 0 || h.SampleRate == 0 { // free format, bad index
		return h, false
	}
	h.Channels = 2
	if b[3]>>6 == 3 {
		h.Channels = 1
	}
	coef := 144
	if h.version != 1 {
		coef = 72
	}
	h.frameLen = coef*h.BitrateKbps*1000/h.SampleRate + int(b[2]>>1&1)
	return h, h.frameLen > 4
}

// Parse skips one ID3v2 tag (syncsafe size, footer flag), then requires two
// consecutive valid layer III frame headers within the next 64 KiB (resync
// allowed over junk). It reads at most 128 KiB.
func Parse(r io.ReaderAt, size int64) (Info, error) {
	off := int64(0)
	var tag [10]byte
	if n, _ := r.ReadAt(tag[:], 0); n == len(tag) && string(tag[:3]) == "ID3" &&
		tag[6]&0x80 == 0 && tag[7]&0x80 == 0 && tag[8]&0x80 == 0 && tag[9]&0x80 == 0 {
		off = 10 + int64(tag[6])<<21 + int64(tag[7])<<14 + int64(tag[8])<<7 + int64(tag[9])
		if tag[5]&0x10 != 0 {
			off += 10
		}
	}
	if off >= size {
		return Info{}, ErrNotMP3
	}
	want := int64(resyncWindow + maxFrame + 4)
	if rest := size - off; rest < want {
		want = rest
	}
	buf := make([]byte, want)
	n, err := r.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return Info{}, err
	}
	buf = buf[:n]
	for i := 0; i < len(buf) && i < resyncWindow; i++ {
		if buf[i] != 0xFF {
			continue
		}
		h, ok := parseHeader(buf[i:])
		if !ok {
			continue
		}
		j := i + h.frameLen
		if j+4 > len(buf) {
			continue
		}
		h2, ok := parseHeader(buf[j:])
		if !ok || h2.version != h.version || h2.SampleRate != h.SampleRate {
			continue
		}
		return h.Info, nil
	}
	return Info{}, ErrNotMP3
}
