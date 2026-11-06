package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/hrtf"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// KindBinaural is the registry name of the headphone placement (C221).
const KindBinaural = "binaural"

// Models, rooms and the preset table of C221.1.
const (
	ModelHRTF       = "hrtf"
	ModelParametric = "parametric"

	RoomNone   = "none"
	RoomStudio = "studio"
	RoomHall   = "hall"

	BinauralDefaultModel  = ModelHRTF
	BinauralDefaultPreset = "studio"
	BinauralDefaultRoom   = RoomStudio
	BinauralDefaultMixDB  = -24
	BinauralDefaultTailS  = 1.0
	BinauralMinMixDB      = -40
	BinauralMaxMixDB      = -12
	BinauralMaxTailS      = 10.0
	BinauralMaxElevation  = 30
)

// BinauralPreset is a virtual loudspeaker pair: the azimuths of the left and the right channel, in degrees.
type BinauralPreset struct {
	Name            string
	LeftAz, RightAz int
}

// BinauralPresets is the preset table in canonical order (C221.1).
var BinauralPresets = []BinauralPreset{{"studio", -30, 30}, {"wide", -45, 45}, {"near", -22, 22}}

// BinauralPresetByName looks a preset up.
func BinauralPresetByName(name string) (BinauralPreset, bool) {
	for _, p := range BinauralPresets {
		if p.Name == name {
			return p, true
		}
	}
	return BinauralPreset{}, false
}

// Warning codes the binaural render can produce (C221.5).
const (
	WarnBinauralHeadphonesOnly   = "binaural_headphones_only"
	WarnBinauralMonoIncompatible = "binaural_mono_incompatible"
	WarnBinauralParametricBasic  = "binaural_parametric_basic"
	WarnBinauralEarImbalance     = "binaural_ear_imbalance"
	WarnElevationIgnored         = "elevation_ignored"
)

func init() { Register(KindBinaural, runBinaural, binauralUnits) }

// BinauralParams are the resolved parameters. The azimuths and the elevation are the values the spec records: the service
// has already snapped them to the table for the hrtf model (ParseBinaural snaps again, which changes nothing).
type BinauralParams struct {
	Model       string
	Preset      string
	LeftAz      int
	RightAz     int
	Elevation   int
	Room        string
	RoomMixDB   int
	TailS       float64
	elevationIn int // the elevation asked for, before the parametric model ignores it
}

// ParseBinaural reads the resolved params object of job.json: an absent or null member takes the preset's or the global
// default, and the ranges of C221.1 are enforced.
func ParseBinaural(raw json.RawMessage) (BinauralParams, error) {
	var in struct {
		Model     *string  `json:"model"`
		Preset    *string  `json:"preset"`
		LeftAz    *int     `json:"left_az_deg"`
		RightAz   *int     `json:"right_az_deg"`
		Elevation *int     `json:"elevation_deg"`
		Room      *string  `json:"room"`
		RoomMixDB *int     `json:"room_mix_db"`
		TailS     *float64 `json:"tail_s"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return BinauralParams{}, &ParamError{Field: "params", Rule: "type"}
		}
	}
	p := BinauralParams{Model: BinauralDefaultModel, Preset: BinauralDefaultPreset, Room: BinauralDefaultRoom, RoomMixDB: BinauralDefaultMixDB, TailS: BinauralDefaultTailS}
	if in.Model != nil {
		p.Model = *in.Model
	}
	if in.Preset != nil {
		p.Preset = *in.Preset
	}
	pr, ok := BinauralPresetByName(p.Preset)
	if !ok {
		return BinauralParams{}, &ParamError{Field: "preset", Rule: RulePreset}
	}
	p.LeftAz, p.RightAz = pr.LeftAz, pr.RightAz
	if in.LeftAz != nil {
		p.LeftAz = *in.LeftAz
	}
	if in.RightAz != nil {
		p.RightAz = *in.RightAz
	}
	if in.Elevation != nil {
		p.Elevation = *in.Elevation
	}
	if in.Room != nil {
		p.Room = *in.Room
	}
	if in.RoomMixDB != nil {
		p.RoomMixDB = *in.RoomMixDB
	}
	if in.TailS != nil {
		p.TailS = *in.TailS
	}
	if err := p.check(); err != nil {
		return BinauralParams{}, err
	}
	p.elevationIn = p.Elevation
	if p.Model == ModelHRTF {
		p.LeftAz, p.RightAz, p.Elevation = hrtf.SnapAz(p.LeftAz), hrtf.SnapAz(p.RightAz), hrtf.SnapEl(p.Elevation)
	} else {
		p.Elevation = 0 // the parametric model has no elevation cue
	}
	return p, nil
}

func (p BinauralParams) check() error {
	switch {
	case p.Model != ModelHRTF && p.Model != ModelParametric:
		return &ParamError{Field: "model", Rule: RuleRange}
	case p.LeftAz < -90 || p.LeftAz > -10:
		return &ParamError{Field: "left_az_deg", Rule: RuleRange}
	case p.RightAz < 10 || p.RightAz > 90:
		return &ParamError{Field: "right_az_deg", Rule: RuleRange}
	case p.Elevation < 0 || p.Elevation > BinauralMaxElevation:
		return &ParamError{Field: "elevation_deg", Rule: RuleRange}
	case p.Room != RoomNone && p.Room != RoomStudio && p.Room != RoomHall:
		return &ParamError{Field: "room", Rule: RuleRange}
	case p.RoomMixDB < BinauralMinMixDB || p.RoomMixDB > BinauralMaxMixDB:
		return &ParamError{Field: "room_mix_db", Rule: RuleRange}
	case !(p.TailS >= 0 && p.TailS <= BinauralMaxTailS):
		return &ParamError{Field: "tail_s", Rule: RuleRange}
	}
	return nil
}

// hasRoom says whether the room is rendered: room none, or a zero tail, means a dry output of the input's length.
func (p BinauralParams) hasRoom() bool { return p.Room != RoomNone }

// TailSamples is the number of frames the output is longer than the input: the generated room tail, cut where it falls below
// -70 dB (effLen frames) and at tail_s. effLen <= 0 stands for "not built yet": the upper bound of the cut is used.
func TailSamples(p BinauralParams, rate, effLen int) int {
	if !p.hasRoom() {
		return 0
	}
	limit := roundHalfUp(p.TailS * float64(rate))
	if effLen > 0 {
		return min(limit, effLen)
	}
	room, _ := ir.Room(p.Room)
	return min(limit, ir.TailBound(room, rate))
}

// BinauralLen is the exact output length for an input of frames frames and a built room of effLen frames (C221.4).
func BinauralLen(frames int, p BinauralParams, rate, effLen int) int {
	return frames + TailSamples(p, rate, effLen)
}

// binauralMixUnits is the price per input frame of the pairwise sums, the room mix and the copies around the convolutions.
const binauralMixUnits = 10

// BinauralUnits is the work Binaural adds to the meter, as an upper bound (the room is cut at -70 dB, which is only known once
// it is built): four convolutions with the 256-tap responses, the resampling of the four responses when the rate is not
// 48 000 Hz, and for a room its generation and two convolutions. Safety is not included.
func BinauralUnits(inFrames int64, rate int, p BinauralParams) int64 {
	const block = dsp.DefaultConvBlock
	conv := func(x, h int64) int64 {
		parts := (h + block - 1) / block
		blocks := (x + h - 1 + block - 1) / block
		return blocks * dsp.Units.ConvBlock(int(parts), 2*block)
	}
	hLen := int64(hrtf.Taps)
	var units int64
	if p.Model == ModelHRTF && rate != hrtf.Rate {
		hLen = int64(roundHalfUp(float64(hrtf.Taps) * float64(rate) / hrtf.Rate))
		units += 4 * hLen * dsp.Units.ResampleSample(dsp.DefaultZeros)
	} else if p.Model == ModelParametric {
		hLen = hrtf.ParametricTaps
	}
	units += 4*conv(inFrames, hLen) + binauralMixUnits*inFrames
	if p.hasRoom() {
		room, _ := ir.Room(p.Room)
		irLen := int64(ir.TailBound(room, rate))
		units += ir.Units(room, rate) + 2*conv(inFrames+hLen-1, irLen)
	}
	return units
}

// BinauralRunUnits is the whole price of a binaural render: BinauralUnits plus the safety stage on the longest output the
// room can give. The transform kind's cost model and the registry's units function are this one function.
func BinauralRunUnits(inFrames int64, rate int, p BinauralParams) int64 {
	return BinauralUnits(inFrames, rate, p) + SafetyUnits(inFrames+int64(TailSamples(p, rate, 0)))
}

func binauralUnits(in, _ int64, rate int, raw json.RawMessage) int64 {
	p, err := ParseBinaural(raw)
	if err != nil {
		p = BinauralParams{Model: ModelHRTF, Room: RoomHall, TailS: BinauralMaxTailS}
	}
	return BinauralRunUnits(in, rate, p)
}

// BinauralInfo is what the render measured.
type BinauralInfo struct {
	TailSamples      int
	ResampledFrom    int // the table rate when the responses were resampled, else 0
	RoomRT60         float64
	EarBalanceInDB   float64 // 10 log10(E_L/E_R) of the parent
	EarBalanceOutDB  float64 // the same, of the result, after Finish
	HRTF             *hrtf.Info
	LeftAz, RightAz  int
	Elevation        int
	ElevationIgnored bool
}

// earResponses returns the four responses [ear][speaker]: how the left and the right virtual loudspeaker reach each ear.
func earResponses(p BinauralParams, rate int, m *dsp.Meter) (h [2][2][]float64, from int, err error) {
	t := hrtf.Default()
	for ear, e := range [2]hrtf.Ear{hrtf.Left, hrtf.Right} {
		for sp, az := range [2]int{p.LeftAz, p.RightAz} {
			if p.Model == ModelParametric {
				h[ear][sp] = hrtf.ParametricIR(az, e, rate)
				continue
			}
			if h[ear][sp], err = t.IRAt(az, p.Elevation, e, rate, m); err != nil {
				return h, 0, err
			}
		}
	}
	if p.Model == ModelHRTF && rate != hrtf.Rate {
		from = hrtf.Rate
	}
	return h, from, nil
}

// add returns a + b over the longer of the two.
func add(a, b []float64) []float64 {
	if len(b) > len(a) {
		a, b = b, a
	}
	out := append([]float64(nil), a...)
	for i, v := range b {
		out[i] += v
	}
	return out
}

// earBalanceDB is 10 log10(E_L / E_R) with a floor so a silent channel gives a large finite number.
func earBalanceDB(b pcm.Buffer) float64 {
	var el, er float64
	for i := range b.Ch[0] {
		el += float64(b.Ch[0][i] * b.Ch[0][i])
		er += float64(b.Ch[1][i] * b.Ch[1][i])
	}
	const floor = 1e-30
	return float64(10 * dsp.Log((el+floor)/(er+floor)) / math.Ln10)
}

// Binaural places the stereo pair of x in front of the head and, for a room, adds the generated reverberation (C221.2-4): each
// ear hears both channels through the response of the direction of its speaker, yL = L*hLL + R*hLR, yR = L*hRL + R*hRR, then
// out = y + 10^(room_mix_db/20) * (y convolved with the room). The result is len(x) + the room tail frames long and has not
// been through the safety stage.
func Binaural(ctx context.Context, x pcm.Buffer, p BinauralParams, seed int64, m *dsp.Meter) (pcm.Buffer, BinauralInfo, error) {
	info := BinauralInfo{LeftAz: p.LeftAz, RightAz: p.RightAz, Elevation: p.Elevation}
	if err := p.check(); err != nil {
		return pcm.Buffer{}, info, err
	}
	rate, n := x.Rate, x.Frames()
	h, from, err := earResponses(p, rate, m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	info.ResampledFrom = from
	if p.Model == ModelHRTF {
		i := hrtf.Default().Info()
		info.HRTF = &i
	}
	var y [2][]float64
	for ear := range y {
		for sp := range 2 {
			if err := ctx.Err(); err != nil {
				return pcm.Buffer{}, info, err
			}
			c, err := dsp.ConvolveUPErr(x.Ch[sp], h[ear][sp], 0, m)
			if err != nil {
				return pcm.Buffer{}, info, err
			}
			y[ear] = add(y[ear], c)
		}
	}
	if err := m.Add(binauralMixUnits * int64(n)); err != nil {
		return pcm.Buffer{}, info, err
	}
	var wet [2][]float64
	if p.hasRoom() {
		room, _ := ir.Room(p.Room)
		rl, rr, rinfo, err := ir.BuildErr(room, rate, seed, m)
		if err != nil {
			return pcm.Buffer{}, info, err
		}
		info.RoomRT60 = rinfo.RT60Measured
		info.TailSamples = TailSamples(p, rate, rinfo.EffectiveLen)
		for ear, r := range [2][]float64{rl, rr} {
			if wet[ear], err = dsp.ConvolveUPErr(y[ear], r, 0, m); err != nil {
				return pcm.Buffer{}, info, err
			}
		}
	}
	outLen := n + info.TailSamples
	gain := dbToLin(float64(p.RoomMixDB))
	out := pcm.Buffer{Rate: rate}
	for ear := range out.Ch {
		o := make([]float64, outLen) // the HRIR tail beyond outLen is dropped under Finish's final fade
		copy(o, y[ear][:min(len(y[ear]), outLen)])
		for i := 0; i < min(len(wet[ear]), outLen); i++ {
			o[i] += float64(gain * wet[ear][i])
		}
		out.Ch[ear] = o
	}
	return out, info, nil
}

func runBinaural(ctx context.Context, in RunIn) (RunOut, error) {
	b, err := requireStereo(in)
	if err != nil {
		return RunOut{}, err
	}
	p, err := ParseBinaural(in.Params)
	if err != nil {
		return RunOut{}, err
	}
	m := in.Meter
	if m != nil && m.Ctx == nil {
		m.Ctx = ctx
	}
	if b.Rate == 0 {
		b.Rate = in.Rate
	}
	lufsIn, ok := safety.LUFS(b)
	if !ok {
		return RunOut{}, safety.ErrSilent
	}
	tpIn := safety.TruePeakDBTP(b)
	out, info, err := Binaural(ctx, b, p, in.Seed, m)
	if err != nil {
		return RunOut{}, err
	}
	if err := m.Add(SafetyUnits(int64(out.Frames()))); err != nil {
		return RunOut{}, err
	}
	rep, err := safety.Finish(&out, safety.FinishIn{TargetLUFS: in.TargetLUFS, RefLUFS: in.RefLUFS})
	if err != nil {
		return RunOut{}, err
	}
	info.EarBalanceInDB, info.EarBalanceOutDB = earBalanceDB(b), earBalanceDB(out)
	delta := info.EarBalanceOutDB - info.EarBalanceInDB
	bin := map[string]any{
		"model": p.Model, "preset": p.Preset, "az_used": map[string]int{"left": p.LeftAz, "right": p.RightAz}, "el_used": p.Elevation,
		"hrtf": nil, "room": p.Room, "room_mix_db": p.RoomMixDB, "tail_samples": info.TailSamples,
		"ear_balance_db": delta, "ear_balance_in_db": info.EarBalanceInDB, "hrir_resampled_from": nil, "lufs_in": lufsIn,
	}
	if info.HRTF != nil {
		bin["hrtf"] = info.HRTF
	}
	if info.ResampledFrom != 0 {
		bin["hrir_resampled_from"] = info.ResampledFrom
	}
	if p.hasRoom() {
		bin["room_rt60_s"] = info.RoomRT60
	}
	hr := hifiReport{Safety: newSafetyDoc(rep, tpIn), Hifi: map[string]any{"binaural": bin}}
	warn := func(code string) { hr.Warnings = append(hr.Warnings, hifiWarning{Code: code}) }
	warn(WarnBinauralHeadphonesOnly)
	warn(WarnBinauralMonoIncompatible)
	if p.Model == ModelParametric {
		warn(WarnBinauralParametricBasic)
		if p.elevationIn != 0 {
			warn(WarnElevationIgnored)
		}
	}
	if math.Abs(delta) > 3 && math.Abs(info.EarBalanceInDB) <= 1 {
		warn(WarnBinauralEarImbalance)
	}
	raw, err := hr.marshal()
	return RunOut{Buffer: out, Report: raw}, err
}
