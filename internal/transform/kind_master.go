package transform

import (
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
)

// The registration of the master kind (C219, C225.4): parameter table, validation, units, title and capabilities in one file, so
// the hi-fi lane edits no shared file. The operator itself registers with the dsp registry from internal/dsp/ops.

var masterPresetNames = func() []string {
	names := make([]string, len(ops.MasterPresets))
	for i, p := range ops.MasterPresets {
		names[i] = p.Name
	}
	return names
}()

// masterParams is the C219.1 table. tilt_db, comp_amount and low_cut_hz default to the preset's values, so they are Derived: the
// capabilities row shows no static default and Validate writes the preset's value into the spec.
var masterParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "preset", Type: "enum", Default: ops.MasterDefaultPreset, Enum: masterPresetNames, Decimals: -1},
	{Name: "tilt_db", Type: "number", Min: fp(ops.MasterTiltMin), Max: fp(ops.MasterTiltMax), Decimals: 1, Derived: true},
	{Name: "comp_amount", Type: "number", Min: fp(ops.MasterCompMin), Max: fp(ops.MasterCompMax), Decimals: 2, Derived: true},
	{Name: "low_cut_hz", Type: "integer", Min: fp(0), Max: fp(40), Decimals: -1, Derived: true},
	{Name: "target_lufs", Type: "number", Min: fp(ops.MasterTargetMin), Max: fp(ops.MasterTargetMax), Decimals: 1, NullIsV: true, Derived: true},
	{Name: "ceiling_dbtp", Type: "number", Default: -1.0, Min: fp(ops.MasterCeilingMin), Max: fp(ops.MasterCeilMax), Decimals: 1},
}

// snap rounds v to the nearest multiple of step (the 0.05 parameters), after which it is written into the spec.
func snap(v, step float64) float64 { return round(math.Round(v/step)*step, 2) }

func validateMaster(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(masterParams, raw)
	if err != nil {
		return nil, err
	}
	out := withDefaults(masterParams, d)
	name, _ := out["preset"].(string)
	pr, _ := ops.PresetByName(name)
	out["tilt_db"], out["comp_amount"], out["low_cut_hz"] = pr.TiltDB, pr.CompAmount, pr.LowCutHz
	if v, ok := d["tilt_db"].(float64); ok {
		out["tilt_db"] = v
	}
	if v, ok := d["comp_amount"].(float64); ok {
		out["comp_amount"] = snap(v, 0.05)
	}
	if v, ok := d["low_cut_hz"].(int); ok {
		if v != 0 && v < 20 {
			return nil, rule("low_cut_hz", RuleRange) // 0 (off) or 20..40
		}
		out["low_cut_hz"] = v
	}
	out["target_lufs"] = nil // null: the D-T1-14 rule (the parent's loudness, else -16)
	if v, ok := d["target_lufs"].(float64); ok {
		out["target_lufs"] = v
	}
	return marshalResolved(out)
}

func titleMaster(in TitleIn) string {
	if p := paramString(paramsMap(in.Resolved), "preset"); p != "" {
		return "master " + p
	}
	return "master"
}

// unitsMaster is C223.1: biquads, compressor and detector per frame and channel, plus the safety stage; the same function the
// operator's work meter uses.
func unitsMaster(in UnitsIn) int64 { return ops.MasterUnits(in.OutFrames) }

func capsMaster() KindCapabilities {
	return KindCapabilities{
		Kind:    KindMaster,
		Summary: "A gentle polish of a whole take: a clean low end, a light tonal tilt, light levelling and a true-peak-safe finish at the loudness of the parent.",
		Inputs:  1,
		Params:  paramSpecs(masterParams),
		BlindSpots: []string{
			"master processes the full mix; it cannot add detail",
			"master cannot rescue a poor mix; at most it makes a take cleaner and level-matched to its parent",
			"the result is level-matched to the parent, so a duel against the parent compares tone, not volume",
			"the true-peak ceiling is -1.0 dBTP by default and can be set no lower than -3.0 and never above -1.0",
		},
		Example: `{"kind":"master","master":{"source_id":"<take id>","preset":"warm"}}`,
	}
}

// warnAfterBinaural is the estimate-time warning of master and widen: the image of a binaural take is already folded.
func warnAfterBinaural(in WarnIn) []Warning {
	for _, k := range in.Chain {
		if k == KindBinaural {
			return []Warning{{Code: WarnAfterBinaural}}
		}
	}
	return nil
}

func init() {
	RegisterKind(KindSpec{Kind: KindMaster, Validate: validateMaster, Units: unitsMaster, Title: titleMaster, Capabilities: capsMaster, MemFactor: 1, Warnings: warnAfterBinaural})
}
