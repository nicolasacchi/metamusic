package safety

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// memScratch keeps spools in memory and counts them, so a test sees a spool that was never released.
type memScratch struct{ made, closed int }

type memSpool struct {
	sc  *memScratch
	buf bytes.Buffer
}

func (m *memScratch) NewSpool() (Spool, error)  { m.made++; return &memSpool{sc: m}, nil }
func (s *memSpool) Write(p []byte) (int, error) { return s.buf.Write(p) }
func (s *memSpool) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.buf.Bytes())), nil
}
func (s *memSpool) Close() error { s.sc.closed++; s.buf.Reset(); return nil }

// streamed runs FinishStream over b and collects the finished samples.
func streamed(t *testing.T, b pcm.Buffer, in FinishIn) (pcm.Buffer, FinishReport, error) {
	t.Helper()
	sc := &memScratch{}
	out := pcm.Buffer{Rate: b.Rate}
	rep, err := FinishStream(context.Background(), BufferSource(b), in, sc, func(l, r []float64) error {
		out.Ch[0], out.Ch[1] = append(out.Ch[0], l...), append(out.Ch[1], r...)
		return nil
	})
	if sc.made != sc.closed {
		t.Errorf("%d spools made, %d released", sc.made, sc.closed)
	}
	return out, rep, err
}

// requireSame asserts that FinishStream and Finish agree on the report, the error and every sample bit.
func requireSame(t *testing.T, name string, b pcm.Buffer, in FinishIn) {
	t.Helper()
	want := b.Clone()
	wantRep, wantErr := Finish(&want, in)
	got, gotRep, gotErr := streamed(t, b, in)
	if !errors.Is(gotErr, wantErr) && !(gotErr == nil && wantErr == nil) {
		t.Errorf("%s: error %v, Finish says %v", name, gotErr, wantErr)
		return
	}
	if gotRep != wantRep {
		t.Errorf("%s: report\n got %+v\nwant %+v", name, gotRep, wantRep)
	}
	if wantErr != nil {
		return
	}
	if got.Frames() != want.Frames() {
		t.Errorf("%s: %d frames, want %d", name, got.Frames(), want.Frames())
		return
	}
	for c := range 2 {
		for i, v := range want.Ch[c] {
			if math.Float64bits(got.Ch[c][i]) != math.Float64bits(v) {
				t.Errorf("%s: channel %d sample %d is %v, Finish gives %v", name, c, i, got.Ch[c][i], v)
				return
			}
		}
	}
}

func withBlock(t *testing.T, n int) {
	t.Helper()
	old := streamBlock
	streamBlock = n
	t.Cleanup(func() { streamBlock = old })
}

// parityCases are the signals of the parity test: not limited, limited with the target lowered, a DC offset, tiny buffers, fades
// of the operator's own, other ceilings, and the failures.
func parityCases() map[string]struct {
	b  pcm.Buffer
	in FinishIn
} {
	type c = struct {
		b  pcm.Buffer
		in FinishIn
	}
	clicks := clickNoise()
	short := dsptest.Stereo(testRate, clicks.Ch[0][:testRate*3], clicks.Ch[1][:testRate*3])
	dc := dsptest.Dual(testRate, dsptest.Offset(dsptest.Sine(testRate, 440, 0.2, 4), 0.01))
	oneSided := dsptest.Stereo(testRate, dsptest.Offset(dsptest.Pink(5, testRate*3), 0.02), dsptest.Pink(6, testRate*3))
	nan := pinkAt(3, 2, -20)
	nan.Ch[1][100] = math.NaN()
	hot := pinkAt(4, 5, -8)
	return map[string]c{
		"quiet to target":     {pinkAt(1, 8, -40), FinishIn{TargetLUFS: -16, RefLUFS: -18.5}},
		"default target":      {pinkAt(2, 6, -30), FinishIn{}},
		"limited and lowered": {clicks, FinishIn{TargetLUFS: -16}},
		"limited, short":      {short, FinishIn{TargetLUFS: -14, CeilingDBTP: -2}},
		"ceiling -3":          {short, FinishIn{TargetLUFS: -14, CeilingDBTP: -3}},
		"ceiling above -1":    {clicks, FinishIn{TargetLUFS: -14, CeilingDBTP: 0}},
		"dc":                  {dc, FinishIn{TargetLUFS: -20}},
		"dc on one channel":   {oneSided, FinishIn{TargetLUFS: -18}},
		"small offset":        {dsptest.Dual(testRate, dsptest.Offset(dsptest.Sine(testRate, 440, 0.2, 2), 5e-5)), FinishIn{TargetLUFS: -20}},
		"own fades":           {pinkAt(7, 3, -25), FinishIn{TargetLUFS: -16, OwnFades: true}},
		"hot input":           {hot, FinishIn{TargetLUFS: -12}},
		"tiny (0.1 s)":        {dsptest.Dual(testRate, dsptest.Sine(testRate, 440, 0.3, 0.1)), FinishIn{TargetLUFS: -20}},
		"shorter than a fade": {dsptest.Dual(testRate, dsptest.Sine(testRate, 440, 0.3, 0.012)), FinishIn{TargetLUFS: -20}},
		"a handful of frames": {dsptest.Dual(testRate, []float64{0.1, -0.2, 0.3, -0.1, 0.2, 0.05, -0.3}), FinishIn{TargetLUFS: -20}},
		"one frame":           {dsptest.Dual(testRate, []float64{0.5}), FinishIn{TargetLUFS: -20}},
		"quiet, not silent":   {dsptest.Dual(testRate, dsptest.Sine(testRate, 440, dsptest.DBToLin(-60), 3)), FinishIn{TargetLUFS: -16}},
		"silent zeros":        {dsptest.Dual(testRate, dsptest.Silence(testRate*2)), FinishIn{TargetLUFS: -16}},
		"silent -90 dBFS":     {dsptest.Dual(testRate, dsptest.Sine(testRate, 440, dsptest.DBToLin(-90), 2)), FinishIn{TargetLUFS: -16}},
		"empty":               {pcm.Buffer{Rate: testRate}, FinishIn{TargetLUFS: -16}},
		"nan":                 {nan, FinishIn{TargetLUFS: -16}},
	}
}

// TestFinishStream_MatchesFinish: the streamed finish is Finish, bit for bit, at the default block size and at block sizes that put
// the detector's look-ahead, the limiter's look-ahead and the loudness steps across block boundaries.
func TestFinishStream_MatchesFinish(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("heavy DSP test: skipped with -short under the race detector")
	}
	for _, block := range []int{StreamBlock, 4096, 1000, 997, 241, 7} {
		t.Run("", func(t *testing.T) {
			withBlock(t, block)
			for name, c := range parityCases() {
				if block < 241 && len(c.b.Ch[0]) > 20000 {
					continue // a 7-frame block over a 100 000 frame signal only repeats the point
				}
				requireSame(t, name, c.b, c.in)
			}
		})
	}
}

func TestFinishStream_Errors(t *testing.T) {
	b := pinkAt(1, 4, -30)
	// a cancelled context stops at the next block and releases its spools
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc := &memScratch{}
	if _, err := FinishStream(ctx, BufferSource(b), FinishIn{TargetLUFS: -16}, sc, func(l, r []float64) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	// a sink that fails ends the run with its error
	boom := errors.New("sink failed")
	sc = &memScratch{}
	if _, err := FinishStream(context.Background(), BufferSource(b), FinishIn{TargetLUFS: -16}, sc, func(l, r []float64) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("sink error: %v", err)
	}
	if sc.made != sc.closed {
		t.Errorf("%d spools made, %d released", sc.made, sc.closed)
	}
	// a scratch that cannot make a spool
	if _, err := FinishStream(context.Background(), BufferSource(b), FinishIn{TargetLUFS: -16}, failScratch{}, func(l, r []float64) error { return nil }); err == nil {
		t.Error("a failing scratch was accepted")
	}
	// a source that cannot be opened
	if _, err := FinishStream(context.Background(), badSource{}, FinishIn{TargetLUFS: -16}, &memScratch{}, func(l, r []float64) error { return nil }); err == nil {
		t.Error("an unreadable source was accepted")
	}
}

type failScratch struct{}

func (failScratch) NewSpool() (Spool, error) { return nil, errors.New("no space") }

type badSource struct{}

func (badSource) Frames() int                { return 10 }
func (badSource) Rate() int                  { return testRate }
func (badSource) Open() (FrameReader, error) { return nil, errors.New("cannot open") }

func TestSpoolSource_RoundTrip(t *testing.T) {
	sc := &memScratch{}
	sp, _ := sc.NewSpool()
	l, r := []float64{1, 2, 3, math.Pi}, []float64{-1, -2, -3, math.E}
	if err := PutFrames(sp, l, r); err != nil {
		t.Fatal(err)
	}
	src := SpoolSource(testRate, 4, sp)
	if src.Frames() != 4 || src.Rate() != testRate {
		t.Fatal("source shape")
	}
	rd, err := src.Open()
	if err != nil {
		t.Fatal(err)
	}
	gl, gr := make([]float64, 3), make([]float64, 3)
	n, err := rd.ReadFrames(gl, gr)
	if n != 3 || err != nil || gl[2] != 3 || gr[0] != -1 {
		t.Fatalf("first read %d %v %v %v", n, err, gl, gr)
	}
	n, err = rd.ReadFrames(gl, gr)
	if n != 1 || err != nil || gl[0] != math.Pi || gr[0] != math.E {
		t.Fatalf("second read %d %v %v %v", n, err, gl, gr)
	}
	if n, err = rd.ReadFrames(gl, gr); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("third read %d %v", n, err)
	}
	if err := rd.Close(); err != nil {
		t.Fatal(err)
	}
}

// finishWithSlack is Finish with the ceiling slack as a parameter: the body of finish.go, line for line (without the input checks
// the test signals never meet), so the re-limit path, which an honest render never reaches, can be compared with finishStream's.
func finishWithSlack(b *pcm.Buffer, in FinishIn, slack float64) (FinishReport, error) {
	rep := FinishReport{TargetLUFSRequested: in.TargetLUFS, RefLUFS: in.RefLUFS, CeilingDBTP: in.Ceiling()}
	target := in.TargetLUFS
	if target == 0 {
		target = DefaultTargetLUFS
		rep.TargetLUFSRequested = target
	}
	removeDC(b, &rep)
	if !in.OwnFades {
		fade(b)
	}
	l0, ok := LUFS(*b)
	if !ok {
		return rep, ErrSilent
	}
	rep.InputLUFS = l0
	ceil := rep.CeilingDBTP
	det := framePeaks(*b)
	gainFor := func(t float64) float64 { return dsp.Pow(10, (t-l0)/20) }
	trial := pcm.Buffer{Rate: b.Rate, Ch: [2][]float64{make([]float64, b.Frames()), make([]float64, b.Frames())}}
	shortfall := func(t float64) float64 {
		limitInto(&trial, *b, det, gainFor(t), ceil)
		l, ok := LUFS(trial)
		if !ok {
			return math.Inf(1)
		}
		return t - l
	}
	eff := target
	sf0 := shortfall(target)
	if sf0 > MaxLimitingLU {
		rep.Limited = true
		hi := target
		lo := math.Min(l0+ceil-float64(2*db10(maxOf(det))), hi-0.01)
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
	rep.TargetLUFSEffective = eff
	rep.GainDB = eff - l0
	info := limitInto(b, *b, det, gainFor(eff), ceil)
	rep.MaxReductionDB, rep.EngagedFraction = info.MaxReductionDB, info.EngagedFraction
	tp := TruePeakDBTP(*b)
	if tp > ceil+slack {
		limitInto(b, *b, framePeaks(*b), 1, ceil-(tp-ceil)-0.02)
		tp = TruePeakDBTP(*b)
		if tp > ceil+slack {
			return rep, ErrCeiling
		}
	}
	rep.OutputTruePeakDBTP = tp
	if l, ok := LUFS(*b); ok {
		rep.OutputLUFS = l
		rep.ShortfallLU = eff - l
	} else {
		return rep, ErrSilent
	}
	return rep, nil
}

// TestFinishStream_ReLimit forces the ceiling alarm's re-limit path with a slack the first limiter run just misses, and the
// ErrCeiling path with a slack nothing meets, and requires the same report, error and samples as finish.go's lines.
func TestFinishStream_ReLimit(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("heavy DSP test: skipped with -short under the race detector")
	}
	clicks := clickNoise()
	b := dsptest.Stereo(testRate, clicks.Ch[0][:testRate*3], clicks.Ch[1][:testRate*3])
	in := FinishIn{TargetLUFS: -14, CeilingDBTP: -2}
	probe := b.Clone()
	rep, err := finishWithSlack(&probe, in, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	ceil := rep.CeilingDBTP
	for name, slack := range map[string]float64{
		"re-limit succeeds": (rep.OutputTruePeakDBTP - ceil) - 0.001,
		"ErrCeiling":        -50,
	} {
		want := b.Clone()
		wantRep, wantErr := finishWithSlack(&want, in, slack)
		sc := &memScratch{}
		got := pcm.Buffer{Rate: b.Rate}
		gotRep, gotErr := finishStream(context.Background(), BufferSource(b), in, sc, func(l, r []float64) error {
			got.Ch[0], got.Ch[1] = append(got.Ch[0], l...), append(got.Ch[1], r...)
			return nil
		}, slack)
		if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && !errors.Is(gotErr, wantErr)) {
			t.Fatalf("%s: stream error %v, reference error %v (slack %.4f, first tp %.4f, ceil %.4f)", name, gotErr, wantErr, slack, rep.OutputTruePeakDBTP, ceil)
		}
		if gotRep != wantRep {
			t.Errorf("%s: report\n got %+v\nwant %+v", name, gotRep, wantRep)
		}
		if (name == "ErrCeiling") != errors.Is(gotErr, ErrCeiling) {
			t.Errorf("%s: error %v", name, gotErr)
		}
		if wantErr == nil {
			if wantRep.OutputTruePeakDBTP >= rep.OutputTruePeakDBTP-0.01 {
				t.Errorf("%s: the re-limit did not run (true peak %.4f, first run %.4f)", name, wantRep.OutputTruePeakDBTP, rep.OutputTruePeakDBTP)
			}
			for c := range 2 {
				for i, v := range want.Ch[c] {
					if math.Float64bits(got.Ch[c][i]) != math.Float64bits(v) {
						t.Fatalf("%s: channel %d sample %d differs", name, c, i)
					}
				}
			}
		}
		if sc.made != sc.closed {
			t.Errorf("%s: %d spools made, %d released", name, sc.made, sc.closed)
		}
	}
}

func TestStreamTruePeak_MatchesTruePeak(t *testing.T) {
	for name, b := range map[string]pcm.Buffer{"pink": pinkAt(3, 3, -20), "clicks": clickNoise(), "silence": dsptest.Dual(testRate, dsptest.Silence(1000))} {
		got, err := StreamTruePeakDBTP(context.Background(), BufferSource(b))
		if err != nil || got != TruePeakDBTP(b) {
			t.Errorf("%s: %v (%v), want %v", name, got, err, TruePeakDBTP(b))
		}
	}
	if _, err := StreamTruePeakDBTP(context.Background(), badSource{}); err == nil {
		t.Error("an unreadable source was accepted")
	}
}
