package transform

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// registerStandIns registers a minimal spec for the hi-fi kinds that no lane has registered yet, so the generic paths (chain,
// warnings, capabilities, binaural refusal) are tested whatever has merged. Real registrations from lanes H1 and H2 win: a stand-in
// is added only for a kind that is absent, and never for score_render (T1b), whose absence is itself tested.
func registerStandIns() {
	for _, k := range []Kind{KindMaster, KindWiden, KindBinaural} {
		if _, ok := LookupKind(k); ok {
			continue
		}
		k := k
		ks := KindSpec{
			Kind: k,
			Validate: func(raw json.RawMessage) (json.RawMessage, error) {
				d, err := decode([]pdef{{Name: "source_id", Type: "track", Required: true, Decimals: -1}, {Name: "tail_s", Type: "number", Default: 1.0, Min: fp(0), Max: fp(10), Decimals: 3},
					{Name: "room", Type: "enum", Default: "studio", Enum: []string{"none", "studio", "hall"}, Decimals: -1}}, raw)
				if err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{"source_id": d["source_id"], "room": "studio", "tail_s": 1.0})
			},
			Units:        func(u UnitsIn) int64 { return 60 * u.OutFrames * int64(u.Channels) },
			Title:        func(TitleIn) string { return string(k) },
			Capabilities: func() KindCapabilities { return KindCapabilities{Kind: k, Summary: "stand-in", Inputs: 1} },
			MemFactor:    1,
		}
		if k == KindBinaural {
			ks.Capabilities = func() KindCapabilities {
				return KindCapabilities{Kind: k, Summary: "stand-in", Inputs: 1, Listening: "headphones", Dataset: "test credit"}
			}
		} else {
			ks.Warnings = func(in WarnIn) []Warning {
				if slices.Contains(in.Chain, KindBinaural) {
					return []Warning{{Code: WarnAfterBinaural}}
				}
				return nil
			}
		}
		RegisterKind(ks)
	}
}

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	registerStandIns()
	code := m.Run()
	if tmplDir != "" {
		_ = os.RemoveAll(tmplDir)
	}
	os.Exit(code)
}
