package hrtf

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// tableSHA256 pins table.q15 byte for byte (C222.4): a rebuilt table is a deliberate change and a dsp_version bump.
const tableSHA256 = "e764dc3d9c5c045a4def2128212e51b61a1b69d5c2d007e603a6a079e0a85bb4"

func TestHRTF_TableHash(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256(tableData)
	if got := hex.EncodeToString(sum[:]); got != tableSHA256 {
		t.Fatalf("table.q15 sha256 = %s, pinned %s", got, tableSHA256)
	}
	if got := Default().Info().SHA12; got != tableSHA256[:12] {
		t.Errorf("Info.SHA12 = %s", got)
	}
}

func TestHRTF_TableShape(t *testing.T) {
	t.Parallel()
	if len(tableData) > 128<<10 {
		t.Errorf("table is %d bytes, limit 128 KiB", len(tableData))
	}
	tb := Default()
	in := tb.Info()
	for name, v := range map[string]string{"dataset": in.Dataset, "subject": in.Subject, "licence": in.Licence, "credit": in.Credit} {
		if v == "" {
			t.Errorf("header %s is empty", name)
		}
	}
	if in.Dataset != "SADIE II" || in.Licence != "Apache-2.0" {
		t.Errorf("dataset %q licence %q", in.Dataset, in.Licence)
	}
	var peak float64
	for ei, el := range Elevations {
		for az := AzMin; az <= -AzMin; az += AzStep {
			r, l := tb.IR(az, el, Right), tb.IR(-az, el, Left)
			if len(r) != Taps {
				t.Fatalf("IR length %d", len(r))
			}
			var e float64
			for n := range r {
				if r[n] != l[n] { // the left ear is the mirror of the right, exactly
					t.Fatalf("el %d az %d tap %d: right %v, mirrored left %v", el, az, n, r[n], l[n])
				}
				if math.IsNaN(r[n]) || r[n] <= -1 || r[n] >= 1 {
					t.Fatalf("el %d az %d tap %d out of range: %v", el, az, n, r[n])
				}
				e += float64(r[n] * r[n])
				peak = math.Max(peak, math.Abs(r[n]))
			}
			if e < 1e-4 {
				t.Errorf("el %d (index %d) az %d: no energy", el, ei, az)
			}
		}
	}
	if peak < 0.9 || peak > 0.97 {
		t.Errorf("table peak %.3f, want about 0.95", peak)
	}
}

func TestHRTF_Snap(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want int }{{0, 0}, {2, 0}, {3, 5}, {31, 30}, {33, 35}, {-33, -35}, {-32, -30}, {90, 90}, {200, 180}, {-200, -180}} {
		if got := SnapAz(c.in); got != c.want {
			t.Errorf("SnapAz(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want int }{{0, 0}, {7, 0}, {8, 15}, {15, 15}, {22, 15}, {23, 30}, {30, 30}, {99, 30}} {
		if got := SnapEl(c.in); got != c.want {
			t.Errorf("SnapEl(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	a, b := Default().IR(31, 7, Right), Default().IR(30, 0, Right)
	for n := range a {
		if a[n] != b[n] {
			t.Fatal("IR does not snap to the table")
		}
	}
}

// smoothedAverage is the energy average of the right-ear responses over all stored directions, 1/3-octave smoothed, in dB per
// 1024-point FFT bin: the quantity the build equalises.
func smoothedAverage(tb *Table) []float64 {
	const n = 1024
	p := make([]float64, n/2+1)
	var count float64
	for _, el := range Elevations {
		for az := AzMin; az <= -AzMin; az += AzStep {
			re, im := dsp.RFFT(tb.IR(az, el, Right), n)
			for k := range p {
				p[k] += float64(re[k]*re[k]) + float64(im[k]*im[k])
			}
			count++
		}
	}
	out := make([]float64, len(p))
	const sixth = 1.122462048309373
	for k := 1; k < len(p); k++ {
		lo, hi := max(int(math.Ceil(float64(k)/sixth)), 1), min(int(math.Floor(float64(k)*sixth)), len(p)-1)
		var s float64
		for j := lo; j <= hi; j++ {
			s += p[j]
		}
		out[k] = 10 * math.Log10(s/float64(hi-lo+1)/count)
	}
	return out
}

// TestHRTF_DiffuseFieldFlat: the diffuse-field equalisation holds after truncation and quantisation (C222.4): the
// energy-averaged, 1/3-octave-smoothed magnitude is flat within +-1 dB from 200 Hz to 10 kHz.
func TestHRTF_DiffuseFieldFlat(t *testing.T) {
	t.Parallel()
	db := smoothedAverage(Default())
	k0, k1 := 200*1024/Rate, 10000*1024/Rate
	var mean float64
	for k := k0; k <= k1; k++ {
		mean += db[k]
	}
	mean /= float64(k1 - k0 + 1)
	for k := k0; k <= k1; k++ {
		if math.Abs(db[k]-mean) > 1 {
			t.Errorf("%.0f Hz: %.2f dB from the band mean", float64(k)*Rate/1024, db[k]-mean)
		}
	}
}

func TestHRTF_NoticePresent(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	in := Default().Info()
	for _, want := range []string{"SADIE II", "Apache License, Version 2.0", "University of York", "10.3390/app8112029", "tools/hrtfgen"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("NOTICE does not mention %q", want)
		}
	}
	if !strings.Contains(in.Credit, "SADIE II") || !strings.Contains(in.Credit, in.Licence) {
		t.Errorf("credit %q names neither the dataset nor the licence", in.Credit)
	}
}

// TestGate_NoRawHRIRCommitted: no raw dataset file is tracked (the gate refuses *.sofa, *.hrir.zip, *.hrirs and any audio).
func TestGate_NoRawHRIRCommitted(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "ls-files")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	for _, f := range strings.Fields(string(out)) {
		l := strings.ToLower(f)
		for _, bad := range []string{".sofa", ".hrir.zip", ".hrirs", ".wav"} {
			if strings.HasSuffix(l, bad) {
				t.Errorf("tracked raw dataset or audio file: %s", f)
			}
		}
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()
	for name, b := range map[string][]byte{
		"empty":     nil,
		"magic":     append([]byte("XXXXXXXX"), tableData[8:]...),
		"truncated": tableData[:len(tableData)-2],
		"header":    append(append([]byte(magic), 0xff, 0xff, 0xff, 0x7f), tableData[12:]...),
		"json":      append(append([]byte(magic), 2, 0, 0, 0, '{', '}'), tableData[12:]...),
	} {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
