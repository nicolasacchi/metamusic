// Package flacmeta reads FLAC metadata in pure Go with bounded allocations.
// It never decodes audio and never panics on hostile input (fuzzed).
package flacmeta

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// BlockType is a FLAC metadata block type.
type BlockType uint8

// Metadata block types.
const (
	BlockStreamInfo BlockType = iota
	BlockPadding
	BlockApplication
	BlockSeekTable
	BlockVorbisComment
	BlockCueSheet
	BlockPicture
)

// Errors returned by Read.
var (
	ErrNotFLAC = errors.New("flacmeta: not a FLAC stream")
	ErrCorrupt = errors.New("flacmeta: corrupt FLAC metadata")
)

// Limits (see the bounded-allocation rules of contract C13).
const (
	maxBlocks         = 1024
	maxVorbisComment  = 1 << 20
	streamInfoLen     = 34
	seekPointLen      = 18
	placeholderSample = ^uint64(0)
)

// StreamInfo is the STREAMINFO block.
type StreamInfo struct {
	MinBlock, MaxBlock uint16
	MinFrame, MaxFrame uint32
	SampleRate         uint32
	Channels           uint8
	BitsPerSample      uint8
	TotalSamples       uint64
	MD5                [16]byte
}

// Block locates one metadata block. Offset is the offset of the 4-byte block
// header; the data occupies [Offset+4, Offset+4+Length).
type Block struct {
	Type   BlockType
	Offset int64
	Length uint32
}

// SeekPoint is one SEEKTABLE entry.
type SeekPoint struct {
	Sample, Offset uint64
	FrameSamples   uint16
}

// Info is the parsed metadata of one FLAC file.
type Info struct {
	StreamInfo   StreamInfo
	Blocks       []Block
	FramesOffset int64    // first byte after the last metadata block
	Vendor       string   //
	Comments     []string // "KEY=value" in file order (from the first VORBIS_COMMENT)
	SeekPoints   []SeekPoint
}

// Count returns how many blocks of type t the file has.
func (i Info) Count(t BlockType) int {
	n := 0
	for _, b := range i.Blocks {
		if b.Type == t {
			n++
		}
	}
	return n
}

// Duration is TotalSamples / SampleRate in seconds.
func (i Info) Duration() float64 {
	if i.StreamInfo.SampleRate == 0 {
		return 0
	}
	return float64(i.StreamInfo.TotalSamples) / float64(i.StreamInfo.SampleRate)
}

// readAt reads exactly n bytes at off, refusing to allocate beyond the file.
func readAt(r io.ReaderAt, size, off int64, n uint32) ([]byte, error) {
	if off < 0 || off+int64(n) > size {
		return nil, ErrCorrupt
	}
	buf := make([]byte, n)
	if m, err := r.ReadAt(buf, off); m < len(buf) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// Read parses the metadata of a FLAC file of the given size.
func Read(r io.ReaderAt, size int64) (Info, error) {
	var info Info
	magic := make([]byte, 4)
	if size < 4 {
		return info, ErrNotFLAC
	}
	if m, err := r.ReadAt(magic, 0); m < 4 {
		return info, wrapEOF(err)
	}
	if string(magic) != "fLaC" {
		return info, ErrNotFLAC
	}
	off := int64(4)
	sawComment, sawSeek := false, false
	for n := 0; ; n++ {
		if n >= maxBlocks {
			return Info{}, fmt.Errorf("%w: more than %d metadata blocks", ErrCorrupt, maxBlocks)
		}
		hdr, err := readAt(r, size, off, 4)
		if err != nil {
			return Info{}, wrapEOF(err)
		}
		last := hdr[0]&0x80 != 0
		typ := BlockType(hdr[0] & 0x7f)
		length := uint32(hdr[1])<<16 | uint32(hdr[2])<<8 | uint32(hdr[3])
		if typ == 127 {
			return Info{}, fmt.Errorf("%w: invalid block type", ErrCorrupt)
		}
		if off+4+int64(length) > size {
			return Info{}, fmt.Errorf("%w: block extends past end of file", ErrCorrupt)
		}
		if n == 0 && (typ != BlockStreamInfo || length != streamInfoLen) {
			return Info{}, fmt.Errorf("%w: STREAMINFO must come first", ErrCorrupt)
		}
		if n > 0 && typ == BlockStreamInfo {
			return Info{}, fmt.Errorf("%w: duplicate STREAMINFO", ErrCorrupt)
		}
		info.Blocks = append(info.Blocks, Block{Type: typ, Offset: off, Length: length})
		switch {
		case typ == BlockStreamInfo:
			b, err := readAt(r, size, off+4, length)
			if err != nil {
				return Info{}, wrapEOF(err)
			}
			info.StreamInfo = parseStreamInfo(b)
			if info.StreamInfo.SampleRate == 0 {
				return Info{}, fmt.Errorf("%w: zero sample rate", ErrCorrupt)
			}
		case typ == BlockSeekTable && !sawSeek:
			sawSeek = true
			if length%seekPointLen != 0 {
				return Info{}, fmt.Errorf("%w: SEEKTABLE length", ErrCorrupt)
			}
			b, err := readAt(r, size, off+4, length)
			if err != nil {
				return Info{}, wrapEOF(err)
			}
			for i := 0; i+seekPointLen <= len(b); i += seekPointLen {
				sp := SeekPoint{
					Sample:       binary.BigEndian.Uint64(b[i:]),
					Offset:       binary.BigEndian.Uint64(b[i+8:]),
					FrameSamples: binary.BigEndian.Uint16(b[i+16:]),
				}
				if sp.Sample != placeholderSample {
					info.SeekPoints = append(info.SeekPoints, sp)
				}
			}
		case typ == BlockVorbisComment && !sawComment:
			sawComment = true
			if length > maxVorbisComment {
				return Info{}, fmt.Errorf("%w: VORBIS_COMMENT too large", ErrCorrupt)
			}
			b, err := readAt(r, size, off+4, length)
			if err != nil {
				return Info{}, wrapEOF(err)
			}
			if err := parseVorbis(b, &info); err != nil {
				return Info{}, err
			}
		}
		off += 4 + int64(length)
		if last {
			info.FramesOffset = off
			return info, nil
		}
	}
}

func wrapEOF(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrCorrupt
	}
	return err
}

func parseStreamInfo(b []byte) StreamInfo {
	var s StreamInfo
	s.MinBlock = binary.BigEndian.Uint16(b[0:])
	s.MaxBlock = binary.BigEndian.Uint16(b[2:])
	s.MinFrame = uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	s.MaxFrame = uint32(b[7])<<16 | uint32(b[8])<<8 | uint32(b[9])
	packed := binary.BigEndian.Uint64(b[10:18])
	s.SampleRate = uint32(packed >> 44)
	s.Channels = uint8(packed>>41&0x7) + 1
	s.BitsPerSample = uint8(packed>>36&0x1f) + 1
	s.TotalSamples = packed & (1<<36 - 1)
	copy(s.MD5[:], b[18:34])
	return s
}

func parseVorbis(b []byte, info *Info) error {
	bad := func(what string) error { return fmt.Errorf("%w: VORBIS_COMMENT %s", ErrCorrupt, what) }
	if len(b) < 8 {
		return bad("too short")
	}
	vlen := int64(binary.LittleEndian.Uint32(b))
	if vlen > int64(len(b))-8 {
		return bad("vendor length")
	}
	info.Vendor = string(b[4 : 4+vlen])
	p := 4 + vlen
	count := int64(binary.LittleEndian.Uint32(b[p:]))
	p += 4
	if count > (int64(len(b))-p)/4 {
		return bad("comment count")
	}
	comments := make([]string, 0, count)
	for i := int64(0); i < count; i++ {
		if int64(len(b))-p < 4 {
			return bad("truncated")
		}
		l := int64(binary.LittleEndian.Uint32(b[p:]))
		p += 4
		if l > int64(len(b))-p {
			return bad("comment length")
		}
		comments = append(comments, string(b[p:p+l]))
		p += l
	}
	info.Comments = comments
	return nil
}

// FramesSHA256 hashes the bytes [off, size): the audio frames.
func FramesSHA256(r io.ReaderAt, size, off int64) (string, error) {
	if off < 0 || off > size {
		return "", ErrCorrupt
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(r, off, size-off)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AudioID is the audio identity "<frames sha>:<total samples>".
func AudioID(framesSHA256 string, totalSamples uint64) string {
	return fmt.Sprintf("%s:%d", framesSHA256, totalSamples)
}
