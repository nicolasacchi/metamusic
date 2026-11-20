package morning

import (
	"math"
	"testing"
)

func TestSlateFreq(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want float64
	}{{1, 440}, {2, 466.1638}, {13, 440}, {12, 830.6094}, {0, 830.6094}} {
		if got := SlateFreq(tc.n); math.Abs(got-tc.want) > 1e-3 {
			t.Errorf("SlateFreq(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
	if got := SlateFreq(2); math.Abs(got-466.1638) > 1e-4 {
		t.Errorf("SlateFreq(2) = %v", got)
	}
}

func sample(b []byte, i int) int32 {
	v := int32(b[i]) | int32(b[i+1])<<8 | int32(int8(b[i+2]))<<16
	return v
}

func TestSlatePCM_LengthAndPeak(t *testing.T) {
	b := SlatePCM(1)
	if len(b) != 72000*6 {
		t.Fatalf("len = %d", len(b))
	}
	peak := 0.0
	for i := 0; i < 72000; i++ {
		l, r := sample(b, i*6), sample(b, i*6+3)
		if l != r {
			t.Fatalf("channels differ at %d", i)
		}
		peak = math.Max(peak, math.Abs(float64(l)))
	}
	db := 20 * math.Log10(peak/8388607)
	if math.Abs(db+20) > 0.1 {
		t.Fatalf("peak = %.3f dBFS", db)
	}
	// Fades: first and last tone samples are silent, and the last 0.5 s is zero.
	if sample(b, 0) != 0 {
		t.Fatal("tone does not start at zero")
	}
	for i := 48000; i < 72000; i++ {
		if sample(b, i*6) != 0 {
			t.Fatalf("non-zero sample in the silent tail at %d", i)
		}
	}
	if sample(b, 47999*6) != 0 {
		t.Fatal("tone does not end at zero")
	}
}

func TestSlatePCM_Distinct(t *testing.T) {
	a, b := SlatePCM(1), SlatePCM(2)
	same := true
	for i := range a {
		if a[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("slates 1 and 2 are identical")
	}
	if string(SlatePCM(1)) != string(SlatePCM(13)) {
		t.Fatal("slate 13 should equal slate 1")
	}
}
