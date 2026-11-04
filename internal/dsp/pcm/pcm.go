// Package pcm holds the planar float64 audio buffer of the transforms and the raw PCM readers and writers (C203.1).
package pcm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
)

// Buffer is a stereo planar buffer. Mono inputs are duplicated to both channels in Go (D-T1-19), so Ch[0] and Ch[1] always
// have the same length.
type Buffer struct {
	Rate int
	Ch   [2][]float64
}

// Frames is the number of sample frames.
func (b Buffer) Frames() int { return len(b.Ch[0]) }

// Clone returns a deep copy of b.
func (b Buffer) Clone() Buffer {
	return Buffer{Rate: b.Rate, Ch: [2][]float64{append([]float64(nil), b.Ch[0]...), append([]float64(nil), b.Ch[1]...)}}
}

const (
	chunkFrames = 4096
	// pcm24Scale maps full scale (1.0) to 2^23; the sample is then clamped to +-(2^23-1).
	pcm24Scale = 1 << 23
	pcm24Max   = 1<<23 - 1
)

// ReadF32LE reads frames little-endian float32 frames of channels interleaved channels (1 or 2) from r; mono is duplicated to
// both channels. A short input is an error (io.ErrUnexpectedEOF). Non-finite samples are passed through (safety.Finish
// rejects them).
func ReadF32LE(r io.Reader, rate, channels, frames int) (Buffer, error) {
	if channels != 1 && channels != 2 {
		return Buffer{}, fmt.Errorf("pcm: %d channels, want 1 or 2", channels)
	}
	if frames < 0 || rate <= 0 {
		return Buffer{}, errors.New("pcm: bad rate or frame count")
	}
	b := Buffer{Rate: rate, Ch: [2][]float64{make([]float64, frames), make([]float64, frames)}}
	buf := make([]byte, 4*channels*min(chunkFrames, max(frames, 1)))
	for done := 0; done < frames; {
		n := min(chunkFrames, frames-done)
		raw := buf[:4*channels*n]
		if _, err := io.ReadFull(r, raw); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Buffer{}, err
		}
		for i := 0; i < n; i++ {
			l := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*channels*i:])))
			rr := l
			if channels == 2 {
				rr = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*(2*i+1):])))
			}
			b.Ch[0][done+i], b.Ch[1][done+i] = l, rr
		}
		done += n
	}
	return b, nil
}

// quantize24 rounds to nearest (half up), clamps to +-(2^23-1); NaN is written as silence.
func quantize24(x float64) int32 {
	if x != x {
		return 0
	}
	v := math.Floor(float64(x*pcm24Scale) + 0.5)
	switch {
	case v > pcm24Max:
		return pcm24Max
	case v < -pcm24Max:
		return -pcm24Max
	}
	return int32(v)
}

// WritePCM24 writes b to w as interleaved little-endian 24-bit PCM (round to nearest, clamp +-(2^23-1), no dither: the
// quantisation floor is already -144 dBFS) and returns the running pcm_sha256 of the bytes written, in hex.
func WritePCM24(w io.Writer, b Buffer) (sha256sum string, err error) {
	if len(b.Ch[0]) != len(b.Ch[1]) {
		return "", errors.New("pcm: channels differ in length")
	}
	h := sha256.New()
	buf := make([]byte, 6*chunkFrames)
	for done := 0; done < b.Frames(); {
		n := min(chunkFrames, b.Frames()-done)
		raw := buf[:6*n]
		for i := 0; i < n; i++ {
			for c := 0; c < 2; c++ {
				v := uint32(quantize24(b.Ch[c][done+i])) // #nosec G115 -- two's complement of a 24-bit value
				o := 6*i + 3*c
				raw[o], raw[o+1], raw[o+2] = byte(v&0xff), byte(v>>8&0xff), byte(v>>16&0xff)
			}
		}
		h.Write(raw)
		if _, err := w.Write(raw); err != nil {
			return "", err
		}
		done += n
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReadPCM24 reads frames interleaved little-endian 24-bit stereo frames (the inverse of WritePCM24).
func ReadPCM24(r io.Reader, rate, frames int) (Buffer, error) {
	if frames < 0 || rate <= 0 {
		return Buffer{}, errors.New("pcm: bad rate or frame count")
	}
	b := Buffer{Rate: rate, Ch: [2][]float64{make([]float64, frames), make([]float64, frames)}}
	buf := make([]byte, 6*min(chunkFrames, max(frames, 1)))
	for done := 0; done < frames; {
		n := min(chunkFrames, frames-done)
		raw := buf[:6*n]
		if _, err := io.ReadFull(r, raw); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return Buffer{}, err
		}
		for i := 0; i < n; i++ {
			for c := 0; c < 2; c++ {
				o := 6*i + 3*c
				v := int32(raw[o]) | int32(raw[o+1])<<8 | int32(raw[o+2])<<16
				if v >= pcm24Scale { // sign-extend the 24-bit value
					v -= 2 * pcm24Scale
				}
				b.Ch[c][done+i] = float64(v) / pcm24Scale
			}
		}
		done += n
	}
	return b, nil
}
