package transform

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
)

// The registration of the widen kind (C220, C225.4). The correlation guard runs inside the render; only the parameters, the unit
// model and the capabilities text live here.

var widenParams = []pdef{
	{Name: "source_id", Type: "track", Required: true, Decimals: -1},
	{Name: "width", Type: "number", Default: ops.WidenWidthDefault, Min: fp(ops.WidenWidthMin), Max: fp(ops.WidenWidthMax), Decimals: 2},
	{Name: "decorrelate", Type: "number", Default: ops.WidenDecorDefault, Min: fp(ops.WidenDecorMin), Max: fp(ops.WidenDecorMax), Decimals: 2},
	{Name: "bass_mono_hz", Type: "integer", Default: ops.WidenBassDefault, Min: fp(0), Max: fp(ops.WidenBassMax), Decimals: -1},
}

func validateWiden(raw json.RawMessage) (json.RawMessage, error) {
	d, err := decode(widenParams, raw)
	if err != nil {
		return nil, err
	}
	out := withDefaults(widenParams, d)
	if v, ok := d["width"].(float64); ok {
		out["width"] = snap(v, 0.05)
	}
	if v, ok := d["decorrelate"].(float64); ok {
		out["decorrelate"] = snap(v, 0.05)
	}
	if v, ok := d["bass_mono_hz"].(int); ok && v != 0 && v < ops.WidenBassMin {
		return nil, rule("bass_mono_hz", RuleRange) // 0 (off) or 60..250
	}
	return marshalResolved(out)
}

func titleWiden(in TitleIn) string {
	return "widen ×" + fmtNum(paramFloat(paramsMap(in.Resolved), "width", ops.WidenWidthDefault))
}

func unitsWiden(in UnitsIn) int64 { return ops.WidenUnits(in.OutFrames) }

func capsWiden() KindCapabilities {
	return KindCapabilities{
		Kind:    KindWiden,
		Summary: "A broader stereo stage whose mono fold-down is the parent's mono fold-down; the bass stays centred.",
		Inputs:  1,
		Params:  paramSpecs(widenParams),
		BlindSpots: []string{
			"widen keeps the mono fold-down; a source that is already wide is left unchanged",
			"widen adds width, not new content; the strength is lowered whenever the stereo correlation would leave its bounds",
			"a source that is already wide or out of phase is passed through and the result says so",
			"a mono source gains width only through decorrelate",
		},
		Example: `{"kind":"widen","widen":{"source_id":"<take id>","width":1.3}}`,
	}
}

func init() {
	// MemFactor 3 (the plan says 1): the render holds the mid, the side, the all-pass copy and the output at once.
	RegisterKind(KindSpec{Kind: KindWiden, Validate: validateWiden, Units: unitsWiden, Title: titleWiden, Capabilities: capsWiden, MemFactor: 3, Warnings: warnAfterBinaural})
}
