package runpod

import (
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
)

// E2b cases of C155.4 (E2-X4b: the only gate for the E2b flags). Every case follows the same rules as the E2a ones.

const (
	CaseExtend   CanaryCase = "extend"
	CaseComplete CanaryCase = "complete"
	CaseExtract  CanaryCase = "extract"
	CaseBaseLong CanaryCase = "base-long"
	CaseLRC      CanaryCase = "lrc"

	// maxCandidateRuntimeS bounds the raised runtime the base-long case is priced and run with (MM_ACESTEP_MAX_RUNTIME
	// goes to 45 minutes at most).
	maxCandidateRuntimeS = 45 * 60
	placeholderLRCFixedS = 60
)

func baseEdit(label, mode string, tracks []string, audioS int, extraLoad int) aceStage {
	return aceStage{label: label, audioS: audioS, class: acestep.ClassBase, extraLoadS: extraLoad,
		item: editItem(mode, map[string]any{"tracks": tracks}, 0)}
}

func lrcRequest(id string, withLRC bool) acestep.Request {
	r := aceRequest(id, 30, 1)
	r.Instrumental, r.VocalLanguage = false, "en"
	r.Lyrics = "[Verse]\nla la la, soft and slow\nla la la, here we go\n"
	r.LRC = withLRC
	return r
}

func aceCasesB() []aceCaseDef {
	return []aceCaseDef{
		{name: CaseExtend, part: "b",
			stages: func(CanaryOptions) []aceStage {
				ext := func(label string, secs int, total int) aceStage {
					return aceStage{label: label, audioS: total, class: acestep.ClassTurbo, extraLoadS: 5,
						item: editItem("extend", map[string]any{"extend_s": sec(secs)}, 1),
						want: func(s *aceState) (int64, bool) {
							ms, ok := srcMS(s)
							return ms + int64(secs*1000), ok
						}}
				}
				// +10 s, then the +90 s boundary of the documented segment range
				return []aceStage{sourceStage("source", srcAudioS), ext("plus10", 10, srcAudioS+10), ext("plus90", 90, srcAudioS+90)}
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.TasksAdd = append(p.TasksAdd, acestep.TaskExtend)
				p.Notes = append(p.Notes, "extend_seam_experimental stays until a listening review of the seam clears it")
			}},
		{name: CaseComplete, part: "b",
			stages: func(CanaryOptions) []aceStage {
				c := baseEdit("complete", "complete", []string{"bass", "drums"}, srcAudioS, 5)
				c.want = srcMS
				return []aceStage{sourceStage("source", srcAudioS), c}
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.TasksAdd = append(p.TasksAdd, acestep.TaskComplete)
				p.Features["models_mode"] = acestep.ModelsBundleBaseBaked // the baked base DiT loaded and rendered
				proposeRTF(p, s.by["complete"], acestep.ClassBase, rep.RequestID)
			}},
		{name: CaseExtract, part: "b",
			stages: func(CanaryOptions) []aceStage {
				return []aceStage{sourceStage("source", srcAudioS), baseEdit("extract", "stems", []string{"vocals"}, srcAudioS, 5)}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				r := s.by["extract"]
				found := r != nil && slices.ContainsFunc(r.names, func(n string) bool { return strings.Contains(n, "vocals") })
				c.check("stem_artifact", found, "the job's artifacts name the vocals stem")
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.TasksAdd = append(p.TasksAdd, acestep.TaskExtract)
				proposeRTF(p, s.by["extract"], acestep.ClassBase, rep.RequestID)
			}},
		{name: CaseBaseLong, part: "b",
			stages: func(o CanaryOptions) []aceStage {
				n := o.MaxS
				if n <= 0 {
					n = defaultLongS
				}
				rtf, load := classRTF(acestep.ClassBase, poolsOrDefault(o.Pools))
				rt := min(maxCandidateRuntimeS, acestep.UnclampedTimeoutS(acestep.GenSeconds(n, rtf, load)))
				long := sourceStage("source-long", n)
				long.maxRuntimeS = min(maxCandidateRuntimeS, acestep.UnclampedTimeoutS(acestep.GenSeconds(n, mustRTF(acestep.ClassTurbo, o.Pools), 10)))
				b := baseEdit("base-long", "complete", []string{"drums"}, n, 5)
				b.maxRuntimeS, b.want = rt, srcMS
				return []aceStage{long, b}
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.Notes = append(p.Notes, "base-long is measured only: use its execution_ms to set MM_ACESTEP_MAX_RUNTIME (D-E2-17b)")
			}},
		{name: CaseLRC, part: "b",
			stages: func(CanaryOptions) []aceStage {
				plain := aceStage{label: "plain", audioS: 30, class: acestep.ClassTurbo,
					req: func(id string) acestep.Request { return lrcRequest(id, false) }}
				with := aceStage{label: "lrc", audioS: 30, class: acestep.ClassTurbo, extraLoadS: placeholderLRCFixedS,
					req: func(id string) acestep.Request { return lrcRequest(id, true) }}
				return []aceStage{plain, with}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				p, l := s.by["plain"], s.by["lrc"]
				if p == nil || l == nil {
					return
				}
				has := slices.Contains(l.names, "lyrics.lrc")
				meas["lrc_produced"] = has
				c.check("lrc_produced", has, "the lrc stage's artifacts name lyrics.lrc")
				if p.execMS != nil && l.execMS != nil {
					d := (*l.execMS - *p.execMS + 999) / 1000
					meas["lrc_fixed_s"] = max(d, 0)
				}
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.Features["lrc"] = true
				if v, ok := rep.Measured["lrc_fixed_s"].(int64); ok {
					p.Features["lrc_fixed_s"] = v
				}
			}},
	}
}

func poolsOrDefault(p []string) []string {
	if len(p) == 0 {
		return acestep.DefaultPools()
	}
	return p
}

func mustRTF(class string, pools []string) int {
	rtf, _ := classRTF(class, poolsOrDefault(pools))
	return rtf
}
