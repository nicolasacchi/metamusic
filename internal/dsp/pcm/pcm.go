// Package pcm holds the planar float64 audio buffer of the transforms and the raw PCM readers and writers (C203.1).
package pcm

import (
	"errors"
	"io"
)

// Buffer is a stereo planar buffer. Mono inputs are duplicated to both channels in Go (D-T1-19), so Ch[0] and Ch[1] always
// have the same length.
type Buffer struct {
	Rate int
	Ch   [2][]float64
}

// Frames is the number of sample frames.
func (b Buffer) Frames() int { return len(b.Ch[0]) }

// STUB(T1-D1): the readers and writers of C203.1. ReadF32LE reads frames little-endian float32 frames of channels interleaved
// channels (1 or 2) and duplicates mono; WritePCM24 writes interleaved little-endian 24-bit PCM (round to nearest, clamp
// +-(2^23-1), no dither) and returns the running pcm_sha256 in hex.

// ReadF32LE reads a buffer from r.
func ReadF32LE(r io.Reader, rate, channels, frames int) (Buffer, error) {
	return Buffer{}, errors.ErrUnsupported
}

// WritePCM24 writes b to w and returns the pcm_sha256 of the bytes written.
func WritePCM24(w io.Writer, b Buffer) (sha256 string, err error) {
	return "", errors.ErrUnsupported
}
