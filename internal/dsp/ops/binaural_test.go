package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/cmplx"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/filt"
	"github.com/nicolasacchi/metamusic/internal/dsp/hrtf"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// binReport is the part of report.json the binaural tests read.
type binReport struct {
	Safety struct {
		TargetEff    float64 `json:"target_lufs_effective"`
		LUFSOut      float64 `json:"lufs_out"`
		TruePeakDBTP float64 `json:"true_peak_dbtp"`
	} `json:"safety"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
	Hifi struct {
		Binaural struct {
			Model   string                    `json:"model"`
			AzUsed  struct{ Left, Right int } `json:"az_used"`
			ElUsed  int                       `json:"el_used"`
			HRTF    *hrtf.Info                `json:"hrtf"`
			Room    string                    `json:"room"`
			Tail    int                       `json:"tail_samples"`
			Balance float64                   `json:"ear_balance_db"`
			BalIn   float64                   `json:"ear_balance_in_db"`
			From    *int                      `json:"hrir_resampled_from"`
		} `json:"binaural"`
	} `json:"hifi"`
}

func (r binReport) has(code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func runBin(t *testing.T, b pcm.Buffer, params string) (RunOut, binReport) {
	t.Helper()
	out, err := runBinaural(context.Background(), RunIn{Rate: b.Rate, Inputs: []pcm.Buffer{b}, Params: json.RawMessage(params), Seed: 5, TargetLUFS: -16, RefLUFS: -24})
	if err != nil {
		t.Fatal(err)
	}
	var rep binReport
	if err := json.Unmarshal(out.Report, &rep); err != nil {
		t.Fatal(err)
	}
	return out, rep
}

func binParams(t *testing.T, params string) BinauralParams {
	t.Helper()
	p, err := ParseBinaural(json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// dry renders without the room or the safety stage.
func dry(t *testing.T, b pcm.Buffer, params string) pcm.Buffer {
	t.Helper()
	out, _, err := Binaural(context.Background(), b, binParams(t, params), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// click is a unit impulse at sample 2000 on one channel (right when right is true) and silence on the other.
func click(rate, n int, right bool) pcm.Buffer {
	x, z := make([]float64, n), make([]float64, n)
	x[2000] = 1
	if right {
		return dsptest.Stereo(rate, z, x)
	}
	return dsptest.Stereo(rate, x, z)
}

// lagIACC is the delay of b behind a in samples: the maximum of their cross-correlation after a 1.5 kHz low-pass, refined by a
// parabola.
func lagIACC(a, b []float64, rate int) float64 {
	lp := filt.LowPass(rate, 1500, filt.Butterworth)
	x, _ := lp.Process(a, nil)
	y, _ := lp.Process(b, nil)
	corr := func(lag int) float64 {
		var s float64
		for i := range x {
			if j := i + lag; j >= 0 && j < len(y) {
				s += x[i] * y[j]
			}
		}
		return s
	}
	best, bv := -100, math.Inf(-1)
	for lag := -100; lag <= 100; lag++ {
		if v := corr(lag); v > bv {
			best, bv = lag, v
		}
	}
	l, r := corr(best-1), corr(best+1)
	return float64(best) + 0.5*(l-r)/(l-2*bv+r)
}

func peakIndex(x []float64) int {
	best := 0
	for i, v := range x {
		if math.Abs(v) > math.Abs(x[best]) {
			best = i
		}
	}
	return best
}

func TestBinaural_Registered(t *testing.T) {
	t.Parallel()
	run, units, ok := Lookup(KindBinaural)
	if !ok || run == nil || units == nil {
		t.Fatal("binaural is not registered")
	}
	if u := units(48000, 48000, 48000, nil); u <= SafetyUnits(48000) {
		t.Errorf("units %d do not exceed the safety stage", u)
	}
	if got := units(48000, 48000, 48000, json.RawMessage(`{"model":"bogus"}`)); got <= 0 {
		t.Errorf("a bad params object must still price the work: %d", got)
	}
}

func TestBinaural_Params(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, in, field, rule string
	}{
		{"model", `{"model":"x"}`, "model", RuleRange},
		{"preset", `{"preset":"x"}`, "preset", RulePreset},
		{"left low", `{"left_az_deg":-91}`, "left_az_deg", RuleRange},
		{"left high", `{"left_az_deg":-9}`, "left_az_deg", RuleRange},
		{"right low", `{"right_az_deg":9}`, "right_az_deg", RuleRange},
		{"right high", `{"right_az_deg":91}`, "right_az_deg", RuleRange},
		{"elevation", `{"elevation_deg":31}`, "elevation_deg", RuleRange},
		{"room", `{"room":"cave"}`, "room", RuleRange},
		{"mix low", `{"room_mix_db":-41}`, "room_mix_db", RuleRange},
		{"mix high", `{"room_mix_db":-11}`, "room_mix_db", RuleRange},
		{"tail", `{"tail_s":10.5}`, "tail_s", RuleRange},
		{"tail negative", `{"tail_s":-1}`, "tail_s", RuleRange},
		{"type", `{"tail_s":"a"}`, "params", "type"},
	} {
		_, err := ParseBinaural(json.RawMessage(c.in))
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Field != c.field || pe.Rule != c.rule {
			t.Errorf("%s: %v, want %s/%s", c.name, err, c.field, c.rule)
		}
	}
	p, err := ParseBinaural(nil)
	if err != nil || p != (BinauralParams{Model: "hrtf", Preset: "studio", LeftAz: -30, RightAz: 30, Room: "studio", RoomMixDB: -24, TailS: 1}) {
		t.Errorf("defaults: %+v, %v", p, err)
	}
	for _, w := range []struct {
		preset      string
		left, right int
	}{{"studio", -30, 30}, {"wide", -45, 45}, {"near", -20, 20}} { // near is +-22 degrees, snapped to the table by hrtf
		if p, _ := ParseBinaural(json.RawMessage(`{"preset":"` + w.preset + `"}`)); p.LeftAz != w.left || p.RightAz != w.right {
			t.Errorf("%s: %d/%d", w.preset, p.LeftAz, p.RightAz)
		}
	}
	if p, _ := ParseBinaural(json.RawMessage(`{"preset":"near","model":"parametric"}`)); p.LeftAz != -22 || p.RightAz != 22 {
		t.Errorf("parametric does not snap: %d/%d", p.LeftAz, p.RightAz)
	}
}

// TestBinaural_Snap: azimuths and elevation snap to the table for hrtf and the snapped values are what is recorded; the
// parametric model keeps the azimuth and ignores the elevation.
func TestBinaural_Snap(t *testing.T) {
	t.Parallel()
	p := binParams(t, `{"left_az_deg":-31,"right_az_deg":33,"elevation_deg":8}`)
	if p.LeftAz != -30 || p.RightAz != 35 || p.Elevation != 15 {
		t.Errorf("hrtf: %d/%d el %d, want -30/35 el 15", p.LeftAz, p.RightAz, p.Elevation)
	}
	if p := binParams(t, `{"elevation_deg":7}`); p.Elevation != 0 {
		t.Errorf("elevation 7 snapped to %d", p.Elevation)
	}
	q := binParams(t, `{"model":"parametric","left_az_deg":-31,"right_az_deg":33,"elevation_deg":20}`)
	if q.LeftAz != -31 || q.RightAz != 33 || q.Elevation != 0 {
		t.Errorf("parametric: %d/%d el %d", q.LeftAz, q.RightAz, q.Elevation)
	}
	_, rep := runBin(t, dsptest.PinkStereo(16000, 3, 0.5, 0.5), `{"left_az_deg":-31,"right_az_deg":33,"elevation_deg":8,"room":"none"}`)
	if b := rep.Hifi.Binaural; b.AzUsed.Left != -30 || b.AzUsed.Right != 35 || b.ElUsed != 15 {
		t.Errorf("report az/el %+v", b)
	}
}

// TestBinaural_ITD: a click on the right speaker only, at +90 degrees: the left ear hears it later than the right (hrtf: the
// interaural cross-correlation lag of this dummy head; parametric: the Woodworth value within one sample).
func TestBinaural_ITD(t *testing.T) {
	t.Parallel()
	const rate = 48000
	out := dry(t, click(rate, 4096, true), `{"right_az_deg":90,"room":"none"}`)
	if got := lagIACC(out.Ch[1], out.Ch[0], rate); got < 34 || got > 42 {
		t.Errorf("hrtf ITD = %.1f samples (%.2f ms), want 34-42", got, got/rate*1000)
	}
	for _, r := range []int{16000, 48000} {
		out = dry(t, click(r, 4096, true), `{"model":"parametric","right_az_deg":90,"room":"none"}`)
		want := 0.0875 / 343 * (math.Pi/2 + 1) * float64(r)
		if got := float64(peakIndex(out.Ch[0]) - peakIndex(out.Ch[1])); math.Abs(got-want) > 1 {
			t.Errorf("parametric ITD at %d Hz = %.1f samples, Woodworth %.2f", r, got, want)
		}
	}
}

// TestBinaural_ILD: a 4 kHz tone on the right speaker only at +90 degrees: the left ear is at least 8 dB below the right
// (hrtf); the parametric model follows the Brown-Duda curve within 1.5 dB.
func TestBinaural_ILD(t *testing.T) {
	t.Parallel()
	const rate = 48000
	x := dsptest.Sine(rate, 4000, 0.3, 0.5)
	b := dsptest.Stereo(rate, dsptest.Silence(len(x)), x)
	level := func(out pcm.Buffer) (l, r float64) {
		seg := func(c int) []float64 { return out.Ch[c][len(x)/2 : len(x)] }
		return dsptest.RMS(seg(0)), dsptest.RMS(seg(1))
	}
	l, r := level(dry(t, b, `{"right_az_deg":90,"room":"none"}`))
	if d := dsptest.DBFS(r) - dsptest.DBFS(l); d < 8 {
		t.Errorf("hrtf interaural level difference %.1f dB, want at least 8", d)
	}
	l, r = level(dry(t, b, `{"model":"parametric","right_az_deg":90,"room":"none"}`))
	w0 := 343 / 0.0875
	mag := func(theta float64) float64 {
		alpha := 1.05 + 0.95*math.Cos(theta*math.Pi/150)
		s := complex(0, 2*math.Pi*4000)
		return cmplx.Abs((1 + complex(alpha, 0)*s/complex(2*w0, 0)) / (1 + s/complex(2*w0, 0)))
	}
	want := 20 * math.Log10(mag(0)/mag(180))
	if d := dsptest.DBFS(r) - dsptest.DBFS(l); math.Abs(d-want) > 1.5 {
		t.Errorf("parametric interaural level difference %.1f dB, Brown-Duda %.1f dB", d, want)
	}
}

// TestBinaural_Symmetry: swapping the input channels swaps the output channels exactly when the speakers are mirrored.
func TestBinaural_Symmetry(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 11, 0.5, 0.2)
	sw := dsptest.Stereo(16000, b.Ch[1], b.Ch[0])
	for _, params := range []string{`{"room":"none"}`, `{"preset":"wide","room":"none"}`, `{"model":"parametric","left_az_deg":-40,"right_az_deg":40,"room":"none"}`} {
		a, c := dry(t, b, params), dry(t, sw, params)
		for ear := range 2 {
			for i := range a.Ch[ear] {
				if a.Ch[ear][i] != c.Ch[1-ear][i] {
					t.Fatalf("%s: ear %d sample %d differs", params, ear, i)
				}
			}
		}
	}
}

// TestBinaural_CentreImage: a dual-mono input gives two ear signals that stay correlated (a stable phantom centre).
func TestBinaural_CentreImage(t *testing.T) {
	t.Parallel()
	b := dsptest.Dual(16000, dsptest.Pink(9, 16000))
	for _, params := range []string{`{"room":"none"}`, `{"model":"parametric","room":"none"}`} {
		out := dry(t, b, params)
		if rho := dsptest.Pearson(out.Ch[0], out.Ch[1]); rho < 0.7 {
			t.Errorf("%s: ear correlation %.2f, want at least 0.7", params, rho)
		}
	}
}

func TestBinaural_Length(t *testing.T) {
	t.Parallel()
	const rate, n = 16000, 8000
	b := dsptest.PinkStereo(rate, 4, 0.5, 0.3)
	for _, c := range []struct {
		params string
		tail   func(effLen int) int
	}{
		{`{"room":"none"}`, func(int) int { return 0 }},
		{`{"room":"studio","tail_s":0}`, func(int) int { return 0 }},
		{`{"room":"studio","tail_s":0.1}`, func(int) int { return 1600 }},
		{`{"room":"studio","tail_s":10}`, func(eff int) int { return eff }},
		{`{"room":"hall","tail_s":0.5}`, func(int) int { return 8000 }},
		{`{"model":"parametric","room":"hall","tail_s":1}`, func(int) int { return 16000 }},
	} {
		out, rep := runBin(t, b, c.params)
		p := binParams(t, c.params)
		if got, want := out.Buffer.Frames(), BinauralLen(n, p, rate, 0)-TailSamples(p, rate, 0)+rep.Hifi.Binaural.Tail; got != want {
			t.Errorf("%s: %d frames, want %d", c.params, got, want)
		}
		if want := c.tail(rep.Hifi.Binaural.Tail); rep.Hifi.Binaural.Tail != want || out.Buffer.Frames() != n+want {
			t.Errorf("%s: tail %d, frames %d, want tail %d", c.params, rep.Hifi.Binaural.Tail, out.Buffer.Frames(), want)
		}
	}
	// the tail never exceeds the upper bound the estimate uses
	p := binParams(t, `{"room":"hall","tail_s":10}`)
	if _, rep := runBin(t, b, `{"room":"hall","tail_s":10}`); rep.Hifi.Binaural.Tail > TailSamples(p, rate, 0) {
		t.Errorf("tail %d exceeds the bound %d", rep.Hifi.Binaural.Tail, TailSamples(p, rate, 0))
	}
}

// TestBinaural_RoomOff_Dry: with no room the result is the direct sum of the four convolutions, nothing else.
func TestBinaural_RoomOff_Dry(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(48000, 21, 0.1, 0.4)
	p := binParams(t, `{"room":"none"}`)
	out := dry(t, b, `{"room":"none"}`)
	if out.Frames() != b.Frames() {
		t.Fatalf("%d frames, want %d", out.Frames(), b.Frames())
	}
	tb := hrtf.Default()
	for ear, e := range [2]hrtf.Ear{hrtf.Left, hrtf.Right} {
		hl, hr := tb.IR(p.LeftAz, 0, e), tb.IR(p.RightAz, 0, e)
		for i := range out.Ch[ear] {
			var want float64
			for k := range hl {
				if i-k >= 0 {
					want += b.Ch[0][i-k]*hl[k] + b.Ch[1][i-k]*hr[k]
				}
			}
			if math.Abs(out.Ch[ear][i]-want) > 1e-9 {
				t.Fatalf("ear %d sample %d: %v, direct sum %v", ear, i, out.Ch[ear][i], want)
			}
		}
	}
}

// TestBinaural_RoomMix: the room is added at room_mix_db (the wet part scales by the dB value).
func TestBinaural_RoomMix(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 8, 0.5, 0.3)
	base := dry(t, b, `{"room":"none"}`)
	wet := func(db string) float64 {
		o := dry(t, b, `{"room":"studio","tail_s":1,"room_mix_db":`+db+`}`)
		var e float64
		for c := range 2 {
			for i := range o.Ch[c] {
				d := o.Ch[c][i]
				if i < len(base.Ch[c]) {
					d -= base.Ch[c][i]
				}
				e += d * d
			}
		}
		return 10 * math.Log10(e)
	}
	if d := wet("-12") - wet("-24"); math.Abs(d-12) > 0.3 {
		t.Errorf("room_mix_db -12 vs -24 changed the wet energy by %.2f dB, want 12", d)
	}
}

// TestBinaural_Resampled44k1: the table is resampled once and the interaural time difference is unchanged to within a sample.
func TestBinaural_Resampled44k1(t *testing.T) {
	t.Parallel()
	itd := func(rate int) (float64, binReport) {
		out := dry(t, click(rate, 4096, true), `{"right_az_deg":90,"room":"none"}`)
		_, rep := runBin(t, dsptest.PinkStereo(rate, 2, 0.3, 0.5), `{"room":"none"}`)
		return lagIACC(out.Ch[1], out.Ch[0], rate) * 48000 / float64(rate), rep
	}
	a, repA := itd(48000)
	b, repB := itd(44100)
	if math.Abs(a-b) > 1 {
		t.Errorf("ITD %.2f samples at 48 kHz, %.2f (in 48 kHz units) at 44.1 kHz", a, b)
	}
	if repA.Hifi.Binaural.From != nil {
		t.Error("48 kHz reports a resample")
	}
	if repB.Hifi.Binaural.From == nil || *repB.Hifi.Binaural.From != 48000 {
		t.Errorf("44.1 kHz does not report hrir_resampled_from 48000: %v", repB.Hifi.Binaural.From)
	}
}

func TestBinaural_Report(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 6, 1, 0.4)
	l, _ := safety.LUFS(b)
	g := dsptest.DBToLin(-24 - l)
	for c := range 2 {
		b.Ch[c] = dsptest.Scale(b.Ch[c], g)
	}
	out, rep := runBin(t, b, `{}`)
	bn := rep.Hifi.Binaural
	if bn.Model != "hrtf" || bn.HRTF == nil || bn.HRTF.Dataset != "SADIE II" || bn.HRTF.Licence != "Apache-2.0" || len(bn.HRTF.SHA12) != 12 || bn.HRTF.Credit == "" {
		t.Errorf("hrtf block: %+v", bn)
	}
	for _, code := range []string{WarnBinauralHeadphonesOnly, WarnBinauralMonoIncompatible} {
		if !rep.has(code) {
			t.Errorf("hrtf render lacks warning %s", code)
		}
	}
	if rep.has(WarnBinauralParametricBasic) || rep.has(WarnElevationIgnored) {
		t.Error("hrtf render carries a parametric warning")
	}
	if math.Abs(rep.Safety.LUFSOut-rep.Safety.TargetEff) > 0.5 || rep.Safety.TruePeakDBTP > -0.95 {
		t.Errorf("safety: %+v", rep.Safety)
	}
	if math.Abs(rep.Safety.TargetEff+16) > 1e-9 {
		t.Errorf("effective target %v, want -16 (the job's)", rep.Safety.TargetEff)
	}
	if out.Buffer.Frames() <= b.Frames() {
		t.Error("the studio room adds a tail")
	}
	_, rep = runBin(t, b, `{"model":"parametric","elevation_deg":15,"room":"none"}`)
	pb := rep.Hifi.Binaural
	if pb.HRTF != nil || pb.ElUsed != 0 || !rep.has(WarnBinauralParametricBasic) || !rep.has(WarnElevationIgnored) || !rep.has(WarnBinauralHeadphonesOnly) {
		t.Errorf("parametric report: %+v warnings %v", pb, rep.Warnings)
	}
	_, rep = runBin(t, b, `{"model":"parametric","room":"none"}`)
	if rep.has(WarnElevationIgnored) {
		t.Error("elevation_ignored without an elevation")
	}
}

// TestBinaural_EarBalanceReported: the report carries the ear balance of the result against the parent's, and warns only when
// a balanced parent comes out lopsided.
func TestBinaural_EarBalanceReported(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 6, 1, 0.5)
	_, rep := runBin(t, b, `{"room":"none"}`)
	if bn := rep.Hifi.Binaural; math.Abs(bn.BalIn) > 1 || math.Abs(bn.Balance) > 1.5 || rep.has(WarnBinauralEarImbalance) {
		t.Errorf("symmetric speakers: parent %.2f dB, change %.2f dB, warnings %v", bn.BalIn, bn.Balance, rep.Warnings)
	}
	// a louder left channel in the parent is carried through, not blamed on the render
	loud := dsptest.Stereo(16000, dsptest.Scale(b.Ch[0], 2), b.Ch[1])
	_, rep = runBin(t, loud, `{"room":"none"}`)
	if bn := rep.Hifi.Binaural; bn.BalIn < 5 || rep.has(WarnBinauralEarImbalance) {
		t.Errorf("lopsided parent: balance %.2f dB, warnings %v", bn.BalIn, rep.Warnings)
	}
	// one speaker straight to the side and one almost in front, fed a balanced bright parent: the render itself favours one ear
	white := dsptest.Stereo(16000, dsptest.Noise(1, 16000, 0.1), dsptest.Noise(2, 16000, 0.1))
	_, rep = runBin(t, white, `{"model":"parametric","left_az_deg":-10,"right_az_deg":90,"room":"none"}`)
	if bn := rep.Hifi.Binaural; math.Abs(bn.Balance) <= 3 || !rep.has(WarnBinauralEarImbalance) {
		t.Errorf("lopsided render: change %.2f dB, warnings %v", bn.Balance, rep.Warnings)
	}
}

func TestBinaural_RoomIsDeterministicPerSeed(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 3, 0.4, 0.5)
	run := func(seed int64) pcm.Buffer {
		o, _, err := Binaural(context.Background(), b, binParams(t, `{"room":"studio"}`), seed, nil)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	a, c, d := run(1), run(1), run(2)
	same, diff := true, false
	for i := range a.Ch[0] {
		same = same && a.Ch[0][i] == c.Ch[0][i] && a.Ch[1][i] == c.Ch[1][i]
		diff = diff || a.Ch[0][i] != d.Ch[0][i]
	}
	if !same || !diff {
		t.Errorf("same seed equal = %v, other seed differs = %v", same, diff)
	}
}

func TestBinaural_Errors(t *testing.T) {
	t.Parallel()
	b := dsptest.PinkStereo(16000, 3, 0.3, 0.5)
	ctx := context.Background()
	if _, err := runBinaural(ctx, RunIn{Inputs: nil, Rate: 16000}); err == nil {
		t.Error("no input accepted")
	}
	if _, err := runBinaural(ctx, RunIn{Inputs: []pcm.Buffer{b}, Rate: 16000, Params: json.RawMessage(`{"preset":"x"}`)}); err == nil {
		t.Error("bad preset accepted")
	}
	silent := dsptest.Stereo(16000, dsptest.Silence(8000), dsptest.Silence(8000))
	if _, err := runBinaural(ctx, RunIn{Inputs: []pcm.Buffer{silent}, Rate: 16000}); !errors.Is(err, safety.ErrSilent) {
		t.Errorf("silent input: %v", err)
	}
	if _, _, err := Binaural(ctx, b, BinauralParams{Model: "x"}, 1, nil); err == nil {
		t.Error("Binaural accepted an unchecked model")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := Binaural(cctx, b, binParams(t, `{}`), 1, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
	if _, err := runBinaural(ctx, RunIn{Inputs: []pcm.Buffer{b}, Rate: 16000, TargetLUFS: -16, Meter: &dsp.Meter{Max: 1000}}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("tiny budget: %v", err)
	}
}

// TestBinaural_Metered: the units model is an upper bound of what the render adds to the meter, and not a loose one.
func TestBinaural_Metered(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		rate   int
		params string
	}{
		{48000, `{"room":"none"}`}, {16000, `{"room":"studio"}`}, {44100, `{"room":"hall","tail_s":2}`}, {16000, `{"model":"parametric","room":"hall"}`},
	} {
		b := dsptest.PinkStereo(c.rate, 12, 1, 0.3)
		m := &dsp.Meter{}
		if _, err := runBinaural(context.Background(), RunIn{Inputs: []pcm.Buffer{b}, Rate: c.rate, Params: json.RawMessage(c.params), TargetLUFS: -16, Meter: m}); err != nil {
			t.Fatal(err)
		}
		_, units, _ := Lookup(KindBinaural)
		model := units(int64(b.Frames()), 0, c.rate, json.RawMessage(c.params))
		if m.Used() > model || float64(m.Used()) < 0.5*float64(model) {
			t.Errorf("rate %d %s: metered %d, model %d", c.rate, c.params, m.Used(), model)
		}
	}
}

func TestBinaural_PresetTable(t *testing.T) {
	t.Parallel()
	want := []BinauralPreset{{"studio", -30, 30}, {"wide", -45, 45}, {"near", -22, 22}}
	if len(BinauralPresets) != len(want) {
		t.Fatalf("%d presets", len(BinauralPresets))
	}
	for i, w := range want {
		if BinauralPresets[i] != w {
			t.Errorf("preset %d = %+v, want %+v", i, BinauralPresets[i], w)
		}
	}
	if _, ok := BinauralPresetByName("x"); ok || strings.Contains(WarnBinauralHeadphonesOnly, " ") {
		t.Error("preset lookup")
	}
}
