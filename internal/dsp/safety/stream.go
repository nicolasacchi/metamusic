package safety

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// FinishStream is Finish for signals that do not fit in memory (T1-D5): the same pipeline, the same arithmetic in the same order,
// so the finished samples and the report are bit for bit those of Finish on the same signal (TestFinishStream_MatchesFinish),
// but the signal is read block by block from a re-readable Source and only O(block + look-ahead) values are held; the 8 bytes
// a frame of the detector and 16 bytes a frame of the limited signal live in scratch spools, not in memory. The cost is passes
// over the data: one scan, one analysis, up to nine trial limiter runs, the final limiter run, a measurement and the delivery.
//
// This file repeats the arithmetic of finish.go, limit.go, lufs.go and truepeak.go in streaming form (their whole-slice
// helpers cannot be shared without changing those files); the parity test is what keeps the copies honest.

// StreamBlock is the number of frames a pass reads at a time.
const StreamBlock = 1 << 15

// streamBlock is StreamBlock; the parity test lowers it to put block boundaries in awkward places.
var streamBlock = StreamBlock

// FrameReader reads a stereo signal sequentially.
type FrameReader interface {
	// ReadFrames fills l and r (equal length) with the next frames and returns how many it filled: len(l) unless the end of the
	// signal is near. At the end it returns 0 and io.EOF.
	ReadFrames(l, r []float64) (int, error)
	Close() error
}

// Source is a stereo signal that can be read from the start any number of times.
type Source interface {
	Frames() int
	Rate() int
	Open() (FrameReader, error)
}

// Spool is a scratch file of raw bytes: written once with Write, then read from the start any number of times with Open. Close
// deletes it.
type Spool interface {
	io.Writer
	Open() (io.ReadCloser, error)
	Close() error
}

// Scratch makes spools.
type Scratch interface {
	NewSpool() (Spool, error)
}

// ---- frames on disk and in memory -----------------------------------------------------------------------------------------

// PutFrames writes frames as interleaved little-endian float64 pairs.
func PutFrames(w io.Writer, l, r []float64) error {
	buf := make([]byte, 16*len(l))
	for i := range l {
		binary.LittleEndian.PutUint64(buf[16*i:], math.Float64bits(l[i]))
		binary.LittleEndian.PutUint64(buf[16*i+8:], math.Float64bits(r[i]))
	}
	_, err := w.Write(buf)
	return err
}

type spoolReader struct {
	rc  io.ReadCloser
	buf []byte
}

func (s *spoolReader) ReadFrames(l, r []float64) (int, error) {
	need := 16 * len(l)
	if cap(s.buf) < need {
		s.buf = make([]byte, need)
	}
	n, err := io.ReadFull(s.rc, s.buf[:need])
	frames := n / 16
	if frames == 0 {
		if err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
			err = io.EOF
		}
		return 0, err
	}
	for i := 0; i < frames; i++ {
		l[i] = math.Float64frombits(binary.LittleEndian.Uint64(s.buf[16*i:]))
		r[i] = math.Float64frombits(binary.LittleEndian.Uint64(s.buf[16*i+8:]))
	}
	return frames, nil
}

func (s *spoolReader) Close() error { return s.rc.Close() }

type spoolSource struct {
	rate, frames int
	sp           Spool
}

// SpoolSource is the signal of frames interleaved float64 pairs (PutFrames) in sp.
func SpoolSource(rate, frames int, sp Spool) Source { return spoolSource{rate, frames, sp} }

func (s spoolSource) Frames() int { return s.frames }
func (s spoolSource) Rate() int   { return s.rate }
func (s spoolSource) Open() (FrameReader, error) {
	rc, err := s.sp.Open()
	if err != nil {
		return nil, err
	}
	return &spoolReader{rc: rc}, nil
}

type bufReader struct {
	b   pcm.Buffer
	pos int
}

func (r *bufReader) ReadFrames(l, rr []float64) (int, error) {
	if r.pos >= r.b.Frames() {
		return 0, io.EOF
	}
	n := min(len(l), r.b.Frames()-r.pos)
	copy(l, r.b.Ch[0][r.pos:r.pos+n])
	copy(rr, r.b.Ch[1][r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func (r *bufReader) Close() error { return nil }

type bufSource struct{ b pcm.Buffer }

// BufferSource is a signal held in memory.
func BufferSource(b pcm.Buffer) Source { return bufSource{b} }

func (s bufSource) Frames() int { return s.b.Frames() }
func (s bufSource) Rate() int   { return s.b.Rate }
func (s bufSource) Open() (FrameReader, error) {
	return &bufReader{b: s.b}, nil
}

// f64Reader reads the float64 stream of the detector spool.
type f64Reader struct {
	rc  io.ReadCloser
	buf []byte
}

func (f *f64Reader) read(dst []float64) (int, error) {
	need := 8 * len(dst)
	if cap(f.buf) < need {
		f.buf = make([]byte, need)
	}
	n, err := io.ReadFull(f.rc, f.buf[:need])
	k := n / 8
	for i := 0; i < k; i++ {
		dst[i] = math.Float64frombits(binary.LittleEndian.Uint64(f.buf[8*i:]))
	}
	if k == 0 && (err == nil || errors.Is(err, io.ErrUnexpectedEOF)) {
		err = io.EOF
	}
	if k > 0 {
		err = nil
	}
	return k, err
}

func putF64(w io.Writer, v []float64) error {
	buf := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint64(buf[8*i:], math.Float64bits(x))
	}
	_, err := w.Write(buf)
	return err
}

// ---- the conditioned signal: DC removal and fades (finish.go removeDC, fade) -------------------------------------------------

type conditioner struct {
	rd   FrameReader
	n    int
	idx  int
	mean [2]float64
	dc   [2]bool
	f    int
	g    []float64
}

func newConditioner(rd FrameReader, n, rate int, mean [2]float64, dc [2]bool, fades bool) *conditioner {
	c := &conditioner{rd: rd, n: n, mean: mean, dc: dc}
	if fades {
		c.f = min(int(math.Floor(float64(rate)*fadeSec+0.5)), n/2)
		c.g = make([]float64, c.f)
		for i := range c.g {
			c.g[i] = 0.5 - float64(0.5*math.Cos(math.Pi*float64(i)/float64(c.f)))
		}
	}
	return c
}

func (c *conditioner) ReadFrames(l, r []float64) (int, error) {
	n, err := c.rd.ReadFrames(l, r)
	if n == 0 {
		return 0, err
	}
	for i := 0; i < n; i++ {
		lv, rv := l[i], r[i]
		if c.dc[0] {
			lv -= c.mean[0]
		}
		if c.dc[1] {
			rv -= c.mean[1]
		}
		if k := c.idx + i; c.f > 0 {
			switch {
			case k < c.f:
				lv, rv = float64(lv*c.g[k]), float64(rv*c.g[k])
			case k >= c.n-c.f:
				j := c.n - 1 - k
				lv, rv = float64(lv*c.g[j]), float64(rv*c.g[j])
			}
		}
		l[i], r[i] = lv, rv
	}
	c.idx += n
	return n, nil
}

func (c *conditioner) Close() error { return c.rd.Close() }

// ---- loudness (lufs.go) -------------------------------------------------------------------------------------------------------

// kState is one K-weighting section with its state, the sample-at-a-time form of biquad.run.
type kState struct {
	q      biquad
	z1, z2 float64
}

func (s *kState) step(v float64) float64 {
	o := float64(s.q.b0*v) + s.z1
	s.z1 = float64(s.q.b1*v) - float64(s.q.a1*o) + s.z2
	s.z2 = float64(s.q.b2*v) - float64(s.q.a2*o)
	return o
}

// kMeter accumulates the K-weighted square sums of kWeightedSquares over a stream.
type kMeter struct {
	shelf, hp [2]kState
	total     [2]float64
	sums      [2][]float64
	step      int
	frames    int
	rate      int
	idx       int
}

func newKMeter(rate, frames int) *kMeter {
	k := &kMeter{step: int(math.Floor(float64(rate)*0.1 + 0.5)), frames: frames, rate: rate}
	sh, hp := kFilters(rate)
	for c := range 2 {
		k.shelf[c].q, k.hp[c].q = sh, hp
	}
	if k.step >= 1 {
		for c := range 2 {
			k.sums[c] = make([]float64, frames/k.step)
		}
	}
	return k
}

func (k *kMeter) add(l, r []float64) {
	if k.step < 1 {
		return
	}
	nfull := k.frames / k.step
	for c, x := range [2][]float64{l, r} {
		for i, v := range x {
			y := k.hp[c].step(k.shelf[c].step(v))
			sq := float64(y * y)
			k.total[c] += sq
			if j := (k.idx + i) / k.step; j < nfull {
				k.sums[c][j] += sq
			}
		}
	}
	k.idx += len(l)
}

// lufs is LUFS of the stream added so far (blockEnergies, then the gates).
func (k *kMeter) lufs() (float64, bool) {
	if k.step < 1 || k.frames == 0 {
		return 0, false
	}
	block := 4 * k.step
	var z []float64
	if k.frames < block {
		z = []float64{(k.total[0] + k.total[1]) / float64(block)}
	} else {
		nb := len(k.sums[0]) - 3
		z = make([]float64, 0, max(nb, 0))
		for j := 0; j < nb; j++ {
			var e float64
			for c := range 2 {
				e += (k.sums[c][j] + k.sums[c][j+1] + k.sums[c][j+2] + k.sums[c][j+3]) / float64(block)
			}
			z = append(z, e)
		}
	}
	return lufsOfBlocks(z)
}

// lufsOfBlocks is the gating half of LUFS.
func lufsOfBlocks(z []float64) (float64, bool) {
	loud := func(e float64) float64 { return lkOffset + db10(e) }
	var sum float64
	var n int
	for _, e := range z {
		if e > 0 && loud(e) > absGateLUFS {
			sum += e
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	gate := loud(sum/float64(n)) + relGateLU
	sum, n = 0, 0
	for _, e := range z {
		if e > 0 && loud(e) > absGateLUFS && loud(e) > gate {
			sum += e
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return loud(sum / float64(n)), true
}

// ---- the detector (truepeak.go framePeaks) --------------------------------------------------------------------------------

// peakScan computes framePeaks over a stream: det[i] needs the samples i-5 .. i+6, so a value is emitted once its look-ahead has
// arrived (or the stream has ended).
type peakScan struct {
	n    int
	tb   *[3][tpTaps]float64
	ext  [2][]float64
	base int // global index of ext[.][0]
	next int // the next index to emit
}

func newPeakScan(n int) *peakScan { return &peakScan{n: n, tb: tpTable()} }

// feed adds a block (nil at the end with final set) and calls emit for every detector value that is ready, in order.
func (p *peakScan) feed(l, r []float64, final bool, emit func(det []float64) error) error {
	p.ext[0] = append(p.ext[0], l...)
	p.ext[1] = append(p.ext[1], r...)
	avail := p.base + len(p.ext[0])
	var out []float64
	for p.next < p.n && (final || p.next+tpTaps-tpLead <= avail) {
		out = append(out, p.at(p.next))
		p.next++
	}
	if keep := max(p.next-tpLead, 0); keep > p.base {
		for c := range 2 {
			p.ext[c] = append(p.ext[c][:0], p.ext[c][keep-p.base:]...)
		}
		p.base = keep
	}
	if len(out) == 0 {
		return nil
	}
	return emit(out)
}

func (p *peakScan) at(i int) float64 {
	var out float64
	for c := range 2 {
		x := p.ext[c]
		m := math.Abs(x[i-p.base])
		for ph := range 3 {
			var acc float64
			for j := range tpTaps {
				if k := i + j - tpLead; k >= 0 && k < p.n {
					acc += float64(x[k-p.base] * p.tb[ph][j])
				}
			}
			m = math.Max(m, math.Abs(acc))
		}
		if m > out {
			out = m
		}
	}
	return out
}

// ---- the limiter (limit.go limitInto) -------------------------------------------------------------------------------------

// boxcarStream is boxcarBack as a stream: out[i] = mean(g[i-w+1 .. i]) with g extended to the left by g[0], from the same
// running prefix sums (pre[k] = pre[k-1] + v), kept in a ring of w+1 values.
type boxcarStream struct {
	w    int
	ring []float64
	last float64 // pre[i+w]
	i    int
}

func (b *boxcarStream) push(g float64) float64 {
	if b.ring == nil {
		b.ring = make([]float64, b.w+1)
		for k := 1; k <= b.w; k++ {
			b.ring[k%(b.w+1)] = b.ring[(k-1)%(b.w+1)] + g
		}
		b.last = b.ring[b.w%(b.w+1)]
	}
	np := b.last + g // pre[i+w+1]
	out := (np - b.ring[(b.i+1)%(b.w+1)]) / float64(b.w)
	b.ring[b.i%(b.w+1)] = np
	b.last = np
	b.i++
	return out
}

// limStream turns the detector values of a signal into the limiter's gain per frame, g[i] of limitInto.
type limStream struct {
	n       int
	limited bool
	cLin    float64
	gain    float64
	look    int
	// slidingMinAhead as a forward monotone deque over need
	dqIdx       []int
	dqVal       []float64
	pushed      int
	emitted     int
	box1, box2  boxcarStream
	alpha, prev float64
	minG        float64
	engaged     int
	count       int
}

func newLimStream(n, rate int, gain, ceilingDBTP float64, limited bool) *limStream {
	l := &limStream{n: n, limited: limited, gain: gain, cLin: dsp.Pow(10, ceilingDBTP/20), prev: 1, minG: 1}
	if limited {
		l.look = max(int(math.Floor(float64(rate)*lookaheadSec+0.5)), 1)
		a := l.look/2 + 1
		l.box1.w, l.box2.w = a, l.look+2-a
		l.alpha = dsp.Exp(-1 / (releaseSec * float64(rate)))
	}
	return l
}

// push consumes detector values and appends the gains that are ready to out. final is set with the last call.
func (l *limStream) push(det []float64, final bool, out []float64) []float64 {
	if !l.limited {
		for range det {
			out = append(out, 1)
		}
		l.count += len(det)
		return out
	}
	emit := func(sm float64) {
		g := l.box2.push(l.box1.push(sm))
		rec := l.prev + float64((1-l.alpha)*(1-l.prev))
		v := math.Min(g, rec)
		l.prev = v
		l.minG = math.Min(l.minG, v)
		if v < engagedBelow {
			l.engaged++
		}
		l.count++
		out = append(out, v)
	}
	for _, d := range det {
		need := 1.0
		if p := float64(d * l.gain); p > l.cLin {
			need = l.cLin / p
		}
		for len(l.dqVal) > 0 && l.dqVal[len(l.dqVal)-1] >= need {
			l.dqVal, l.dqIdx = l.dqVal[:len(l.dqVal)-1], l.dqIdx[:len(l.dqIdx)-1]
		}
		l.dqVal, l.dqIdx = append(l.dqVal, need), append(l.dqIdx, l.pushed)
		if i := l.pushed - l.look; i >= 0 {
			for l.dqIdx[0] < i {
				l.dqVal, l.dqIdx = l.dqVal[1:], l.dqIdx[1:]
			}
			emit(l.dqVal[0])
			l.emitted++
		}
		l.pushed++
	}
	if final {
		for i := l.emitted; i < l.n; i++ {
			for l.dqIdx[0] < i {
				l.dqVal, l.dqIdx = l.dqVal[1:], l.dqIdx[1:]
			}
			emit(l.dqVal[0])
			l.emitted++
		}
	}
	return out
}

func (l *limStream) info() LimiterInfo {
	if !l.limited {
		return LimiterInfo{}
	}
	return LimiterInfo{MaxReductionDB: float64(-2 * db10(l.minG)), EngagedFraction: float64(l.engaged) / float64(l.n)}
}

// ---- the stages ---------------------------------------------------------------------------------------------------------------

type streamer struct {
	ctx     context.Context
	sc      Scratch
	src     Source
	n, rate int
	spools  []Spool
}

func (s *streamer) release() {
	for _, sp := range s.spools {
		_ = sp.Close()
	}
}

func (s *streamer) spool() (Spool, error) {
	sp, err := s.sc.NewSpool()
	if err != nil {
		return nil, err
	}
	s.spools = append(s.spools, sp)
	return sp, nil
}

// each reads rd to the end in blocks and calls fn on each.
func (s *streamer) each(rd FrameReader, fn func(l, r []float64) error) error {
	defer func() { _ = rd.Close() }()
	l, r := make([]float64, streamBlock), make([]float64, streamBlock)
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		n, err := rd.ReadFrames(l, r)
		if n > 0 {
			if ferr := fn(l[:n], r[:n]); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// detect computes the detector of a stream; with w set the values are also written to it. It returns the largest.
func (s *streamer) detect(rd FrameReader, w io.Writer) (float64, error) {
	ps := newPeakScan(s.n)
	var maxDet float64
	emit := func(det []float64) error {
		for _, v := range det {
			if v > maxDet {
				maxDet = v
			}
		}
		if w != nil {
			return putF64(w, det)
		}
		return nil
	}
	if err := s.each(rd, func(l, r []float64) error { return ps.feed(l, r, false, emit) }); err != nil {
		return 0, err
	}
	return maxDet, ps.feed(nil, nil, true, emit)
}

// limitPass is limitInto as a pass: it reads the signal from sig and its detector from det, limits src*gain to the ceiling and
// hands the result to emit block by block.
func (s *streamer) limitPass(sig FrameReader, det Spool, maxDet, gain, ceil float64, emit func(l, r []float64) error) (LimiterInfo, error) {
	limited := float64(maxDet*gain) > dsp.Pow(10, ceil/20)
	ls := newLimStream(s.n, s.rate, gain, ceil, limited)
	drc, err := det.Open()
	if err != nil {
		_ = sig.Close()
		return LimiterInfo{}, err
	}
	defer func() { _ = drc.Close() }()
	dr := &f64Reader{rc: drc}
	var pendL, pendR, gs []float64
	dblock := make([]float64, streamBlock)
	apply := func() error {
		m := min(len(gs), len(pendL))
		if m == 0 {
			return nil
		}
		ol, or := make([]float64, m), make([]float64, m)
		for i := range m {
			f := float64(gain * gs[i])
			ol[i], or[i] = float64(pendL[i]*f), float64(pendR[i]*f)
		}
		gs, pendL, pendR = gs[m:], pendL[m:], pendR[m:]
		return emit(ol, or)
	}
	err = s.each(sig, func(l, r []float64) error {
		k, derr := dr.read(dblock[:len(l)])
		if k != len(l) {
			if derr == nil || errors.Is(derr, io.EOF) {
				derr = io.ErrUnexpectedEOF
			}
			return derr
		}
		pendL, pendR = append(pendL, l...), append(pendR, r...)
		gs = ls.push(dblock[:k], false, gs)
		return apply()
	})
	if err != nil {
		return LimiterInfo{}, err
	}
	gs = ls.push(nil, true, gs)
	if err := apply(); err != nil {
		return LimiterInfo{}, err
	}
	return ls.info(), nil
}

// FinishStream runs Finish on src and hands the finished signal to sink in order, block by block. The report and the samples are
// those of Finish on the same signal. sc provides scratch storage for the detector and the limited signal (24 bytes a frame);
// ctx is polled at every block.
func FinishStream(ctx context.Context, src Source, in FinishIn, sc Scratch, sink func(l, r []float64) error) (FinishReport, error) {
	return finishStream(ctx, src, in, sc, sink, ceilingSlackDB)
}

// finishStream is FinishStream with the ceiling slack as a parameter, so the test can force the re-limit path (an honest render
// never reaches it) and compare it with the same path of Finish.
func finishStream(ctx context.Context, src Source, in FinishIn, sc Scratch, sink func(l, r []float64) error, slack float64) (FinishReport, error) {
	s := &streamer{ctx: ctx, sc: sc, src: src, n: src.Frames(), rate: src.Rate()}
	defer s.release()
	rep := FinishReport{TargetLUFSRequested: in.TargetLUFS, RefLUFS: in.RefLUFS, CeilingDBTP: in.Ceiling()}
	target := in.TargetLUFS
	if target == 0 {
		target = DefaultTargetLUFS
		rep.TargetLUFSRequested = target
	}
	// the finite and silence checks, and the channel means (removeDC)
	var peak float64
	var sum [2]float64
	rd, err := src.Open()
	if err != nil {
		return rep, err
	}
	finite := true
	if err := s.each(rd, func(l, r []float64) error {
		for c, x := range [2][]float64{l, r} {
			for _, v := range x {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					finite = false
				}
				peak = math.Max(peak, math.Abs(v))
				sum[c] += v
			}
		}
		return nil
	}); err != nil {
		return rep, err
	}
	if !finite {
		return rep, ErrNonFinite
	}
	if peak == 0 || float64(2*db10(peak)) < silentPeakDBFS {
		return rep, ErrSilent
	}
	var mean [2]float64
	var dc [2]bool
	for c := range 2 {
		mean[c] = sum[c] / float64(s.n)
		if math.Abs(mean[c]) > dcThreshold {
			dc[c] = true
			rep.DCRemoved = true
		}
	}
	conditioned := func() (FrameReader, error) {
		rd, err := src.Open()
		if err != nil {
			return nil, err
		}
		return newConditioner(rd, s.n, s.rate, mean, dc, !in.OwnFades), nil
	}
	// the loudness and the detector of the conditioned signal
	detSp, err := s.spool()
	if err != nil {
		return rep, err
	}
	km := newKMeter(s.rate, s.n)
	ps := newPeakScan(s.n)
	var maxDet float64
	emitDet := func(det []float64) error {
		for _, v := range det {
			if v > maxDet {
				maxDet = v
			}
		}
		return putF64(detSp, det)
	}
	cr, err := conditioned()
	if err != nil {
		return rep, err
	}
	if err := s.each(cr, func(l, r []float64) error {
		km.add(l, r)
		return ps.feed(l, r, false, emitDet)
	}); err != nil {
		return rep, err
	}
	if err := ps.feed(nil, nil, true, emitDet); err != nil {
		return rep, err
	}
	l0, ok := km.lufs()
	if !ok {
		return rep, ErrSilent
	}
	rep.InputLUFS = l0
	ceil := rep.CeilingDBTP
	gainFor := func(t float64) float64 { return dsp.Pow(10, (t-l0)/20) }
	var serr error
	shortfall := func(t float64) float64 {
		cr, err := conditioned()
		if err != nil {
			serr = err
			return math.Inf(1)
		}
		tk := newKMeter(s.rate, s.n)
		if _, err := s.limitPass(cr, detSp, maxDet, gainFor(t), ceil, func(l, r []float64) error { tk.add(l, r); return nil }); err != nil {
			serr = err
			return math.Inf(1)
		}
		l, ok := tk.lufs()
		if !ok {
			return math.Inf(1)
		}
		return t - l
	}
	eff := target
	sf0 := shortfall(target)
	if serr == nil && sf0 > MaxLimitingLU {
		rep.Limited = true
		hi := target
		lo := math.Min(l0+ceil-float64(2*db10(maxDet)), hi-0.01)
		guess := target - (sf0 - MaxLimitingLU)
		if guess > lo && guess < hi {
			if shortfall(guess) <= MaxLimitingLU {
				lo = guess
			} else {
				hi = guess
			}
		}
		for range bisections - 1 {
			mid := (lo + hi) / 2
			if shortfall(mid) <= MaxLimitingLU {
				lo = mid
			} else {
				hi = mid
			}
		}
		eff = lo
	}
	if serr != nil {
		return rep, serr
	}
	rep.TargetLUFSEffective = eff
	rep.GainDB = eff - l0
	// the final limiter run writes the limited signal
	final, err := s.spool()
	if err != nil {
		return rep, err
	}
	cr, err = conditioned()
	if err != nil {
		return rep, err
	}
	info, err := s.limitPass(cr, detSp, maxDet, gainFor(eff), ceil, func(l, r []float64) error { return PutFrames(final, l, r) })
	if err != nil {
		return rep, err
	}
	rep.MaxReductionDB, rep.EngagedFraction = info.MaxReductionDB, info.EngagedFraction
	finalSrc := SpoolSource(s.rate, s.n, final)
	// the true peak of the result, and one re-limit when it is above the ceiling
	tpOf := func(src Source, withLoudness bool) (tp float64, lufs float64, lok bool, err error) {
		rd, err := src.Open()
		if err != nil {
			return 0, 0, false, err
		}
		var tk *kMeter
		if withLoudness {
			tk = newKMeter(s.rate, s.n)
		}
		ps := newPeakScan(s.n)
		var mx float64
		emit := func(det []float64) error {
			for _, v := range det {
				if v > mx {
					mx = v
				}
			}
			return nil
		}
		if err := s.each(rd, func(l, r []float64) error {
			if tk != nil {
				tk.add(l, r)
			}
			return ps.feed(l, r, false, emit)
		}); err != nil {
			return 0, 0, false, err
		}
		if err := ps.feed(nil, nil, true, emit); err != nil {
			return 0, 0, false, err
		}
		tp = tpFloor
		if mx > 0 {
			tp = float64(2 * db10(mx))
		}
		if tk != nil {
			lufs, lok = tk.lufs()
		}
		return tp, lufs, lok, nil
	}
	tp, lufs, lok, err := tpOf(finalSrc, true)
	if err != nil {
		return rep, err
	}
	if tp > ceil+slack {
		det2, err := s.spool()
		if err != nil {
			return rep, err
		}
		rd, err := finalSrc.Open()
		if err != nil {
			return rep, err
		}
		mx2, err := s.detect(rd, det2)
		if err != nil {
			return rep, err
		}
		final2, err := s.spool()
		if err != nil {
			return rep, err
		}
		rd, err = finalSrc.Open()
		if err != nil {
			return rep, err
		}
		if _, err := s.limitPass(rd, det2, mx2, 1, ceil-(tp-ceil)-0.02, func(l, r []float64) error { return PutFrames(final2, l, r) }); err != nil {
			return rep, err
		}
		finalSrc = SpoolSource(s.rate, s.n, final2)
		if tp, lufs, lok, err = tpOf(finalSrc, true); err != nil {
			return rep, err
		}
		if tp > ceil+slack {
			return rep, ErrCeiling
		}
	}
	rep.OutputTruePeakDBTP = tp
	if !lok {
		return rep, ErrSilent
	}
	rep.OutputLUFS = lufs
	rep.ShortfallLU = eff - lufs
	rd, err = finalSrc.Open()
	if err != nil {
		return rep, err
	}
	if err := s.each(rd, sink); err != nil {
		return rep, err
	}
	return rep, nil
}

// StreamTruePeakDBTP is TruePeakDBTP of a source that is read block by block.
func StreamTruePeakDBTP(ctx context.Context, src Source) (float64, error) {
	s := &streamer{ctx: ctx, n: src.Frames(), rate: src.Rate()}
	rd, err := src.Open()
	if err != nil {
		return 0, err
	}
	mx, err := s.detect(rd, nil)
	if err != nil {
		return 0, err
	}
	if mx <= 0 {
		return tpFloor, nil
	}
	return float64(2 * db10(mx)), nil
}
