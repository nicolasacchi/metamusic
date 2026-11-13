package transform

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/dsp/hrtf"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
)

// The registration of the binaural kind (C221, C225.4): parameter table, validation, units, title, estimate-time warnings and
// capabilities in one file. The refusal of an already binaural source (C218.5) and the length of the output are the service's
// (estimate.go, resolve.go); the operator registers with the dsp registry from internal/dsp/ops.

var binauralPresetNames = func() []string {
	names := make([]string, len(ops.BinauralPresets))
	for i, p := range ops.BinauralPresets {
		names[i] = p.Name
	}
	return names
}()

// binauralParams is the C221.1 table. The azimuths default to the preset's, so they are Derived: the capabilities row shows no
// static default and Validate writes the preset's (snapped) value into the spec.
var binauralParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "model", Type: "enum", Default: ops.BinauralDefaultModel, Enum: []string{ops.ModelHRTF, ops.ModelParametric}, Decimals: -1},
	{Name: "preset", Type: "enum", Default: ops.BinauralDefaultPreset, Enum: binauralPresetNames, Decimals: -1},
	{Name: "left_az_deg", Type: "integer", Min: fp(-90), Max: fp(-10), Decimals: -1, Derived: true},
	{Name: "right_az_deg", Type: "integer", Min: fp(10), Max: fp(90), Decimals: -1, Derived: true},
	{Name: "elevation_deg", Type: "integer", Default: 0, Min: fp(0), Max: fp(ops.BinauralMaxElevation), Decimals: -1},
	{Name: "room", Type: "enum", Default: ops.BinauralDefaultRoom, Enum: []string{ops.RoomNone, ops.RoomStudio, ops.RoomHall}, Decimals: -1},
	{Name: "room_mix_db", Type: "integer", Default: ops.BinauralDefaultMixDB, Min: fp(ops.BinauralMinMixDB), Max: fp(ops.BinauralMaxMixDB), Decimals: -1},
	{Name: "tail_s", Type: "number", Default: ops.BinauralDefaultTailS, Min: fp(0), Max: fp(ops.BinauralMaxTailS), Decimals: 2},
}

// validateBinaural writes every default out. For the hrtf model the azimuths and the elevation are snapped to the table and the
// snapped values are what the spec records (C221.1); the parametric model keeps the azimuths as sent and records the elevation
// it will ignore, so the estimate can say so.
func validateBinaural(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(binauralParams, raw)
	if err != nil {
		return nil, err
	}
	out := withDefaults(binauralParams, d)
	pr, _ := ops.BinauralPresetByName(out["preset"].(string))
	left, right := pr.LeftAz, pr.RightAz
	if v, ok := d["left_az_deg"].(int); ok {
		left = v
	}
	if v, ok := d["right_az_deg"].(int); ok {
		right = v
	}
	el := out["elevation_deg"].(int)
	if out["model"] == ops.ModelHRTF {
		left, right, el = hrtf.SnapAz(left), hrtf.SnapAz(right), hrtf.SnapEl(el)
	}
	out["left_az_deg"], out["right_az_deg"], out["elevation_deg"] = left, right, el
	return marshalResolved(out)
}

func titleBinaural(in TitleIn) string {
	m := paramsMap(in.Resolved)
	t := "binaural"
	if p := paramString(m, "preset"); p != "" {
		t += " " + p
	}
	if paramString(m, "model") == ops.ModelParametric {
		t += " (parametric)"
	}
	return t
}

// unitsBinaural is C223.1: four convolutions with the responses, the room's generation and two convolutions, the mixes and the
// safety stage; the same function the operator's registry entry uses.
func unitsBinaural(in UnitsIn) int64 {
	p, err := ops.ParseBinaural(in.Resolved)
	if err != nil { // the resolved spec was written by validateBinaural; this is the priciest valid choice
		p = ops.BinauralParams{Model: ops.ModelHRTF, Room: ops.RoomHall, TailS: ops.BinauralMaxTailS}
	}
	return ops.BinauralRunUnits(in.InFrames, in.Rate, p)
}

// warnBinaural are the estimate-time warnings of C223.3, from the resolved parameters only.
func warnBinaural(in WarnIn) []Warning {
	w := []Warning{{Code: WarnBinauralHeadphonesOnly}, {Code: WarnBinauralMonoIncompatible}}
	m := paramsMap(in.Resolved)
	if paramString(m, "model") == ops.ModelParametric {
		w = append(w, Warning{Code: WarnBinauralParametricBasic})
		if paramInt(m, "elevation_deg", 0) != 0 {
			w = append(w, Warning{Code: WarnElevationIgnored, Field: "elevation_deg"})
		}
	}
	return w
}

func capsBinaural() KindCapabilities {
	return KindCapabilities{
		Kind:    KindBinaural,
		Summary: "A headphone copy: the left and right channels of the take are placed as two virtual loudspeakers in front of the head, with an optional small generated room.",
		Inputs:  1,
		Params:  paramSpecs(binauralParams),
		BlindSpots: []string{
			"binaural is for headphones only; it places the stereo pair in front of the head, not individual instruments; a binaural Take is never processed twice",
			"the mono fold-down and loudspeaker playback of a binaural copy are worse than the parent's; the copy is a listening copy, not a master",
			"the hrtf model uses one measured dummy head, not your ears: it can sound less out-of-head than a personal measurement",
			"the parametric model only moves sounds left and right (lateralisation); it has no elevation and does not externalise",
			"the room is generated reverberation, never a measured space; a tail longer than tail_s is cut",
		},
		Example:   `{"kind":"binaural","binaural":{"source_id":"<take id>","preset":"studio","room":"studio"}}`,
		Listening: "headphones",
		Dataset:   hrtf.Default().Info().Credit,
	}
}

func init() {
	// MemFactor is unused: binaural streams its convolutions and its finish, so kindMemoryMB counts the input, the room and a window.
	RegisterKind(KindSpec{Kind: KindBinaural, Validate: validateBinaural, Units: unitsBinaural, Title: titleBinaural, Capabilities: capsBinaural, MemFactor: 0, Warnings: warnBinaural})
}
