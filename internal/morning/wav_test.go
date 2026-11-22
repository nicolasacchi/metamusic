package morning

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWAV_ConcatSampleExact(t *testing.T) {
	slate := SlatePCM(3)
	audio := make([]byte, 1000*wavBytesFrame)
	for i := range audio {
		audio[i] = byte(i%251 + 1)
	}
	frames := int64(SlateSamples) + 1000 + 7
	var buf bytes.Buffer
	err := writeWAV(&buf, frames, bytes.NewReader(slate), bytes.NewReader(audio), &zeroReader{n: 7 * wavBytesFrame})
	if err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if int64(len(b)) != wavHeaderLen+frames*wavBytesFrame {
		t.Fatalf("len = %d", len(b))
	}
	r := bytes.NewReader(b)
	off, got, err := wavData(r, int64(len(b)))
	if err != nil || off != wavHeaderLen || got != frames {
		t.Fatalf("wavData = %d, %d, %v", off, got, err)
	}
	if !bytes.Equal(b[off:off+int64(len(slate))], slate) {
		t.Error("slate frames moved")
	}
	if !bytes.Equal(b[off+int64(len(slate)):off+int64(len(slate))+int64(len(audio))], audio) {
		t.Error("audio frames do not start right after the slate")
	}
	if !bytes.Equal(b[len(b)-7*wavBytesFrame:], make([]byte, 7*wavBytesFrame)) {
		t.Error("tail is not the zero pad")
	}
	if binary.LittleEndian.Uint32(b[4:]) != uint32(36+frames*wavBytesFrame) {
		t.Error("RIFF size wrong")
	}

	for name, parts := range map[string][]io.Reader{
		"short": {bytes.NewReader(slate)},
		"long":  {bytes.NewReader(slate), bytes.NewReader(audio), bytes.NewReader(audio), &zeroReader{n: 100}},
	} {
		if err := writeWAV(io.Discard, frames, parts...); err == nil {
			t.Errorf("%s parts: want an error", name)
		}
	}
	if err := writeWAV(io.Discard, 1<<40); err == nil {
		t.Error("an oversized side must be refused")
	}
	if err := writeWAV(failWriter{}, 1); err == nil {
		t.Error("a failing writer must fail")
	}
	if err := writeWAV(&limitedWriter{n: wavHeaderLen + 2}, 10, &zeroReader{n: 60}); err == nil {
		t.Error("a failing body write must fail")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type limitedWriter struct{ n int }

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, io.ErrClosedPipe
	}
	l.n -= len(p)
	return len(p), nil
}

func TestFitReader(t *testing.T) {
	for _, tc := range []struct {
		have, n int
	}{{10, 10}, {10, 4}, {4, 10}, {0, 3}} {
		b, err := io.ReadAll(fitReader2(bytes.NewReader(bytes.Repeat([]byte{9}, tc.have)), int64(tc.have), int64(tc.n)))
		if err != nil || len(b) != tc.n {
			t.Errorf("fit(%d, %d) = %d bytes, %v", tc.have, tc.n, len(b), err)
		}
	}
	if b, _ := io.ReadAll(fitReader([]byte{1, 2, 3}, 5)); !bytes.Equal(b, []byte{1, 2, 3, 0, 0}) {
		t.Errorf("fitReader = %v", b)
	}
	if n, err := (&zeroReader{}).Read(make([]byte, 4)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("empty zeroReader = %d, %v", n, err)
	}
}

func wavWith(chunks ...[]byte) []byte {
	var body []byte
	body = append(body, "WAVE"...)
	for _, c := range chunks {
		body = append(body, c...)
	}
	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

func chunk(id string, body []byte) []byte {
	b := append([]byte(id), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...)
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func TestWAVData_Parsing(t *testing.T) {
	good := wavHeader(0)[20:36] // the fmt body
	pcm := make([]byte, 12)
	for _, tc := range []struct {
		name   string
		b      []byte
		frames int64
		bad    bool
	}{
		{"canonical", wavWith(chunk("fmt ", good), chunk("data", pcm)), 2, false},
		{"skips a LIST chunk with odd size", wavWith(chunk("fmt ", good), chunk("LIST", []byte("abc")), chunk("data", pcm)), 2, false},
		{"truncated data counts what is there", append(wavWith(chunk("fmt ", good))[:0:0], append(wavWith(chunk("fmt ", good)), append([]byte("data\xff\xff\xff\x7f"), pcm...)...)...), 2, false},
		{"not riff", []byte(strings.Repeat("x", 64)), 0, true},
		{"no data", wavWith(chunk("fmt ", good)), 0, true},
		{"data before fmt", wavWith(chunk("data", pcm), chunk("fmt ", good)), 0, true},
		{"short fmt", wavWith(chunk("fmt ", good[:8]), chunk("data", pcm)), 0, true},
		{"mono", wavWith(chunk("fmt ", withU16(good, 2, 1)), chunk("data", pcm)), 0, true},
		{"16 bit", wavWith(chunk("fmt ", withU16(good, 14, 16)), chunk("data", pcm)), 0, true},
		{"float", wavWith(chunk("fmt ", withU16(good, 0, 3)), chunk("data", pcm)), 0, true},
		{"rate", wavWith(chunk("fmt ", withU16(good, 4, 44100)), chunk("data", pcm)), 0, true},
		{"extensible ok", wavWith(chunk("fmt ", withU16(good, 0, 0xFFFE)), chunk("data", pcm)), 2, false},
	} {
		_, frames, err := wavData(bytes.NewReader(tc.b), int64(len(tc.b)))
		if (err != nil) != tc.bad || (!tc.bad && frames != tc.frames) {
			t.Errorf("%s: frames %d, err %v", tc.name, frames, err)
		}
	}
}

func withU16(b []byte, at int, v uint16) []byte {
	c := bytes.Clone(b)
	binary.LittleEndian.PutUint16(c[at:], v)
	return c
}
