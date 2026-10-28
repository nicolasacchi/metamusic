package flacmeta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Replace says which metadata blocks Rewrite swaps. For each type: nil keeps the source's blocks of that type, an empty
// non-nil slice drops them, anything else is the new payload (the only block of its type).
type Replace struct{ Vorbis, Cuesheet, Picture []byte }

// Rewrite writes a copy of the FLAC file src (size bytes, described by info) to dst: "fLaC", STREAMINFO and the first
// SEEKTABLE verbatim, then the VORBIS_COMMENT, CUESHEET and PICTURE blocks (replaced as r says, in that order; every other
// block, padding included, is dropped), with the last-block flag set on the final block, then the audio frames byte for
// byte. It never touches the frames region: the caller asserts FramesSHA256 before and after. It returns the bytes written.
func Rewrite(dst io.Writer, src io.ReaderAt, size int64, info Info, r Replace) (int64, error) {
	if len(info.Blocks) == 0 || info.Blocks[0].Type != BlockStreamInfo {
		return 0, errors.New("flacmeta: source has no STREAMINFO")
	}
	if info.FramesOffset < 4 || info.FramesOffset > size {
		return 0, errors.New("flacmeta: frames offset is outside the source")
	}
	type out struct {
		typ  BlockType
		data []byte
	}
	var blocks []out
	read := func(bl Block) ([]byte, error) {
		data := make([]byte, bl.Length)
		if n, err := src.ReadAt(data, bl.Offset+4); n < len(data) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("flacmeta: read source block: %w", err)
		}
		return data, nil
	}
	first := func(t BlockType) (Block, bool) {
		for _, bl := range info.Blocks[1:] {
			if bl.Type == t {
				return bl, true
			}
		}
		return Block{}, false
	}
	si, err := read(info.Blocks[0])
	if err != nil {
		return 0, err
	}
	blocks = append(blocks, out{BlockStreamInfo, si})
	if bl, ok := first(BlockSeekTable); ok {
		data, err := read(bl)
		if err != nil {
			return 0, err
		}
		blocks = append(blocks, out{BlockSeekTable, data})
	}
	for _, s := range []struct {
		typ  BlockType
		repl []byte
	}{{BlockVorbisComment, r.Vorbis}, {BlockCueSheet, r.Cuesheet}, {BlockPicture, r.Picture}} {
		switch {
		case s.repl == nil:
			for _, bl := range info.Blocks[1:] {
				if bl.Type != s.typ {
					continue
				}
				data, err := read(bl)
				if err != nil {
					return 0, err
				}
				blocks = append(blocks, out{s.typ, data})
			}
		case len(s.repl) == 0:
		case len(s.repl) > MaxBlockLen:
			return 0, fmt.Errorf("flacmeta: block type %d exceeds the block size limit", s.typ)
		default:
			blocks = append(blocks, out{s.typ, s.repl})
		}
	}
	var head bytes.Buffer
	head.WriteString("fLaC")
	for i, b := range blocks {
		head.Write(BlockHeader(b.typ, i == len(blocks)-1, len(b.data)))
		head.Write(b.data)
	}
	n, err := dst.Write(head.Bytes())
	total := int64(n)
	if err != nil {
		return total, fmt.Errorf("flacmeta: write: %w", err)
	}
	want := size - info.FramesOffset
	m, err := io.Copy(dst, io.NewSectionReader(src, info.FramesOffset, want))
	total += m
	switch {
	case err != nil:
		return total, fmt.Errorf("flacmeta: copy frames: %w", err)
	case m != want:
		return total, fmt.Errorf("flacmeta: copy frames: %w", io.ErrUnexpectedEOF)
	}
	return total, nil
}
