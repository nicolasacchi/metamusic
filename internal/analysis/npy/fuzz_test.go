package npy_test

import (
	"bytes"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
)

func FuzzNPY(f *testing.F) {
	f.Add(build(1, dict("<i8", "3,", false), le(8, 1, 2, 3)))
	f.Add(build(2, dict("<u2", "2, 2", false), le(2, 1, 2, 3, 4)))
	f.Add(build(3, dict(">i4", "1,", true), nil))
	f.Add([]byte("\x93NUMPY\x01\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			t.Skip()
		}
		a, err := npy.Read(bytes.NewReader(b), 1<<20)
		if err == nil {
			n := 1
			for _, d := range a.Shape {
				n *= d
			}
			if n != len(a.Data) {
				t.Fatalf("shape %v vs %d values", a.Shape, len(a.Data))
			}
		}
	})
}
