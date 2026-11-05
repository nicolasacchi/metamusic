package pv_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

func hashFloats(parts ...[]float64) string {
	h := sha256.New()
	var b [8]byte
	for _, x := range parts {
		for _, v := range x {
			binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
			h.Write(b[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func goldens() map[string]string {
	x := signal()
	p := p1024()
	free := p
	free.Lock = false
	fm := p
	fm.FormantRatio = func(m int) float64 { return 1 + float64(m%7)/10 }
	ramp := func(m int) float64 { return 1 + float64(m)/150 }
	bank, err := pv.ShiftBank(x, []pv.Voice{
		{Rho: constant(1.5), Gain: constant(0.5)},
		{Rho: func(m int) float64 { return dsp.Pow(2, -float64(m)/100) }, Formant: constant(1), Gain: func(m int) float64 { return float64(m%5) / 4 }},
	}, p)
	if err != nil {
		panic(err)
	}
	return map[string]string{
		"stretch_lock":    hashFloats(pv.Stretch(x, constant(1.25), p)),
		"stretch_free":    hashFloats(pv.Stretch(x, constant(0.8), free)),
		"stretch_ramp":    hashFloats(pv.Stretch(x, ramp, p)),
		"shift":           hashFloats(pv.Shift(x, constant(dsp.Pow(2, 3.0/12)), p)),
		"shift_formant":   hashFloats(pv.Shift(x, constant(dsp.Pow(2, -3.0/12)), fm)),
		"bank":            hashFloats(bank...),
		"stretch_default": hashFloats(pv.Stretch(dsptest.Pink(3, 9000), constant(1.1), pv.Params{Lock: true})),
	}
}

const goldenPath = "testdata/pv-golden.txt"

// TestPV_Golden pins the vocoder bit for bit on fixed inputs (linux/amd64, GOAMD64=v1).
func TestPV_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := goldens()
	names := make([]string, 0, len(got))
	for k := range got {
		names = append(names, k)
	}
	sort.Strings(names)
	if *update {
		var sb strings.Builder
		sb.WriteString("# sha256 of the little-endian bit patterns of each pv output on fixed inputs. linux/amd64, GOAMD64=v1.\n")
		for _, k := range names {
			sb.WriteString(k + " " + got[k] + "\n")
		}
		if err := os.WriteFile(goldenPath, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0][0] != '#' {
			want[f[0]] = f[1]
		}
	}
	if len(want) != len(got) {
		t.Errorf("golden file has %d entries, test makes %d", len(want), len(got))
	}
	for _, k := range names {
		if got[k] != want[k] {
			t.Errorf("%s: hash %s, golden %s", k, got[k], want[k])
		}
	}
}

// TestDeterminism_FMAOff re-executes the golden under GODEBUG=cpu.fma=off and requires the same bits (it fails if the
// runtime does not know the setting, so it can never pass vacuously).
func TestDeterminism_FMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestPV_Golden$`)
}
