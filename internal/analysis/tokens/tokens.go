package tokens

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
)

// ErrUnsupported is returned for arrays whose layout cannot be read as token streams.
var ErrUnsupported = errors.New("tokens: unsupported array")

const (
	maxStreams  = 16
	maxPeriod   = 400
	windowS     = 10.0
	hashBase    = 0x9E3779B97F4A7C15 // odd 64-bit multiplier
	maxToken    = math.MaxUint32
	minPeriodic = 2 // a periodic run must contain at least two full periods
)

// Version is the stored analyses.version for token loops (D-M2b-11).
func Version() string { return analysis.TokenLoopDef + " " + runtime.Version() }

func r2(v float64) float64 { return math.Round(v*100) / 100 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }
func r4(v float64) float64 { return math.Round(v*10000) / 10000 }

// Analyze measures repetition in every stream of a semantic token array.
// The time axis is the longest axis (ties: the last); the other axes are
// flattened row-major into at most 16 streams.
func Analyze(a npy.Array, durationS float64) (Report, error) {
	if !(durationS > 0) || math.IsInf(durationS, 0) {
		return Report{}, errors.New("tokens: duration must be positive")
	}
	if len(a.Shape) == 0 || len(a.Shape) > 3 {
		return Report{}, fmt.Errorf("%w: %d dimensions", ErrUnsupported, len(a.Shape))
	}
	total := 1
	for _, d := range a.Shape {
		total *= d
	}
	if total != len(a.Data) {
		return Report{}, errors.New("tokens: shape does not match data")
	}
	axis := 0
	for i, d := range a.Shape {
		if d >= a.Shape[axis] {
			axis = i
		}
	}
	n := a.Shape[axis]
	if n == 0 {
		return Report{}, fmt.Errorf("%w: empty time axis", ErrUnsupported)
	}
	streams := total / n
	if streams > maxStreams {
		return Report{}, fmt.Errorf("%w: %d streams", ErrUnsupported, streams)
	}
	// stride of the time axis and decomposition of the stream index
	stride := 1
	for _, d := range a.Shape[axis+1:] {
		stride *= d
	}
	var others []int // sizes of the non-time axes, in order
	var otherStride []int
	for i, d := range a.Shape {
		if i == axis {
			continue
		}
		s := 1
		for _, e := range a.Shape[i+1:] {
			s *= e
		}
		others = append(others, d)
		otherStride = append(otherStride, s)
	}
	rep := Report{DType: a.DType, Shape: slices.Clone(a.Shape), TimeAxis: axis, Streams: make([]Stream, 0, streams)}
	for si := 0; si < streams; si++ {
		off, rem := 0, si
		for j := len(others) - 1; j >= 0; j-- {
			off += (rem % others[j]) * otherStride[j]
			rem /= others[j]
		}
		toks := make([]uint32, n)
		for i := range toks {
			v := a.Data[off+i*stride]
			if v < 0 || v > maxToken {
				return Report{}, fmt.Errorf("%w: token %d out of range", ErrUnsupported, v)
			}
			toks[i] = uint32(v)
		}
		rep.Streams = append(rep.Streams, analyzeStream(si, toks, durationS))
	}
	return rep, nil
}

func analyzeStream(index int, t []uint32, durationS float64) Stream {
	n := len(t)
	tps := float64(n) / durationS
	s := Stream{Index: index, NTokens: n, TokensPerS: r2(tps), Encoding: "u16le", GzipRatio10s: []float64{}}
	distinct := map[uint32]struct{}{}
	for _, v := range t {
		distinct[v] = struct{}{}
		if v >= 1<<16 {
			s.Encoding = "u32le"
		}
	}
	s.DistinctTokens = len(distinct)
	grams := map[[4]uint32]struct{}{}
	for i := 0; i+4 <= n; i++ {
		grams[[4]uint32{t[i], t[i+1], t[i+2], t[i+3]}] = struct{}{}
	}
	s.Distinct4GramRatio = r4(float64(len(grams)) / float64(max(1, n-3)))
	s.LongestRepeat = longestRepeat(t, tps)
	s.PeriodicRun = periodicRun(t, tps)
	s.GzipRatio10s = gzipRatios(t, tps, s.Encoding)
	if len(s.GzipRatio10s) > 0 {
		sorted := slices.Clone(s.GzipRatio10s)
		slices.Sort(sorted)
		mn := sorted[0]
		med := sorted[len(sorted)/2]
		if len(sorted)%2 == 0 {
			med = r4((sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2)
		}
		s.GzipRatioMin, s.GzipRatioMedian = &mn, &med
	}
	return s
}

// findRepeat returns the earliest-second pair (p1 < p2, p2-p1 >= l) of equal
// windows of length l, or ok == false.
func findRepeat(t []uint32, l int, pow uint64) (p1, p2 int, ok bool) {
	n := len(t)
	if l < 1 || 2*l > n {
		return 0, 0, false
	}
	var h uint64
	for i := 0; i < l; i++ {
		h = h*hashBase + uint64(t[i]) + 1
	}
	first := make(map[uint64]int, n-l+1)
	for p := 0; ; p++ {
		if q, seen := first[h]; seen {
			if p-q >= l && slices.Equal(t[q:q+l], t[p:p+l]) {
				return q, p, true
			}
		} else {
			first[h] = p
		}
		if p+l >= n {
			break
		}
		h = h*hashBase - (uint64(t[p])+1)*pow + uint64(t[p+l]) + 1
	}
	return 0, 0, false
}

func longestRepeat(t []uint32, tps float64) Repeat {
	n := len(t)
	lo, hi := 0, n/2
	var best Repeat
	powFor := func(l int) uint64 {
		p := uint64(1)
		for i := 0; i < l; i++ {
			p *= hashBase
		}
		return p
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if p1, p2, ok := findRepeat(t, mid, powFor(mid)); ok {
			lo = mid
			best = Repeat{Tokens: mid, S: r3(float64(mid) / tps), FirstAtS: r3(float64(p1) / tps), SecondAtS: r3(float64(p2) / tps)}
		} else {
			hi = mid - 1
		}
	}
	return best
}

func periodicRun(t []uint32, tps float64) Periodic {
	n := len(t)
	maxP := min(maxPeriod, max(1, int(math.Round(2*tps))))
	var best Periodic
	bestSpan, bestStart := 0, 0
	for p := 1; p <= maxP && p < n; p++ {
		run, bestRun, end := 0, 0, 0
		for i := p; i < n; i++ {
			if t[i] == t[i-p] {
				run++
				if run > bestRun {
					bestRun, end = run, i
				}
			} else {
				run = 0
			}
		}
		if bestRun < p*(minPeriodic-1) {
			continue
		}
		if span := bestRun + p; span > bestSpan {
			bestSpan, bestStart = span, end-bestRun+1-p
			best = Periodic{
				PeriodTokens: p, PeriodS: r3(float64(p) / tps),
				SpanTokens: span, SpanS: r3(float64(span) / tps),
				Repeats: r2(float64(span) / float64(p)),
			}
		}
	}
	if bestSpan > 0 {
		best.AtS = r3(float64(bestStart) / tps)
	}
	return best
}

func gzipRatios(t []uint32, tps float64, enc string) []float64 {
	w := int(math.Round(windowS * tps))
	out := []float64{}
	if w < 1 {
		return out
	}
	size := 2
	if enc == "u32le" {
		size = 4
	}
	for start := 0; start < len(t); start += w {
		end := min(start+w, len(t))
		if end-start < w && 2*(end-start) < w {
			break
		}
		raw := make([]byte, 0, (end-start)*size)
		for _, v := range t[start:end] {
			if size == 2 {
				raw = binary.LittleEndian.AppendUint16(raw, uint16(v)) // #nosec G115 -- v < 65536 for u16le
			} else {
				raw = binary.LittleEndian.AppendUint32(raw, v)
			}
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		out = append(out, r4(float64(buf.Len())/float64(len(raw))))
	}
	return out
}
