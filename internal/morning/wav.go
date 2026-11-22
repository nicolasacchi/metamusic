package morning

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The side format: 48 kHz stereo signed 24-bit little-endian PCM in a canonical 44-byte-header WAV.
const (
	wavChannels   = 2
	wavBytesFrame = 6 // 3 bytes x 2 channels
	wavHeaderLen  = 44
	maxWAVBytes   = 1<<32 - 1 - 36 // the RIFF size field is 32 bits
)

// wavHeader returns the header of a WAV holding frames frames of side-format PCM.
func wavHeader(frames int64) []byte {
	n := uint32(frames * wavBytesFrame) // #nosec G115 -- callers bound frames by maxWAVBytes
	h := make([]byte, 0, wavHeaderLen)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, 36+n)
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 1) // PCM
	h = binary.LittleEndian.AppendUint16(h, wavChannels)
	h = binary.LittleEndian.AppendUint32(h, SampleRate)
	h = binary.LittleEndian.AppendUint32(h, SampleRate*wavBytesFrame)
	h = binary.LittleEndian.AppendUint16(h, wavBytesFrame)
	h = binary.LittleEndian.AppendUint16(h, 24)
	h = append(h, "data"...)
	return binary.LittleEndian.AppendUint32(h, n)
}

// wavData locates the PCM of a WAV: the byte offset of the data chunk body and its length in frames. It accepts only
// 48 kHz stereo 24-bit integer PCM (format 1 or WAVE_FORMAT_EXTENSIBLE) and skips any other chunk.
func wavData(r io.ReaderAt, size int64) (off, frames int64, err error) {
	var h [12]byte
	if _, err := r.ReadAt(h[:], 0); err != nil || string(h[:4]) != "RIFF" || string(h[8:]) != "WAVE" {
		return 0, 0, errors.New("morning: not a WAV file")
	}
	var fmtOK bool
	pos := int64(12)
	for pos+8 <= size {
		var ch [8]byte
		if _, err := r.ReadAt(ch[:], pos); err != nil {
			return 0, 0, fmt.Errorf("morning: wav chunk header: %w", err)
		}
		n := int64(binary.LittleEndian.Uint32(ch[4:]))
		body := pos + 8
		switch string(ch[:4]) {
		case "fmt ":
			if n < 16 {
				return 0, 0, errors.New("morning: short wav fmt chunk")
			}
			var f [16]byte
			if _, err := r.ReadAt(f[:], body); err != nil {
				return 0, 0, fmt.Errorf("morning: wav fmt chunk: %w", err)
			}
			tag := binary.LittleEndian.Uint16(f[0:])
			if (tag != 1 && tag != 0xFFFE) || binary.LittleEndian.Uint16(f[2:]) != wavChannels ||
				binary.LittleEndian.Uint32(f[4:]) != SampleRate || binary.LittleEndian.Uint16(f[14:]) != 24 {
				return 0, 0, errors.New("morning: wav is not 48 kHz stereo 24-bit PCM")
			}
			fmtOK = true
		case "data":
			if !fmtOK {
				return 0, 0, errors.New("morning: wav data before fmt")
			}
			if body+n > size { // a truncated file: count what is there
				n = size - body
			}
			return body, n / wavBytesFrame, nil
		}
		pos = body + n + n&1
	}
	return 0, 0, errors.New("morning: wav has no data chunk")
}

// zeroReader yields n zero bytes.
type zeroReader struct{ n int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.n {
		p = p[:z.n]
	}
	clear(p)
	z.n -= int64(len(p))
	return len(p), nil
}

// writeWAV writes the header and then exactly frames frames taken from the parts in order, so chapter starts are sample
// exact. It fails when the parts hold a different number of frames.
func writeWAV(w io.Writer, frames int64, parts ...io.Reader) error {
	if frames < 0 || frames*wavBytesFrame > maxWAVBytes {
		return fmt.Errorf("morning: side of %d frames does not fit a WAV", frames)
	}
	if _, err := w.Write(wavHeader(frames)); err != nil {
		return err
	}
	want := frames * wavBytesFrame
	n, err := io.Copy(w, io.MultiReader(parts...))
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("morning: side parts hold %d bytes, want %d", n, want)
	}
	return nil
}
