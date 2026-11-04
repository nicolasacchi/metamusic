package safety

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// The lookahead true-peak limiter of C207.3. The detector is the 4x-oversampled peak of both channels linked (so no image
// shift between channels); the gain needed per frame goes through a sliding minimum over the 5 ms lookahead, then through two
// cascaded boxcars whose combined support is exactly the lookahead (a triangular, non-negative smoother: every output is a
// weighted mean of values at least as small as the gain the frame itself needs, so the attack never overshoots), and finally
// a first-order release of 80 ms that only ever lowers the gain. The processing is offline, so the lookahead needs no delay
// line: the smoother looks ahead instead (delay-compensated by construction).

const (
	lookaheadSec = 0.005
	releaseSec   = 0.080
	// engagedBelow is the gain under which a frame counts as limited (-0.00001 dB).
	engagedBelow = 1 - 1e-6
)

// LimiterInfo reports what the limiter did.
type LimiterInfo struct {
	MaxReductionDB  float64 // the deepest gain reduction in dB (>= 0)
	EngagedFraction float64 // the fraction of frames with a reduced gain
}

// Limit limits b in place so its true peak stays at or under ceilingDBTP. The meter is charged but a refusal cannot be
// returned (Limit is O(n) and runs inside Finish); a caller that cares reads m.Used.
func Limit(b *pcm.Buffer, ceilingDBTP float64, m *dsp.Meter) LimiterInfo {
	_ = m.Add(int64(b.Frames()) * 80)
	return limitInto(b, *b, framePeaks(*b), 1, ceilingDBTP)
}

// slidingMinAhead is out[i] = min(g[i .. i+look]).
func slidingMinAhead(g []float64, look int) []float64 {
	n := len(g)
	out := make([]float64, n)
	dq := make([]int, 0, 2*look+2)
	head := 0
	for i := n - 1; i >= 0; i-- {
		for len(dq) > head && g[dq[len(dq)-1]] >= g[i] {
			dq = dq[:len(dq)-1]
		}
		dq = append(dq, i)
		for dq[head] > i+look {
			head++
		}
		out[i] = g[dq[head]]
		if head > 1<<16 { // keep the backing array small on long inputs
			dq = append(dq[:0], dq[head:]...)
			head = 0
		}
	}
	return out
}

// boxcarBack is out[i] = mean(g[i-w+1 .. i]) with g extended to the left by g[0].
func boxcarBack(g []float64, w int) []float64 {
	n := len(g)
	pre := make([]float64, n+w+1) // pre[k] = sum of the first k elements of the extended series
	for k := 1; k <= n+w; k++ {
		v := g[0]
		if k > w {
			v = g[k-w-1]
		}
		pre[k] = pre[k-1] + v
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = (pre[i+w+1] - pre[i+1]) / float64(w)
	}
	return out
}

// limitInto writes src * gain, limited to ceilingDBTP, into dst (which may alias src), given src's detector peaks det
// (framePeaks). The linear detector means det scales with gain, so Finish measures it once for all its trial gains.
func limitInto(dst *pcm.Buffer, src pcm.Buffer, det []float64, gain, ceilingDBTP float64) LimiterInfo {
	n := src.Frames()
	c := dsp.Pow(10, ceilingDBTP/20)
	need := make([]float64, n)
	limited := false
	for i, d := range det {
		need[i] = 1
		if p := float64(d * gain); p > c {
			need[i] = c / p
			limited = true
		}
	}
	g := make([]float64, n)
	for i := range g {
		g[i] = 1
	}
	var info LimiterInfo
	if limited {
		look := max(int(math.Floor(float64(src.Rate)*lookaheadSec+0.5)), 1)
		a := look/2 + 1
		sm := boxcarBack(boxcarBack(slidingMinAhead(need, look), a), look+2-a)
		alpha := dsp.Exp(-1 / (releaseSec * float64(src.Rate)))
		prev, minG, engaged := 1.0, 1.0, 0
		for i := range g {
			rec := prev + float64((1-alpha)*(1-prev))
			v := math.Min(sm[i], rec)
			g[i], prev = v, v
			minG = math.Min(minG, v)
			if v < engagedBelow {
				engaged++
			}
		}
		info = LimiterInfo{MaxReductionDB: float64(-2 * db10(minG)), EngagedFraction: float64(engaged) / float64(n)}
	}
	for c := range 2 {
		for i := range n {
			dst.Ch[c][i] = float64(src.Ch[c][i] * float64(gain*g[i]))
		}
	}
	return info
}
