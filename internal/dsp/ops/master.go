package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/filt"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// KindMaster is the registry name of the gentle master (C219).
const KindMaster = "master"

// MasterPreset is one row of the fixed preset table (C219.1), pinned by TestMaster_PresetTable.
type MasterPreset struct {
	Name       string
	TiltDB     float64
	CompAmount float64
	LowCutHz   int
}

// MasterPresets is the preset table in canonical order.
var MasterPresets = []MasterPreset{
	{"neutral", 0.0, 0.25, 25},
	{"warm", -1.5, 0.30, 25},
	{"bright", 1.5, 0.25, 25},
	{"punch", 0.0, 0.50, 30},
}

// PresetByName looks a preset up.
func PresetByName(name string) (MasterPreset, bool) {
	for _, p := range MasterPresets {
		if p.Name == name {
			return p, true
		}
	}
	return MasterPreset{}, false
}

// The master parameter ranges (C219.1) and the fixed chain constants (C219.2).
const (
	MasterTiltMin, MasterTiltMax     = -3.0, 3.0
	MasterCompMin, MasterCompMax     = 0.0, 1.0
	MasterTargetMin, MasterTargetMax = -30.0, -12.0
	MasterCeilingMin, MasterCeilMax  = -3.0, -1.0
	MasterDefaultPreset              = "neutral"

	tiltLowHz, tiltHighHz = 250.0, 2500.0
	shelfSlope            = 0.7
	compAttackSec         = 0.030
	compReleaseSec        = 0.300
	compThresholdOffsetDB = 2.0 // threshold = measured loudness + 2 dB
	compKneeDB            = 6.0
	compMaxReductionDB    = 6.0
)

// Warning codes the master render can produce (C219.3).
const (
	WarnMasterHeavyCompression = "master_heavy_compression"
	WarnMasterQuietBoost       = "master_quiet_boost"
	WarnMasterTargetLowered    = "master_target_lowered"
	WarnMasterInputClipped     = "master_input_clipped"
)

func init() { Register(KindMaster, runMaster, masterUnits) }

// MasterParams are the resolved parameters of the operator.
type MasterParams struct {
	Preset      string
	TiltDB      float64
	CompAmount  float64
	LowCutHz    int
	TargetLUFS  *float64 // nil: the loudness rule of the job
	CeilingDBTP float64
}

// ParseMaster reads the resolved params object of job.json: an absent or null member takes the preset's (or the global)
// default, and the ranges of C219.1 are enforced.
func ParseMaster(raw json.RawMessage) (MasterParams, error) {
	var in struct {
		Preset      *string  `json:"preset"`
		TiltDB      *float64 `json:"tilt_db"`
		CompAmount  *float64 `json:"comp_amount"`
		LowCutHz    *int     `json:"low_cut_hz"`
		TargetLUFS  *float64 `json:"target_lufs"`
		CeilingDBTP *float64 `json:"ceiling_dbtp"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return MasterParams{}, &ParamError{Field: "params", Rule: "type"}
		}
	}
	name := MasterDefaultPreset
	if in.Preset != nil {
		name = *in.Preset
	}
	pr, ok := PresetByName(name)
	if !ok {
		return MasterParams{}, &ParamError{Field: "preset", Rule: RulePreset}
	}
	p := MasterParams{Preset: name, TiltDB: pr.TiltDB, CompAmount: pr.CompAmount, LowCutHz: pr.LowCutHz, TargetLUFS: in.TargetLUFS, CeilingDBTP: safety.DefaultCeilingDBTP}
	if in.TiltDB != nil {
		p.TiltDB = *in.TiltDB
	}
	if in.CompAmount != nil {
		p.CompAmount = *in.CompAmount
	}
	if in.LowCutHz != nil {
		p.LowCutHz = *in.LowCutHz
	}
	if in.CeilingDBTP != nil {
		p.CeilingDBTP = *in.CeilingDBTP
	}
	return p, p.check()
}

func (p MasterParams) check() error {
	switch {
	case !(p.TiltDB >= MasterTiltMin && p.TiltDB <= MasterTiltMax):
		return &ParamError{Field: "tilt_db", Rule: RuleRange}
	case !(p.CompAmount >= MasterCompMin && p.CompAmount <= MasterCompMax):
		return &ParamError{Field: "comp_amount", Rule: RuleRange}
	case p.LowCutHz != 0 && (p.LowCutHz < 20 || p.LowCutHz > 40):
		return &ParamError{Field: "low_cut_hz", Rule: RuleRange}
	case p.TargetLUFS != nil && !(*p.TargetLUFS >= MasterTargetMin && *p.TargetLUFS <= MasterTargetMax):
		return &ParamError{Field: "target_lufs", Rule: RuleRange}
	case !(p.CeilingDBTP >= MasterCeilingMin && p.CeilingDBTP <= MasterCeilMax):
		return &ParamError{Field: "ceiling_dbtp", Rule: RuleRange}
	}
	return nil
}

// MasterUnits is the work-unit model of C223.1 as measured (C223.1 says 60 per frame and channel before the X0 step 5
// recalibration): biquads, compressor and detector, plus the safety stage.
func MasterUnits(frames int64) int64 { return 130*frames + SafetyUnits(frames) }

func masterUnits(_, out int64, _ int, _ json.RawMessage) int64 { return MasterUnits(out) }

// MasterInfo is what the chain measured.
type MasterInfo struct {
	LUFSIn           float64
	MeanReductionDB  float64
	MaxReductionDB   float64
	CompressorActive bool
}

// compressor holds the fixed ballistics and knee of C219.2 so the tests drive it with their own threshold.
type compressor struct {
	thresholdDB, ratio float64
}

// reductionDB is the static gain reduction, in dB (>= 0), at input level levelDB: a soft knee of compKneeDB around the
// threshold, slope 1 - 1/ratio above it, never more than compMaxReductionDB.
func (c compressor) reductionDB(levelDB float64) float64 {
	over := levelDB - c.thresholdDB
	var red float64
	switch {
	case float64(2*over) <= -compKneeDB:
		return 0
	case float64(2*math.Abs(over)) <= compKneeDB:
		x := over + compKneeDB/2
		red = float64((1-1/c.ratio)*float64(x*x)) / (2 * compKneeDB)
	default:
		red = float64((1 - 1/c.ratio) * over)
	}
	return math.Min(red, compMaxReductionDB)
}

// follow is one step of the peak follower: a one-pole smoother with the attack coefficient while the input rises above the
// envelope and the release coefficient otherwise.
func follow(env, x, att, rel float64) float64 {
	if x > env {
		return env + float64(att*(x-env))
	}
	return env + float64(rel*(x-env))
}

// timeConstant is the one-pole coefficient 1 - exp(-1/(tau*rate)).
func timeConstant(tau float64, rate int) float64 { return 1 - dsp.Exp(-1/(float64(tau)*float64(rate))) }

// compState is the compressor of C219.2 as a stream: the follower and the reduction sums live between blocks.
type compState struct {
	c                  compressor
	att, rel, floorLin float64
	env, sum, peak     float64
	frames             int
}

func newCompState(c compressor, rate int) *compState {
	return &compState{c: c, att: timeConstant(compAttackSec, rate), rel: timeConstant(compReleaseSec, rate),
		// below this envelope the reduction is exactly zero: skip the logarithm
		floorLin: dsp.Pow(10, (c.thresholdDB-compKneeDB/2)/20)}
}

// block applies the compressor in place to the next frames. The detector is max(|L|,|R|) through a one-pole peak follower
// (attack 30 ms, release 300 ms); there is no make-up gain.
func (s *compState) block(l, r []float64) {
	for i := range l {
		x := math.Max(math.Abs(l[i]), math.Abs(r[i]))
		s.env = follow(s.env, x, s.att, s.rel)
		if s.env <= s.floorLin {
			continue
		}
		red := s.c.reductionDB(float64(20 * dsp.Log(s.env) / math.Ln10))
		if red <= 0 {
			continue
		}
		g := dsp.Pow(10, -red/20)
		l[i] = float64(l[i] * g)
		r[i] = float64(r[i] * g)
		s.sum += red
		s.peak = math.Max(s.peak, red)
	}
	s.frames += len(l)
}

// reductions are the mean and the maximum reduction in dB of what went through.
func (s *compState) reductions() (mean, peak float64) {
	if s.frames > 0 {
		mean = s.sum / float64(s.frames)
	}
	return mean, s.peak
}

// compress applies the compressor to b in place and returns the mean and maximum reduction in dB.
func (c compressor) compress(b *pcm.Buffer, m *dsp.Meter) (mean, peak float64, err error) {
	n := b.Frames()
	st := newCompState(c, b.Rate)
	for start := 0; start < n; start += 4096 {
		end := min(start+4096, n)
		if err := m.Add(int64(end-start) * 30); err != nil {
			return 0, 0, err
		}
		st.block(b.Ch[0][start:end], b.Ch[1][start:end])
	}
	mean, peak = st.reductions()
	return mean, peak, nil
}

// hifiBlock is the number of frames the streamed chains of master, widen and binaural handle at a time; the parity tests lower it to
// put block boundaries in awkward places.
var hifiBlock = safety.StreamBlock

// masterStages are the filter sections of the chain of C219.2 (low cut, then the tilt as two shelves).
func masterStages(rate int, p MasterParams) filt.Cascade {
	var stages filt.Cascade
	if p.LowCutHz > 0 {
		stages = append(stages, filt.HighPass(rate, float64(p.LowCutHz), filt.Butterworth))
	}
	if p.TiltDB != 0 {
		stages = append(stages, filt.LowShelf(rate, tiltLowHz, -p.TiltDB/2, shelfSlope), filt.HighShelf(rate, tiltHighHz, p.TiltDB/2, shelfSlope))
	}
	return stages
}

// renderMaster runs the chain of C219.2 up to, but not including, the safety stage over b in blocks and hands the result to sink
// (the slices are only valid during the call): the memory is a block, not the signal. The arithmetic is that of the sample-by-sample
// recurrences of the filters and the compressor, which carry their state across blocks, so the samples are those of a single pass.
func renderMaster(ctx context.Context, b pcm.Buffer, p MasterParams, lufsIn float64, m *dsp.Meter, sink func(l, r []float64) error) (MasterInfo, error) {
	info := MasterInfo{LUFSIn: lufsIn}
	if err := p.check(); err != nil {
		return info, err
	}
	stages := masterStages(b.Rate, p)
	var fs [2]*filt.Stream
	if len(stages) > 0 {
		fs = [2]*filt.Stream{filt.NewStream(stages), filt.NewStream(stages)}
	}
	var comp *compState
	if p.CompAmount > 0 {
		info.CompressorActive = true
		comp = newCompState(compressor{thresholdDB: lufsIn + compThresholdOffsetDB, ratio: 1 + float64(1.5*p.CompAmount)}, b.Rate)
	}
	n := b.Frames()
	bl, br := make([]float64, min(hifiBlock, n)), make([]float64, min(hifiBlock, n))
	for start := 0; start < n; start += hifiBlock {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		end := min(start+hifiBlock, n)
		l, r := bl[:end-start], br[:end-start]
		copy(l, b.Ch[0][start:end])
		copy(r, b.Ch[1][start:end])
		if fs[0] != nil {
			for c, x := range [2][]float64{l, r} {
				if err := fs[c].Process(x, m); err != nil {
					return info, err
				}
			}
		}
		if comp != nil {
			if err := m.Add(int64(end-start) * 30); err != nil {
				return info, err
			}
			comp.block(l, r)
		}
		if err := sink(l, r); err != nil {
			return info, err
		}
	}
	if comp != nil {
		info.MeanReductionDB, info.MaxReductionDB = comp.reductions()
	}
	return info, nil
}

// collect returns a sink that appends what it is given to a buffer, and the buffer.
func collect(rate, frames int) (*pcm.Buffer, func(l, r []float64) error) {
	out := &pcm.Buffer{Rate: rate, Ch: [2][]float64{make([]float64, 0, frames), make([]float64, 0, frames)}}
	return out, func(l, r []float64) error {
		out.Ch[0], out.Ch[1] = append(out.Ch[0], l...), append(out.Ch[1], r...)
		return nil
	}
}

// Master runs the chain of C219.2 up to, but not including, the safety stage: low cut, tilt, compressor. lufsIn is the
// loudness of the input (the compressor threshold is lufsIn + 2 dB). The input is never trimmed or re-timed.
func Master(b pcm.Buffer, p MasterParams, lufsIn float64, m *dsp.Meter) (pcm.Buffer, MasterInfo, error) {
	out, sink := collect(b.Rate, b.Frames())
	info, err := renderMaster(context.Background(), b, p, lufsIn, m, sink)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	return *out, info, nil
}

func runMaster(ctx context.Context, in RunIn) (RunOut, error) {
	b, err := requireStereo(in)
	if err != nil {
		return RunOut{}, err
	}
	p, err := ParseMaster(in.Params)
	if err != nil {
		return RunOut{}, err
	}
	m := in.Meter
	if m != nil && m.Ctx == nil {
		m.Ctx = ctx
	}
	if err := m.Add(SafetyUnits(int64(b.Frames()))); err != nil {
		return RunOut{}, err
	}
	lufsIn, ok := safety.LUFS(b)
	if !ok {
		return RunOut{}, safety.ErrSilent
	}
	tpIn := safety.TruePeakDBTP(b)
	out, info, err := Master(b, p, lufsIn, m)
	if err != nil {
		return RunOut{}, err
	}
	rep, err := safety.Finish(&out, safety.FinishIn{TargetLUFS: p.target(in), RefLUFS: in.RefLUFS, CeilingDBTP: p.CeilingDBTP})
	if err != nil {
		return RunOut{}, err
	}
	raw, err := masterReportDoc(p, info, rep, tpIn)
	return RunOut{Buffer: out, Report: raw}, err
}

// target is the loudness target of the render: the explicit param, else the job's.
func (p MasterParams) target(in RunIn) float64 {
	if p.TargetLUFS != nil {
		return *p.TargetLUFS
	}
	return in.TargetLUFS
}

// streamMaster is runMaster with the render and the finish streamed: the chain goes block by block into a scratch spool, which
// safety.FinishStream reads and hands to sink, so the memory is the input and a few blocks, not the 100 bytes a frame of the in-memory
// finish. Same samples, same report (TestRunTo_MatchesRun).
func streamMaster(ctx context.Context, in RunIn, sc safety.Scratch, sink func(l, r []float64) error) (int, int, json.RawMessage, error) {
	b, err := requireStereo(in)
	if err != nil {
		return 0, 0, nil, err
	}
	p, err := ParseMaster(in.Params)
	if err != nil {
		return 0, 0, nil, err
	}
	m := in.Meter
	if m != nil && m.Ctx == nil {
		m.Ctx = ctx
	}
	if err := m.Add(SafetyUnits(int64(b.Frames()))); err != nil {
		return 0, 0, nil, err
	}
	src := safety.BufferSource(b)
	lufsIn, ok, err := safety.StreamLUFS(ctx, src)
	if err != nil {
		return 0, 0, nil, err
	}
	if !ok {
		return 0, 0, nil, safety.ErrSilent
	}
	tpIn, err := safety.StreamTruePeakDBTP(ctx, src)
	if err != nil {
		return 0, 0, nil, err
	}
	var info MasterInfo
	rep, err := renderAndFinish(ctx, sc, b.Rate, safety.FinishIn{TargetLUFS: p.target(in), RefLUFS: in.RefLUFS, CeilingDBTP: p.CeilingDBTP}, sink,
		func(w func(l, r []float64) error) (int, error) {
			var err error
			info, err = renderMaster(ctx, b, p, lufsIn, m, w)
			return b.Frames(), err
		}, nil)
	if err != nil {
		return 0, 0, nil, err
	}
	raw, err := masterReportDoc(p, info, rep, tpIn)
	return b.Rate, b.Frames(), raw, err
}

// masterReport is the operator's report: the safety object, the chain's numbers and the warnings of C219.3.
func masterReportDoc(p MasterParams, info MasterInfo, rep safety.FinishReport, tpIn float64) (json.RawMessage, error) {
	hr := hifiReport{Safety: newSafetyDoc(rep, tpIn), Hifi: map[string]any{"master": map[string]any{
		"preset": p.Preset, "tilt_db": p.TiltDB, "comp_amount": p.CompAmount, "low_cut_hz": p.LowCutHz,
		"ceiling_dbtp": rep.CeilingDBTP, "lufs_in": info.LUFSIn,
		"comp": map[string]any{"mean_reduction_db": info.MeanReductionDB, "max_reduction_db": info.MaxReductionDB},
	}}}
	warn := func(code string) { hr.Warnings = append(hr.Warnings, hifiWarning{Code: code}) }
	if info.MeanReductionDB > 3 {
		warn(WarnMasterHeavyCompression)
	}
	if rep.GainDB > 12 {
		warn(WarnMasterQuietBoost)
	}
	if rep.TargetLUFSEffective < rep.TargetLUFSRequested-0.05 {
		warn(WarnMasterTargetLowered)
	}
	if tpIn > 0 {
		warn(WarnMasterInputClipped)
	}
	return hr.marshal()
}
