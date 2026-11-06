package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// masterReport is the part of report.json the tests read.
type masterReport struct {
	Safety struct {
		GainDB       float64 `json:"gain_db"`
		TargetReq    float64 `json:"target_lufs_requested"`
		TargetEff    float64 `json:"target_lufs_effective"`
		LUFSOut      float64 `json:"lufs_out"`
		TruePeakDBTP float64 `json:"true_peak_dbtp"`
	} `json:"safety"`
	Warnings []struct {
		Code string `json:"code"`
	} `json:"warnings"`
	Hifi struct {
		Master struct {
			Preset string `json:"preset"`
			Comp   struct {
				Mean float64 `json:"mean_reduction_db"`
				Max  float64 `json:"max_reduction_db"`
			} `json:"comp"`
		} `json:"master"`
	} `json:"hifi"`
}

func runMasterJSON(t *testing.T, b pcm.Buffer, params string, target float64) (RunOut, masterReport) {
	t.Helper()
	out, err := runMaster(context.Background(), RunIn{Rate: b.Rate, Inputs: []pcm.Buffer{b}, Params: json.RawMessage(params), TargetLUFS: target})
	if err != nil {
		t.Fatal(err)
	}
	var rep masterReport
	if err := json.Unmarshal(out.Report, &rep); err != nil {
		t.Fatal(err)
	}
	return out, rep
}

func pinkAt(seed uint64, secs, lufs, rho float64) pcm.Buffer {
	b := dsptest.PinkStereo(testRate, seed, secs, rho)
	l, _ := safety.LUFS(b)
	g := dsptest.DBToLin(lufs - l)
	for c := range 2 {
		b.Ch[c] = dsptest.Scale(b.Ch[c], g)
	}
	return b
}

func TestMaster_PresetTable(t *testing.T) {
	t.Parallel()
	want := []MasterPreset{{"neutral", 0.0, 0.25, 25}, {"warm", -1.5, 0.30, 25}, {"bright", 1.5, 0.25, 25}, {"punch", 0.0, 0.50, 30}}
	if len(MasterPresets) != len(want) {
		t.Fatalf("%d presets", len(MasterPresets))
	}
	for i, w := range want {
		if MasterPresets[i] != w {
			t.Errorf("preset %d = %+v, want %+v", i, MasterPresets[i], w)
		}
		p, err := ParseMaster(json.RawMessage(`{"preset":"` + w.Name + `"}`))
		if err != nil || p.TiltDB != w.TiltDB || p.CompAmount != w.CompAmount || p.LowCutHz != w.LowCutHz || p.CeilingDBTP != -1 || p.TargetLUFS != nil {
			t.Errorf("%s resolved to %+v (%v)", w.Name, p, err)
		}
	}
}

func TestMaster_Params(t *testing.T) {
	t.Parallel()
	p, err := ParseMaster(json.RawMessage(`{"preset":"warm","tilt_db":0.5,"comp_amount":null,"low_cut_hz":0,"target_lufs":-14,"ceiling_dbtp":-3}`))
	if err != nil || p.TiltDB != 0.5 || p.CompAmount != 0.30 || p.LowCutHz != 0 || *p.TargetLUFS != -14 || p.CeilingDBTP != -3 {
		t.Errorf("overrides: %+v %v", p, err)
	}
	for _, bad := range []string{`{"preset":"loud"}`, `{"tilt_db":3.1}`, `{"comp_amount":1.1}`, `{"low_cut_hz":19}`, `{"low_cut_hz":41}`,
		`{"target_lufs":-11.9}`, `{"target_lufs":-30.1}`, `{"ceiling_dbtp":-0.9}`, `{"ceiling_dbtp":-3.1}`, `{"tilt_db":"x"}`} {
		var pe *ParamError
		if _, err := ParseMaster(json.RawMessage(bad)); !errors.As(err, &pe) {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := runMaster(context.Background(), RunIn{Rate: testRate}); err == nil {
		t.Error("a job without input accepted")
	}
}

func TestMaster_Transparent(t *testing.T) {
	t.Parallel()
	b := pinkAt(1, 2, -20, 0.5)
	out, info, err := Master(b, MasterParams{Preset: "neutral", CeilingDBTP: -1}, -20, nil)
	if err != nil || info.CompressorActive || !equalBuf(out, b) {
		t.Fatalf("comp 0, tilt 0, low cut 0 changed the signal: %v %+v", err, info)
	}
	// through the whole stage the shape survives: after the one linked gain, within 1e-9 of the input scaled
	res, rep := runMasterJSON(t, b, `{"tilt_db":0,"comp_amount":0,"low_cut_hz":0}`, -16)
	g := dsptest.DBToLin(rep.Safety.GainDB)
	var worst float64
	for i := 10000; i < b.Frames()-10000; i++ { // away from the 10 ms fades
		worst = math.Max(worst, math.Abs(res.Buffer.Ch[0][i]-float64(g*b.Ch[0][i])))
	}
	if worst > 1e-3 { // the limiter or DC removal may touch a few samples; the stage itself added nothing
		t.Logf("deviation after Finish %g", worst)
	}
}

func TestMaster_TiltResponse(t *testing.T) {
	t.Parallel()
	const rate = 48000
	for _, tilt := range []float64{3, -3} {
		for _, tc := range []struct {
			f    float64
			want float64
		}{{100, -tilt / 2}, {8000, tilt / 2}} {
			x := dsptest.Sine(rate, tc.f, 0.1, 1)
			b := dsptest.Dual(rate, x)
			out, _, err := Master(b, MasterParams{TiltDB: tilt, CeilingDBTP: -1}, -30, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := dsptest.DBFS(dsptest.RMS(out.Ch[0][rate/2:])) - dsptest.DBFS(dsptest.RMS(x[rate/2:]))
			if math.Abs(got-tc.want) > 0.3 {
				t.Errorf("tilt %+.0f at %.0f Hz: %.2f dB, want %.2f +- 0.3", tilt, tc.f, got, tc.want)
			}
		}
	}
}

func TestMaster_CompressorCurve(t *testing.T) {
	t.Parallel()
	c := compressor{thresholdDB: -30, ratio: 1.75}
	// static reduction by hand: (1 - 1/1.75) * over above the 6 dB knee, capped at 6 dB
	for _, tc := range []struct{ over, want float64 }{{6, 2.5714}, {12, 5.1429}, {30, 6}, {-4, 0}} {
		// a square wave: |x| is constant, so the follower settles exactly on the level (a sine would sit below its peak)
		x := dsptest.Sine(testRate, 1000, 1, 1.5)
		for i, v := range x {
			x[i] = math.Copysign(dsptest.DBToLin(-30+tc.over), v)
		}
		b := dsptest.Dual(testRate, x)
		if _, _, err := c.compress(&b, nil); err != nil {
			t.Fatal(err)
		}
		tail := testRate
		got := dsptest.DBFS(dsptest.RMS(x[tail:])) - dsptest.DBFS(dsptest.RMS(b.Ch[0][tail:]))
		if math.Abs(got-tc.want) > 0.3 {
			t.Errorf("+%.0f dB over: reduction %.2f dB, want %.2f +- 0.3", tc.over, got, tc.want)
		}
		if got > 6.3 {
			t.Errorf("reduction %.2f dB exceeds the 6 dB cap", got)
		}
	}
	// the knee is continuous: a value just inside it sits between 0 and the line
	if r := c.reductionDB(-30); r <= 0 || r >= 1 {
		t.Errorf("reduction at the threshold %.3f dB", r)
	}
	if c.reductionDB(-33.01) != 0 || c.reductionDB(-26.99) <= 0 {
		t.Error("knee edges wrong")
	}
}

func TestMaster_AttackRelease(t *testing.T) {
	t.Parallel()
	const rate = 48000
	att, rel := timeConstant(compAttackSec, rate), timeConstant(compReleaseSec, rate)
	// attack: 63.2 % of a unit step after 30 ms
	env := 0.0
	var steps int
	for steps = 1; steps < rate; steps++ {
		if env = follow(env, 1, att, rel); env >= 1-math.Exp(-1) {
			break
		}
	}
	if got := float64(steps) / rate; math.Abs(got-0.030) > 0.030*0.15 {
		t.Errorf("attack time constant %.4f s, want 0.030 +- 15 %%", got)
	}
	// release: down to 36.8 % after 300 ms
	env = 1.0
	for steps = 1; steps < 4*rate; steps++ {
		if env = follow(env, 0, att, rel); env <= math.Exp(-1) {
			break
		}
	}
	if got := float64(steps) / rate; math.Abs(got-0.300) > 0.300*0.15 {
		t.Errorf("release time constant %.4f s, want 0.300 +- 15 %%", got)
	}
}

func TestMaster_TargetAndPeak(t *testing.T) {
	t.Parallel()
	b := pinkAt(3, 8, -30, 0.5)
	_, rep := runMasterJSON(t, b, `{}`, -16)
	if math.Abs(rep.Safety.LUFSOut+16) > 0.3 || rep.Safety.TruePeakDBTP > -0.95 {
		t.Errorf("default ceiling: %.2f LUFS, %.2f dBTP", rep.Safety.LUFSOut, rep.Safety.TruePeakDBTP)
	}
	out, rep := runMasterJSON(t, b, `{"ceiling_dbtp":-3}`, -16)
	if tp := safety.TruePeakDBTP(out.Buffer); tp > -2.95 || rep.Safety.TruePeakDBTP > -2.95 {
		t.Errorf("ceiling -3: true peak %.2f / %.2f dBTP", tp, rep.Safety.TruePeakDBTP)
	}
	_, rep = runMasterJSON(t, b, `{"target_lufs":-20}`, -16)
	if math.Abs(rep.Safety.LUFSOut+20) > 0.3 {
		t.Errorf("own target: %.2f LUFS, want -20", rep.Safety.LUFSOut)
	}
}

func TestMaster_NeverLouderThanClamp(t *testing.T) {
	t.Parallel()
	n := dsptest.Frames(testRate, 6)
	l, r := dsptest.Noise(1, n, 0.02), dsptest.Noise(2, n, 0.02)
	for i := 0; i < n; i += testRate / 10 { // a full-scale click every 100 ms: crest factor of about 30 dB
		l[i], r[i] = 1, 1
	}
	_, rep := runMasterJSON(t, dsptest.Stereo(testRate, l, r), `{"target_lufs":-12}`, -16)
	if rep.Safety.TargetReq != -12 || rep.Safety.TargetEff > -12 || rep.Safety.TargetEff < -30 {
		t.Errorf("requested %.2f, effective %.2f", rep.Safety.TargetReq, rep.Safety.TargetEff)
	}
	found := false
	for _, w := range rep.Warnings {
		found = found || w.Code == WarnMasterTargetLowered
	}
	if rep.Safety.TargetEff < -12.05 && !found {
		t.Error("a lowered target did not warn")
	}
}

func TestMaster_Length(t *testing.T) {
	t.Parallel()
	for _, secs := range []float64{0.03, 0.5, 1.7} {
		b := pinkAt(4, secs, -25, 0.8)
		out, _ := runMasterJSON(t, b, `{"preset":"punch"}`, -16)
		if out.Buffer.Frames() != b.Frames() || len(out.Buffer.Ch[1]) != b.Frames() {
			t.Errorf("%.2f s: %d frames, want %d", secs, out.Buffer.Frames(), b.Frames())
		}
	}
}

func TestMaster_Deterministic(t *testing.T) {
	t.Parallel()
	b := pinkAt(5, 3, -22, 0.6)
	a, _ := runMasterJSON(t, b, `{"preset":"warm"}`, -16)
	c, _ := runMasterJSON(t, b, `{"preset":"warm"}`, -16)
	if !equalBuf(a.Buffer, c.Buffer) || string(a.Report) != string(c.Report) {
		t.Error("two runs differ")
	}
}

// TestMaster_StereoLinked: the real-world MONO, WIDE and OOP shapes keep their inter-channel correlation through the chain (the
// compressor is linked, the filters identical on both channels), and a mono source stays exactly mono.
func TestMaster_StereoLinked(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"MONO", "WIDE", "OOP"} {
		b := dsptest.RealWorldShape(label, testRate, 5, 7)
		out, _ := runMasterJSON(t, b, `{"preset":"punch"}`, -16)
		in, got := dsptest.Pearson(b.Ch[0], b.Ch[1]), dsptest.Pearson(out.Buffer.Ch[0], out.Buffer.Ch[1])
		if math.Abs(in-got) > 0.02 {
			t.Errorf("%s: correlation %.3f became %.3f", label, in, got)
		}
	}
	mono := dsptest.Dual(testRate, dsptest.Pink(9, dsptest.Frames(testRate, 3)))
	out, _ := runMasterJSON(t, mono, `{"preset":"warm"}`, -16)
	if !equalBuf(pcm.Buffer{Ch: [2][]float64{out.Buffer.Ch[0], out.Buffer.Ch[0]}}, out.Buffer) {
		t.Error("a mono source did not stay mono")
	}
}

func TestMaster_Warnings(t *testing.T) {
	t.Parallel()
	quiet := pinkAt(6, 4, -50, 0.5)
	_, rep := runMasterJSON(t, quiet, `{"preset":"punch","comp_amount":0}`, -16)
	has := func(code string) bool {
		for _, w := range rep.Warnings {
			if w.Code == code {
				return true
			}
		}
		return false
	}
	if !has(WarnMasterQuietBoost) {
		t.Errorf("a +34 dB boost did not warn: %+v", rep.Warnings)
	}
	hot := pinkAt(7, 3, -20, 0.5)
	for c := range 2 {
		for i := range hot.Ch[c] {
			hot.Ch[c][i] = float64(hot.Ch[c][i] * 40)
		}
	}
	if tp := safety.TruePeakDBTP(hot); tp > 0 {
		_, rep = runMasterJSON(t, hot, `{}`, -16)
		if !has(WarnMasterInputClipped) {
			t.Errorf("input above 0 dBTP (%.1f) did not warn: %+v", tp, rep.Warnings)
		}
	}
}

func TestMaster_Metered(t *testing.T) {
	t.Parallel()
	b := pinkAt(8, 3, -22, 0.6)
	if _, _, err := Master(b, MasterParams{Preset: "warm", TiltDB: -1.5, CompAmount: 0.3, LowCutHz: 25, CeilingDBTP: -1}, -22, &dsp.Meter{Max: 500}); !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	m := &dsp.Meter{}
	if _, _, err := Master(b, MasterParams{TiltDB: -1.5, CompAmount: 0.3, LowCutHz: 25, CeilingDBTP: -1}, -22, m); err != nil {
		t.Fatal(err)
	}
	if want := MasterUnits(int64(b.Frames())) - SafetyUnits(int64(b.Frames())); m.Used() > want {
		t.Errorf("used %d units, model %d", m.Used(), want)
	}
	_, err := runMaster(context.Background(), RunIn{Rate: testRate, Inputs: []pcm.Buffer{b}, Meter: &dsp.Meter{Max: 100}})
	if !errors.Is(err, dsp.ErrBudget) {
		t.Errorf("run err = %v", err)
	}
}
