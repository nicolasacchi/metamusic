package tape

import (
	"errors"
	"io"
)

// The wire format between ffmpeg and the gain stage is s32le stereo (8 bytes a frame, the 24-bit sample left-aligned in 32
// bits; 16-bit sources arrive left-aligned too) and between the gain stage and the encoder s24le stereo (6 bytes a frame).
const (
	inFrameBytes  = 8
	inSampleBytes = 4
	stageBlock    = 1 << 14 // samples converted per pass
)

var (
	errClipped      = errors.New("a gained sample left the 24-bit range")
	errPartialFrame = errors.New("the decoder stopped inside a frame")
)

// gainStage is the io.Writer between one item's ffmpeg and the encoder pipe. It accumulates whole 8-byte frames (a Write
// may end anywhere, even inside a sample), shifts each sample down to its 24-bit value, applies the item's static gain in
// integers (ApplyGain), counts frames and tracks the post-gain peak, and writes 3 little-endian bytes per sample to out in
// Chunk-sized writes. A clamp makes the stage fail for good with errClipped (the export fails with TAPE_CLIPPED). The stage
// keeps no state between items: a new stage per item is what makes the concatenation gapless.
type gainStage struct {
	gain   int
	out    io.Writer
	carry  [inFrameBytes]byte
	nCarry int
	vals   []int32
	buf    []byte
	frames int64
	peak   int32 // largest post-gain |sample|, in 24-bit units
	err    error
}

func newGainStage(out io.Writer, gainCDB int) *gainStage {
	return &gainStage{gain: gainCDB, out: out, vals: make([]int32, stageBlock), buf: make([]byte, 0, Chunk+3*stageBlock)}
}

// Write implements io.Writer. It reports len(p) once p is consumed (output may still be buffered until Close or the next
// Chunk) and the sticky error after a failure.
func (g *gainStage) Write(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	n := len(p)
	if g.nCarry > 0 {
		k := copy(g.carry[g.nCarry:], p)
		g.nCarry += k
		p = p[k:]
		if g.nCarry < inFrameBytes {
			return n, nil
		}
		g.nCarry = 0
		if err := g.convert(g.carry[:]); err != nil {
			return 0, err
		}
	}
	whole := len(p) - len(p)%inFrameBytes
	if err := g.convert(p[:whole]); err != nil {
		return 0, err
	}
	g.nCarry = copy(g.carry[:], p[whole:])
	return n, nil
}

// convert processes whole frames.
func (g *gainStage) convert(b []byte) error {
	for len(b) > 0 {
		k := min(len(b)/inSampleBytes, len(g.vals))
		v := g.vals[:k]
		for i := range v {
			s := int32(uint32(b[4*i]) | uint32(b[4*i+1])<<8 | uint32(b[4*i+2])<<16 | uint32(b[4*i+3])<<24) //nolint:gosec // reinterpreting the little-endian bits as signed
			v[i] = s >> 8
		}
		if ApplyGain(v, v, g.gain) > 0 {
			g.err = errClipped
			return g.err
		}
		for _, x := range v {
			if a := abs32(x); a > g.peak {
				g.peak = a
			}
			g.buf = append(g.buf, byte(x), byte(x>>8), byte(x>>16)) //nolint:gosec // the low three bytes of a 24-bit value
		}
		g.frames += int64(k / 2)
		b = b[k*inSampleBytes:]
		if len(g.buf) >= Chunk {
			if err := g.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *gainStage) flush() error {
	if len(g.buf) == 0 {
		return nil
	}
	_, err := g.out.Write(g.buf)
	g.buf = g.buf[:0]
	if err != nil {
		g.err = err
	}
	return err
}

// Close writes what is buffered. It fails when the input ended inside a frame.
func (g *gainStage) Close() error {
	if g.err != nil {
		return g.err
	}
	if g.nCarry != 0 {
		g.err = errPartialFrame
		return g.err
	}
	return g.flush()
}

func abs32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}
