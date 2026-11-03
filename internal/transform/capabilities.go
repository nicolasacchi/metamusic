package transform

import (
	"context"
	"os"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Capabilities answers get_transform_capabilities (C212.5): the kinds with their parameter tables, the limits, the caller's
// quota, the queue and the host's tools. The kind entries are generated from the same Go tables that validate a request, so a
// listed range is an enforced range (TestCapabilities_MatchesValidation).
type Capabilities struct {
	Enabled    bool
	Kinds      []KindCapabilities
	Limits     LimitsInfo
	Quota      QuotaInfo
	QuotaNote  string
	Queue      QueueInfo
	Tools      ToolsInfo
	Soundfonts []SoundfontInfo
	DSPVersion string
}

// LimitsInfo are the limits of C200.4 and D-T1-10 as configured.
type LimitsInfo struct{ MaxInputs, MaxInputS, MaxOutputS, MaxMemMB, MaxCPUS, Concurrent int }

// QueueInfo is the queue as capabilities show it.
type QueueInfo struct{ Running, Queued, Max int }

// ToolsInfo is the tools of the host.
type ToolsInfo struct {
	FFmpeg, FLAC, Fluidsynth bool
	FluidsynthVersion        string
}

// SoundfontInfo is a configured soundfont as the API shows it: a name, a sha prefix and a size; never a path.
type SoundfontInfo struct {
	Name, SHA12 string
	Bytes       int64
}

// quotaNote is the sentence that states the shared pool (D-T1-10).
const quotaNote = "The daily CPU cap is one shared pool per principal kind: all agent tokens together draw from the agent cap, and humans from their own, so one agent can use up the agent pool and starve other agents but never a human."

// Capabilities returns the capabilities for the caller's principal kind.
func (s *Service) Capabilities(ctx context.Context, p auth.Principal) (Capabilities, error) {
	if !s.Enabled() {
		return Capabilities{}, unavailable("transform")
	}
	pool, err := principalPool(p)
	if err != nil {
		return Capabilities{}, err
	}
	cfg := s.o.Config
	ts := s.tools()
	c := Capabilities{
		Enabled: true, DSPVersion: DSPVersion, QuotaNote: quotaNote,
		Limits: LimitsInfo{MaxInputs: MaxInputs, MaxInputS: MaxInputS, MaxOutputS: cfg.TransformMaxOutS, MaxMemMB: cfg.TransformMaxMemMB,
			MaxCPUS: cfg.TransformMaxCPUS, Concurrent: ConcurrentRenders},
		Tools: ToolsInfo(ts),
	}
	if c.Quota, err = s.poolQuota(ctx, s.o.Store.R, pool); err != nil {
		return Capabilities{}, err
	}
	running, queued, err := s.queueState(ctx, s.o.Store.R)
	if err != nil {
		return Capabilities{}, err
	}
	c.Queue = QueueInfo{Running: running, Queued: queued, Max: cfg.TransformQueueMax}
	for _, sf := range cfg.Soundfonts {
		info := SoundfontInfo{Name: sf.Name}
		if st, err := os.Stat(sf.Path); err == nil {
			info.Bytes = st.Size()
		}
		if s.o.SoundfontSHA12 != nil {
			info.SHA12 = s.o.SoundfontSHA12(sf)
		}
		c.Soundfonts = append(c.Soundfonts, info)
	}
	scoreEnabled := len(cfg.Soundfonts) > 0 && ts.Fluidsynth
	for _, k := range RegisteredKinds() {
		if k == KindScoreRender && !scoreEnabled { // listed only when T1b is enabled
			continue
		}
		ks, _ := LookupKind(k)
		c.Kinds = append(c.Kinds, ks.Capabilities())
	}
	return c, nil
}

// paramSpecs turns a parameter table into the capabilities rows.
func paramSpecs(table []pdef) []ParamSpec {
	out := make([]ParamSpec, 0, len(table))
	for _, p := range table {
		ps := ParamSpec{Name: p.Name, Type: p.Type, Min: p.Min, Max: p.Max, Enum: p.Enum, Required: p.Required}
		if !p.Derived {
			ps.Default = p.Default
		}
		out = append(out, ps)
	}
	return out
}

// alignmentBlindSpots are the stated limits of the local beat-phase estimate (C204.3).
var alignmentBlindSpots = []string{
	"rubato and free-time intros: the local grid assumes a steady pulse inside the transition window",
	"half-time and double-time feel, and swing, can be read as the wrong tempo",
	"a tempo change inside the transition window lowers the confidence of the beat phase",
	"the downbeat heuristic (downbeat \"auto\") cannot work for four-on-the-floor and mis-reads a reggae one-drop; pass downbeat_a_s and downbeat_b_s",
}

func capsMorph() KindCapabilities {
	return KindCapabilities{
		Kind:       KindMorph,
		Summary:    "A beat- and key-aligned transition from one take into another, with the tempo ramped over a number of bars.",
		Inputs:     2,
		Params:     paramSpecs(morphParams),
		BlindSpots: alignmentBlindSpots,
		Example:    `{"kind":"morph","morph":{"track_a_id":"<take id>","track_b_id":"<other take id>","downbeat_a_s":0.4,"downbeat_b_s":0.2}}`,
	}
}

func capsPolytempo() KindCapabilities {
	return KindCapabilities{
		Kind:       KindPolytempo,
		Summary:    "Two takes at different tempos whose downbeats meet at a chosen bar and then run in unison.",
		Inputs:     2,
		Params:     paramSpecs(polytempoParams),
		BlindSpots: alignmentBlindSpots,
		Example:    `{"kind":"polytempo","polytempo":{"track_a_id":"<take id>","track_b_id":"<other take id>","downbeat_a_s":0.4,"downbeat_b_s":0.2}}`,
	}
}

func capsShepard() KindCapabilities {
	return KindCapabilities{
		Kind:    KindShepard,
		Summary: "An endlessly rising or falling construction built from a short loop of one take.",
		Inputs:  1,
		Params:  paramSpecs(shepardParams),
		BlindSpots: append([]string{
			"a shepard of five layers of a full mix is expected to sound muddy; use a pad-heavy, drum-free loop",
		}, alignmentBlindSpots[:2]...),
		Example: `{"kind":"shepard","shepard":{"source_id":"<take id>","loop_s":4}}`,
	}
}

func capsGlacier() KindCapabilities {
	return KindCapabilities{
		Kind:    KindGlacier,
		Summary: "A 10 to 50 times time-stretch of an excerpt into a long ambient piece; it is meant to sound processed.",
		Inputs:  1,
		Params:  paramSpecs(glacierParams),
		BlindSpots: []string{
			"transients dissolve completely: rhythm and lyrics are not recognisable after the stretch",
			"stereo is always decorrelated, so the result is wide and not mono compatible",
		},
		Example: `{"kind":"glacier","glacier":{"source_id":"<take id>","factor":20}}`,
	}
}

func capsEnsemble() KindCapabilities {
	return KindCapabilities{
		Kind:    KindEnsemble,
		Summary: "An octave-stacked, formant-shifted choir of one take in a generated room.",
		Inputs:  1,
		Params:  paramSpecs(ensembleParams),
		BlindSpots: []string{
			"a take is a full mix: the stack covers the whole band, drums and bass included; use an a-cappella-like take for a musical result",
			"the choir preset is fixed (five voices); free voices are not a parameter",
		},
		Example: `{"kind":"ensemble","ensemble":{"source_id":"<take id>","space":"cathedral"}}`,
	}
}
