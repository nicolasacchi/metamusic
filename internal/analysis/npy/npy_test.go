package npy_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
)

// build assembles a .npy file with a given format version, header dict and payload.
func build(major byte, dict string, payload []byte) []byte {
	h := dict
	pad := 64 - (10+len(h)+1)%64
	if major >= 2 {
		pad = 64 - (12+len(h)+1)%64
	}
	h += strings.Repeat(" ", pad%64) + "\n"
	var b bytes.Buffer
	b.WriteString("\x93NUMPY")
	b.WriteByte(major)
	b.WriteByte(0)
	if major == 1 {
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(h)))
	} else {
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(h)))
	}
	b.WriteString(h)
	b.Write(payload)
	return b.Bytes()
}

func dict(descr, shape string, fortran bool) string {
	f := "False"
	if fortran {
		f = "True"
	}
	return fmt.Sprintf("{'descr': '%s', 'fortran_order': %s, 'shape': (%s), }", descr, f, shape)
}

func le(size int, vals ...uint64) []byte {
	var out []byte
	for _, v := range vals {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, v)
		out = append(out, b[:size]...)
	}
	return out
}

func TestNPY_Read(t *testing.T) {
	t.Parallel()
	neg := func(v int64) uint64 { return uint64(v) }
	for _, c := range []struct {
		name    string
		file    []byte
		max     int64
		want    []int64
		shape   []int
		wantErr error
	}{
		{"v1 i8 (n,)", build(1, dict("<i8", "3,", false), le(8, 1, neg(-2), 3)), 1 << 20, []int64{1, -2, 3}, []int{3}, nil},
		{"v2 i4 (1,n)", build(2, dict("<i4", "1, 3", false), le(4, 7, neg(-8), 9)), 1 << 20, []int64{7, -8, 9}, []int{1, 3}, nil},
		{"v3 i2 (2,n)", build(3, dict("<i2", "2, 2", false), le(2, 1, 2, neg(-3), 4)), 1 << 20, []int64{1, 2, -3, 4}, []int{2, 2}, nil},
		{"u2 (n,2)", build(1, dict("<u2", "2, 2", false), le(2, 65535, 0, 1, 2)), 1 << 20, []int64{65535, 0, 1, 2}, []int{2, 2}, nil},
		{"u1", build(1, dict("|u1", "3,", false), []byte{0, 128, 255}), 1 << 20, []int64{0, 128, 255}, []int{3}, nil},
		{"i1", build(1, dict("|i1", "2,", false), []byte{0x80, 0x7f}), 1 << 20, []int64{-128, 127}, []int{2}, nil},
		{"u4", build(1, dict("<u4", "1,", false), le(4, 4294967295)), 1 << 20, []int64{4294967295}, []int{1}, nil},
		{"u8 small", build(1, dict("<u8", "1,", false), le(8, 5)), 1 << 20, []int64{5}, []int{1}, nil},
		{"u8 too big", build(1, dict("<u8", "1,", false), le(8, 1<<63)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"empty", build(1, dict("<i8", "0,", false), nil), 1 << 20, []int64{}, []int{0}, nil},
		{"fortran", build(1, dict("<i4", "2, 2", true), le(4, 1, 2, 3, 4)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"big endian", build(1, dict(">i4", "1,", false), le(4, 1)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"float", build(1, dict("<f4", "1,", false), le(4, 1)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"4 dims", build(1, dict("<i4", "1, 1, 1, 1", false), le(4, 1)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"scalar", build(1, dict("<i4", "", false), le(4, 1)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"version 4", build(4, dict("<i4", "1,", false), le(4, 1)), 1 << 20, nil, nil, npy.ErrUnsupported},
		{"header 70 KiB", build(2, dict("<i4", "1,", false)+strings.Repeat(" ", 70<<10), le(4, 1)), 1 << 20, nil, nil, npy.ErrCorrupt},
		{"data short", build(1, dict("<i4", "3,", false), le(4, 1, 2)), 1 << 20, nil, nil, npy.ErrCorrupt},
		{"data long", build(1, dict("<i4", "2,", false), le(4, 1, 2, 3)), 1 << 20, nil, nil, npy.ErrCorrupt},
		{"bad magic", []byte("not a npy file at all"), 1 << 20, nil, nil, npy.ErrCorrupt},
		{"garbage header", build(1, "[1,2,3]", nil), 1 << 20, nil, nil, npy.ErrCorrupt},
		{"over budget", build(1, dict("<i4", "1000,", false), le(4, 1)), 1000, nil, nil, npy.ErrTooLarge},
		{"empty input", nil, 1 << 20, nil, nil, npy.ErrCorrupt},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a, err := npy.Read(bytes.NewReader(c.file), c.max)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(a.Data, c.want) || !slices.Equal(a.Shape, c.shape) {
				t.Errorf("got %v %v, want %v %v", a.Shape, a.Data, c.shape, c.want)
			}
		})
	}
}

// TestNPY_HugeShapeNoAllocation is not parallel: it measures process-wide allocation.
func TestNPY_HugeShapeNoAllocation(t *testing.T) {
	f := build(1, dict("<i8", "1000000000, 1000", false), le(8, 1))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := npy.Read(bytes.NewReader(f), 64<<20)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("huge shape accepted")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("allocated %d bytes before rejecting", grew)
	}
}
