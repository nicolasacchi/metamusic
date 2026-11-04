package pcm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"testing"
)

func TestPCM24_RoundTrip(t *testing.T) {
	t.Parallel()
	const half = 1 << 23
	const chunk = 1 << 21 // values per chunk: 2^20 stereo frames
	for base := -half; base < half; base += chunk {
		edge := base == -half || base == half-chunk // the two chunks holding both clamps are swept in full
		if testing.Short() && !edge {
			continue
		}
		stride := 1
		if !edge {
			stride = 61 // the middle is checked on every 61st frame: a full 2^24 sweep costs a minute under -race
		}
		b := Buffer{Rate: 48000, Ch: [2][]float64{make([]float64, chunk/2), make([]float64, chunk/2)}}
		for i := 0; i < chunk/2; i++ {
			b.Ch[0][i] = float64(base+2*i) / half
			b.Ch[1][i] = float64(base+2*i+1) / half
		}
		var buf bytes.Buffer
		if _, err := WritePCM24(&buf, b); err != nil {
			t.Fatal(err)
		}
		got, err := ReadPCM24(&buf, 48000, chunk/2)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < chunk/2; i += stride {
			for c := 0; c < 2; c++ {
				want := float64(base+2*i+c) / half
				if want < -float64(half-1)/half {
					want = -float64(half-1) / half // -2^23 clamps to -(2^23-1)
				}
				if got.Ch[c][i] != want {
					t.Fatalf("value %d: read %v, want %v", base+2*i+c, got.Ch[c][i], want)
				}
			}
		}
	}
}

func TestPCM24_ClampRoundNaN(t *testing.T) {
	t.Parallel()
	b := Buffer{Rate: 8000, Ch: [2][]float64{
		{2, -2, 1, -1, 0.5 / (1 << 23), -0.5 / (1 << 23), 0.49 / (1 << 23), math.NaN()},
		{math.Inf(1), math.Inf(-1), 0, 0, 1.5 / (1 << 23), -1.5 / (1 << 23), 0, 0},
	}}
	var buf bytes.Buffer
	if _, err := WritePCM24(&buf, b); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPCM24(&buf, 8000, 8)
	if err != nil {
		t.Fatal(err)
	}
	top := float64(1<<23-1) / (1 << 23)
	one := 1.0 / (1 << 23)
	wantL := []float64{top, -top, top, -top, one, 0, 0, 0}
	wantR := []float64{top, -top, 0, 0, 2 * one, -one, 0, 0}
	for i := range wantL {
		if got.Ch[0][i] != wantL[i] || got.Ch[1][i] != wantR[i] {
			t.Errorf("frame %d: (%v, %v), want (%v, %v)", i, got.Ch[0][i], got.Ch[1][i], wantL[i], wantR[i])
		}
	}
}

// fixedSHA is the pcm_sha256 of the 8-sample vector of TestPCM24_SHA: it is the sha256 of the 24 bytes written out by hand.
const fixedSHA = "2cb439c0402823a5062b8c4b8b110f858fc1b07f97e66c83f86c65aa8d3712c0"

func TestPCM24_SHA(t *testing.T) {
	t.Parallel()
	b := Buffer{Rate: 48000, Ch: [2][]float64{
		{0, 0.5, -0.5, 1, -1, 0.25, 1.0 / (1 << 23), -1.0 / (1 << 23)},
		{0.125, -0.125, 0.75, -0.75, 0, 0, 0, 0},
	}}
	// interleaved little-endian 24-bit by hand: L0 R0 L1 R1 ...
	hand := []byte{
		0, 0, 0, 0, 0, 0x10, // 0 | 0.125 = 0x100000
		0, 0, 0x40, 0, 0, 0xf0, // 0.5 = 0x400000 | -0.125 = 0xf00000
		0, 0, 0xc0, 0, 0, 0x60, // -0.5 = 0xc00000 | 0.75 = 0x600000
		0xff, 0xff, 0x7f, 0, 0, 0xa0, // clamp 0x7fffff | -0.75 = 0xa00000
		0x01, 0x00, 0x80, 0, 0, 0, // clamp -(2^23-1) = 0x800001 | 0
		0, 0, 0x20, 0, 0, 0, // 0.25 = 0x200000 | 0
		1, 0, 0, 0, 0, 0, // one LSB
		0xff, 0xff, 0xff, 0, 0, 0, // minus one LSB
	}
	var buf bytes.Buffer
	sum, err := WritePCM24(&buf, b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), hand) {
		t.Fatalf("bytes % x\nwant  % x", buf.Bytes(), hand)
	}
	h := sha256.Sum256(hand)
	if sum != hex.EncodeToString(h[:]) {
		t.Errorf("returned %s, sha256 of the bytes is %x", sum, h)
	}
	if sum != fixedSHA {
		t.Errorf("pcm_sha256 %s, committed %s", sum, fixedSHA)
	}
}

func TestWritePCM24_Errors(t *testing.T) {
	t.Parallel()
	if _, err := WritePCM24(io.Discard, Buffer{Ch: [2][]float64{{1}, {1, 2}}}); err == nil {
		t.Error("unequal channels accepted")
	}
	boom := errors.New("boom")
	if _, err := WritePCM24(failWriter{boom}, Buffer{Ch: [2][]float64{{1}, {1}}}); !errors.Is(err, boom) {
		t.Errorf("write error not returned: %v", err)
	}
	if _, err := ReadPCM24(bytes.NewReader(make([]byte, 5)), 8000, 1); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short read: %v", err)
	}
	if _, err := ReadPCM24(bytes.NewReader(nil), 8000, 1); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("empty read: %v", err)
	}
	if _, err := ReadPCM24(bytes.NewReader(nil), 0, 1); err == nil {
		t.Error("rate 0 accepted")
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

func f32(vals ...float32) *bytes.Reader {
	var b bytes.Buffer
	for _, v := range vals {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return bytes.NewReader(b.Bytes())
}

func TestReadF32LE(t *testing.T) {
	t.Parallel()
	st, err := ReadF32LE(f32(0.5, -0.5, 0.25, 1), 44100, 2, 2)
	if err != nil || st.Rate != 44100 || st.Frames() != 2 {
		t.Fatalf("stereo: %+v, %v", st, err)
	}
	if st.Ch[0][0] != 0.5 || st.Ch[1][0] != -0.5 || st.Ch[0][1] != 0.25 || st.Ch[1][1] != 1 {
		t.Errorf("stereo planes %v %v", st.Ch[0], st.Ch[1])
	}
	mono, err := ReadF32LE(f32(0.5, -0.25, 7), 48000, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range []float64{0.5, -0.25, 7} {
		if mono.Ch[0][i] != v || mono.Ch[1][i] != v {
			t.Errorf("mono frame %d not duplicated: %v %v", i, mono.Ch[0][i], mono.Ch[1][i])
		}
	}
	nan, err := ReadF32LE(f32(float32(math.NaN())), 48000, 1, 1)
	if err != nil || !math.IsNaN(nan.Ch[0][0]) {
		t.Errorf("NaN must pass through: %v %v", nan.Ch[0], err)
	}
	if _, err := ReadF32LE(f32(1, 2, 3), 48000, 2, 2); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short input: %v", err)
	}
	if _, err := ReadF32LE(f32(), 48000, 1, 1); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("empty input: %v", err)
	}
	for _, c := range []struct{ rate, ch, frames int }{{48000, 3, 1}, {0, 1, 1}, {48000, 1, -1}} {
		if _, err := ReadF32LE(f32(), c.rate, c.ch, c.frames); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	// more than one internal chunk
	long := make([]float32, 3*chunkFrames+5)
	for i := range long {
		long[i] = float32(i)
	}
	b, err := ReadF32LE(f32(long...), 48000, 1, len(long))
	if err != nil || b.Ch[1][len(long)-1] != float64(len(long)-1) {
		t.Errorf("multi-chunk read: %v", err)
	}
}

func TestBuffer_Clone(t *testing.T) {
	t.Parallel()
	a := Buffer{Rate: 1, Ch: [2][]float64{{1, 2}, {3, 4}}}
	c := a.Clone()
	c.Ch[0][0] = 9
	if a.Ch[0][0] != 1 || c.Rate != 1 || c.Ch[1][1] != 4 {
		t.Error("Clone shares storage")
	}
}
