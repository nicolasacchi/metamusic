package runpod

import (
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// ProposedPatch is what a passing case proposes to the operator's reviewed PR (E2-X4): fields of
// features.verified.json to set (never applied here) and measured engine_rtf rows. A case that did not pass proposes
// nothing, so a flag is flipped only by evidence.
type ProposedPatch struct {
	Features  map[string]any `json:"features,omitempty"`
	TasksAdd  []string       `json:"tasks_add,omitempty"`
	EngineRTF []ProposedRTF  `json:"engine_rtf,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
}

// ProposedRTF is a measured engine_rtf row, written with updated_by canary:<id>.
type ProposedRTF struct {
	Engine    string `json:"engine"`
	TaskClass string `json:"task_class"`
	GPUClass  string `json:"gpu_class"`
	RTFMilli  int    `json:"rtf_milli"`
	LoadS     int    `json:"load_s"`
	UpdatedBy string `json:"updated_by"`
}

const (
	srcAudioS     = 30  // the source take of the edit cases
	longSourceS   = 120 // the longer source of the 90 s boundary range
	seededSeed    = 3   // the fixed seed of the reproducibility pairs
	cancelCaseS   = 120 // terminate and kill-switch take length
	timeoutCaseS  = 120
	timeoutRunS   = 30
	defaultLongS  = 480
	refWindowFrom = 5
	refWindowTo   = 25
)

func compose(label string, audioS int, seed int64) aceStage {
	return aceStage{label: label, audioS: audioS, class: acestep.ClassTurbo,
		req: func(id string) acestep.Request { return aceRequest(id, audioS, seed) }}
}

func sourceStage(label string, audioS int) aceStage {
	s := compose(label, audioS, 1)
	s.keep, s.skipWithSource = true, true
	return s
}

// proposeRTF adds the measured engine_rtf row of a plain render stage.
func proposeRTF(p *ProposedPatch, res *stageResult, class, by string) {
	if res == nil || res.execMS == nil || res.audioS == "" || res.gpuClass == "" {
		return
	}
	ms, ok := milliOf(jsonNum(res.audioS))
	if !ok || ms <= 0 {
		return
	}
	seedLoad := 10
	if class == acestep.ClassBase {
		seedLoad = 20
	}
	p.EngineRTF = append(p.EngineRTF, ProposedRTF{Engine: EngineACEStep, TaskClass: class, GPUClass: spend.GPUClassKey(res.gpuClass),
		RTFMilli: spend.ObservedRTF(*res.execMS, ms), LoadS: seedLoad, UpdatedBy: "canary:" + by})
}

func aceCasesA() []aceCaseDef {
	return []aceCaseDef{
		{name: CaseT2M, part: "a",
			stages: func(CanaryOptions) []aceStage { return []aceStage{compose("t2m", 30, 1)} },
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				// a worker that reached a job proves the cached model is visible to it
				p.Features["models_mode"] = acestep.ModelsBundleBaseBaked
				proposeRTF(p, s.by["t2m"], acestep.ClassTurbo, rep.RequestID)
			}},
		{name: CaseT2MLong, part: "a",
			stages: func(o CanaryOptions) []aceStage {
				n := o.MaxS
				if n <= 0 {
					n = defaultLongS
				}
				st := compose("t2m-long", n, 1)
				return []aceStage{st}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				r := s.by["t2m-long"]
				if r == nil || c.lastJob.ExpectedAudioS == 0 {
					return
				}
				// expected_audio_s must match the render: the quote and the cap were computed from it
				got, ok := milliOf(jsonNum(r.audioS))
				diff := got - int64(c.lastJob.ExpectedAudioS)*1000
				if diff < 0 {
					diff = -diff
				}
				c.check("expected_audio_matches", ok && diff <= 500, "audio %s s, expected_audio_s %d", r.audioS, c.lastJob.ExpectedAudioS)
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				n := o.MaxS
				if n <= 0 {
					n = defaultLongS
				}
				if n <= acestep.DefaultMaxS {
					p.Notes = append(p.Notes, "the default pools already allow this length; nothing to change in the pools")
					return
				}
				pools := map[string]any{}
				for _, pl := range o.Pools {
					pools[pl] = map[string]any{"max_s": n, "canary_commit": "<commit of this run>"}
				}
				p.Features["pools"] = pools
			}},
		{name: CaseReference, part: "a",
			stages: func(CanaryOptions) []aceStage {
				ref := func(label string) aceStage {
					return aceStage{label: label, audioS: srcAudioS, class: acestep.ClassTurbo, extraLoadS: 5,
						item: func(s *aceState, _ string) map[string]any {
							return map[string]any{"mode": "reference", "engine": EngineACEStep, "parent_track_id": s.src, "seeds": []int64{seededSeed},
								"reference": map[string]any{"track_id": s.src, "from_s": sec(refWindowFrom), "to_s": sec(refWindowTo)}}
						}}
				}
				return []aceStage{sourceStage("source", srcAudioS), compose("seed-a", 30, seededSeed), compose("seed-b", 30, seededSeed), ref("ref-a"), ref("ref-b")}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				if same, known := sameResults(s, "seed-a", "seed-b"); known {
					meas["seed_reproducible"] = same
				}
				if same, known := sameResults(s, "ref-a", "ref-b"); known {
					meas["reference_reproducible"] = same
				}
				// recorded, not gated: a non-reproducible render is a finding, not a failure
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.Features["reference"] = true
				if v, _ := rep.Measured["seed_reproducible"].(bool); v {
					p.Features["seed_reproducible"] = true
				}
				if v, _ := rep.Measured["reference_reproducible"].(bool); v {
					p.Features["reference_reproducible"] = true
				}
			}},
		{name: CaseRepaint, part: "a",
			stages: func(CanaryOptions) []aceStage {
				rng := aceStage{label: "range", audioS: srcAudioS, class: acestep.ClassTurbo, extraLoadS: 5,
					item: editItem("repaint", map[string]any{"from_s": sec(8), "to_s": sec(14)}, 1), want: srcMS}
				boundary := aceStage{label: "boundary", audioS: longSourceS, class: acestep.ClassTurbo, extraLoadS: 5, skipWithSource: true,
					item: editItem("repaint", map[string]any{"from_s": sec(30), "to_s": sec(120)}, 1), want: srcMS}
				return []aceStage{sourceStage("source", srcAudioS), rng, sourceStage("source-long", longSourceS), boundary}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				// the outside-range similarity needs the untouched regions of both masters through Ears' Tier A
				// code; the canary is a REST client without media access, so it is recorded as not computed
				meas["outside_range_similarity"] = "not_computed"
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				if o.SourceTrack != "" {
					p.Notes = append(p.Notes, "the 90 s boundary range was not run (--source-track): repaint is not proposed")
					return
				}
				p.TasksAdd = append(p.TasksAdd, acestep.TaskRepaint)
			}},
		{name: CaseTerminate, part: "a", stages: func(CanaryOptions) []aceStage { return []aceStage{compose("terminate", cancelCaseS, 1)} }},
		{name: CaseTimeout, part: "a", stages: func(CanaryOptions) []aceStage {
			s := compose("timeout", timeoutCaseS, 1)
			s.maxRuntimeS = timeoutRunS
			return []aceStage{s}
		}},
		{name: CaseKillSwitch, part: "a", stages: func(CanaryOptions) []aceStage { return []aceStage{compose("kill-switch", cancelCaseS, 1)} }},
		{name: CaseInitFail, part: "a", stages: func(CanaryOptions) []aceStage { return []aceStage{compose("init-fail", 30, 1)} }},
		{name: CaseFirstPull, part: "a",
			stages: func(CanaryOptions) []aceStage { return []aceStage{compose("first-pull", 30, 1)} },
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				if r := s.by["first-pull"]; r != nil {
					meas["first_pull_wall_s"] = r.wallS // unbilled by RunPod (D-E2-18): a wall-clock observation only
				}
			}},
	}
}

// sameResults compares the audio of two stages of a case.
func sameResults(s *aceState, a, b string) (same, known bool) {
	x, y := s.by[a], s.by[b]
	if x == nil || y == nil {
		return false, false
	}
	return sameArtifact(x, y)
}
