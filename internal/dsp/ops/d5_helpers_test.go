package ops

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

const (
	d5Rate = 16000
	d5N    = 1024 // the phase-vocoder frame of the 16 kHz tests
)

// d5Clicks is a train of single-sample impulses of height amp at the given tempo; the first sits at first seconds.
func d5Clicks(rate int, bpm, first, secs, amp float64) []float64 {
	x := make([]float64, dsptest.Frames(rate, secs))
	for k := 0; ; k++ {
		i := int(math.Floor((first+float64(k)*60/bpm)*float64(rate) + 0.5))
		if i >= len(x) {
			return x
		}
		x[i] = amp
	}
}

// d5Align is the alignment object of a pair job with explicit downbeats.
func d5Align(bpmA, bpmB, downA, downB float64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"inputs":[{"bpm":%g,"downbeat_s":%g,"beats_per_bar":4},{"bpm":%g,"downbeat_s":%g,"beats_per_bar":4}]}`,
		bpmA, downA, bpmB, downB))
}

// d5PeakNear is the time and height of the largest |x| within +-w seconds of t.
func d5PeakNear(x []float64, rate int, t, w float64) (at, v float64) {
	lo, hi := max(int((t-w)*float64(rate)), 0), min(int((t+w)*float64(rate)), len(x)-1)
	best := lo
	for i := lo; i <= hi; i++ {
		if math.Abs(x[i]) > math.Abs(x[best]) {
			best = i
		}
	}
	return float64(best) / float64(rate), math.Abs(x[best])
}

func d5Near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f +- %.4f", what, got, want, tol)
	}
}

func d5Cents(f, ref float64) float64 { return 1200 * math.Log2(f/ref) }
