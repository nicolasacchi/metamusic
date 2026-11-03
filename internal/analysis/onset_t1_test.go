package analysis

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"runtime"
	"testing"
)

// t1OnsetSignal is a fixed synthetic mono signal: 4 s at 16 kHz of a 2 Hz click train (decaying noise bursts from an integer
// LCG) over a quiet bed. It uses only exact float operations, so it is the same bits on every host.
func t1OnsetSignal() []float32 {
	const sr = 16000
	x := make([]float32, 4*sr)
	var lcg uint64 = 0x9e3779b97f4a7c15
	next := func() float64 {
		lcg = lcg*6364136223846793005 + 1442695040888963407
		return float64(int64(lcg>>11))/float64(1<<52) - 1 // [-1, 1)
	}
	for i := range x {
		v := 0.01 * next()
		if p := i % (sr / 2); p < 400 { // a 25 ms burst every 0.5 s
			v += next() * float64(400-p) / 400 * 0.8
		}
		x[i] = float32(v)
	}
	return x
}

// TestOnset_PinnedForT1 pins analysis.OnsetEnvelope for the transforms (D-T1-20): the beat-phase front end of T1 calls the
// export as shipped, and a golden hash of the envelope of a fixed synthetic signal makes a later change to analysis (a
// retune, an mm-tempo/2, a different Log1p) fail loudly. The response to a failure is a dsp_version bump in the transforms,
// never a silent update of this hash.
//
// The Go 1.27 source reading behind the decision (T1-X0 step 6): math.Log1p, Sin, Cos, Sqrt, Atan2, Log2 and Cbrt are pure Go
// (or an exact instruction) on amd64 and select no code path at run time; math.Log has an assembly body on amd64 but only
// SSE2 arithmetic; math.Exp (and so Pow, Sinh, Cosh and Tanh) picks an FMA path at run time from the CPU: those are the
// functions D-T1-3 keeps out of internal/dsp. The golden is for linux/amd64 built with GOAMD64=v1 (the default), the pinned
// target; on any other architecture the test checks the length only.
func TestOnset_PinnedForT1(t *testing.T) {
	t.Parallel()
	env := OnsetEnvelope(t1OnsetSignal(), 16000)
	if want := (4*16000-tempoWindow)/tempoHop + 1; len(env) != want {
		t.Fatalf("envelope has %d frames, want %d", len(env), want)
	}
	h := sha256.New()
	var b [8]byte
	for _, v := range env {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			t.Fatalf("envelope value %v", v)
		}
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		h.Write(b[:])
	}
	got := hex.EncodeToString(h.Sum(nil))
	const golden = "ee3ae8f2429d4c569b0d40b86ad45f886746849c479f32135e19f54fb082a90f"
	if runtime.GOARCH != "amd64" {
		t.Skipf("the golden is pinned for linux/amd64 (this is %s); got %s", runtime.GOARCH, got)
	}
	if got != golden {
		t.Errorf("OnsetEnvelope hash = %s, want %s: analysis changed, bump dsp_version in the transforms", got, golden)
	}
}
