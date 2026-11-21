package generation

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// Tier B warning codes (C339.4). Stable API values.
const (
	WarnTierBUnavailable    = "tier_b_unavailable"
	WarnTierBNoLyrics       = "tier_b_no_lyrics"
	WarnTierBExceedsRuntime = "tier_b_exceeds_runtime"
	WarnTierBEngine         = "tier_b_engine"
)

const tierBField = "options.tier_b"

// earsPlan is the resolved Tier B decision of an estimate: the expected Tier B seconds of every take that has a listening check,
// keyed by its index in the planner's takes. A nil plan has none.
type earsPlan map[int]int

// secs is the expected Tier B seconds of take k, 0 when it has no listening check (every planned take has at least one second).
func (e earsPlan) secs(k int) int { return e[k] }

func tierBWarn(t *planTake, code, msg string) {
	t.Warnings = append(t.Warnings, Issue{Code: code, Field: tierBField, Severity: "warning", Message: msg})
}

// canaryRenderID reports whether a request id is one a canary minted; auto mode leaves those renders plain.
func canaryRenderID(id string) bool { return strings.HasPrefix(id, "mm-canary-") }

// resolveEars decides, for every planned take, whether it gets a listening check (D-EARS-2), and writes the take's spec and needs.
// tierB is the estimate's options.tier_b: nil is auto (on where the take has lyric lines, fits the runtime cap and the routed
// backend has the capabilities), true is on and refuses where it cannot be (a closed capability is requireCaps's 503), false is off.
// It runs after the plan is expanded and before requireCaps, so the needs it adds are checked with everything else.
func (s *Service) resolveEars(ctx context.Context, pl *planner, st provider.State, tierB *bool) (earsPlan, error) {
	explicit := tierB != nil
	if !s.earsQ.on || (explicit && !*tierB) {
		return nil, nil
	}
	if pl.ace {
		if explicit {
			return nil, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "options.tier_b is for yue2 renders only", map[string]any{"field": tierBField})
		}
		for _, t := range pl.takes {
			tierBWarn(t, WarnTierBEngine, "no listening check: it is available for yue2 renders only")
		}
		return nil, nil
	}
	var (
		out         earsPlan
		qp          *spend.QuoteParams
		closedKnown bool
		closed      bool
	)
	for k, t := range pl.takes {
		if t.Kind != "" && t.Kind != jobspec.KindGenerate {
			continue
		}
		if !explicit && canaryRenderID(t.RequestID) {
			continue
		}
		req := probeRequest(t.RequestJSON)
		if s.earsQ.o.lyricLines(req.Lyrics) == 0 {
			tierBWarn(t, WarnTierBNoLyrics, "no listening check: the lyrics have no line to listen for")
			continue
		}
		transcribe := req.ABC != "" || req.Cot == "full"
		earsS := max(1, spend.EarsExpectedS(t.ExpectedAudioS, s.earsQ.o.LoadS, s.earsQ.o.RTFMilli, s.earsQ.o.TranscribeRTFMilli, transcribe))
		if !explicit {
			if !closedKnown {
				open, err := provider.WorkerCaps(ctx, s.o.Store.R, st.Backend, st.Quote, s.o.Now())
				if err != nil {
					return nil, err
				}
				closed, closedKnown = len(provider.MissingCaps([]string{jobspec.CapEars, jobspec.CapV2}, open)) > 0, true
			}
			if closed {
				tierBWarn(t, WarnTierBUnavailable, "no listening check: the worker image is not open for it yet")
				continue
			}
		}
		if qp == nil {
			q, _, err := s.quoteInputs(ctx, s.o.Store.R, st)
			if err != nil {
				return nil, err
			}
			qp = &q
		}
		if !spend.TierBFits(*qp, spend.TakeIn{AudioS: t.ExpectedAudioS, Full: req.Cot == "full", EarsS: earsS}) {
			if explicit {
				return nil, invalidItem(t.ItemIndex, tierBField, "tier_b_exceeds_runtime")
			}
			tierBWarn(t, WarnTierBExceedsRuntime, "no listening check: it would not fit the runtime cap")
			continue
		}
		if err := s.setEarsSpec(t, req, earsS, transcribe); err != nil {
			return nil, err
		}
		if out == nil {
			out = earsPlan{}
		}
		out[k] = earsS
	}
	return out, nil
}

// setEarsSpec adds the Ears block to the take's spec and the capabilities it needs (ears, v2), keeping anything already there.
func (s *Service) setEarsSpec(t *planTake, req earsProbe, earsS int, transcribe bool) error {
	sp, err := jobspec.Parse(t.Spec)
	if err != nil {
		return err
	}
	stages := []string{"separate", "asr"}
	if transcribe {
		stages = append(stages, "transcribe")
	}
	// the language is forced when the lyrics tell it (D-EARS-5); unknown is auto-detected by the worker
	sp.Ears = &jobspec.Ears{Lang: s.earsQ.o.language(req.Lyrics), BudgetS: spend.EarsBudgetS(earsS), Stages: stages}
	raw, err := sp.Canonical()
	if err != nil {
		return err
	}
	t.Spec = raw
	t.Needs = jobspec.WithCaps(t.Needs, jobspec.CapEars)
	return nil
}

// earsProbe is what the Tier B decision reads from a canonical yue2 request.
type earsProbe struct {
	Lyrics string `json:"lyrics"`
	Cot    string `json:"cot"`
	ABC    string `json:"abc"`
}

func probeRequest(requestJSON string) earsProbe {
	var p earsProbe
	_ = json.Unmarshal([]byte(requestJSON), &p) // a take whose request does not parse has no lyrics: no listening check
	return p
}
