package acestep

import "github.com/nicolasacchi/metamusic/internal/engine"

// Phase constants of the quote's timeout formula (spend.QuoteTakes): timeout = ceil(1.6 x gen) + 10 + 60 + 10.
const timeoutOverheadS = 80

// QuoteInput is what the spend quote needs to know about one take, without any price or rate: the class picks
// the RTF row, AudioS is the exact length (never clamped) and ExtraLoadS adds the alignment pass.
type QuoteInput struct {
	Task, Class string
	AudioS      int
	LRC         bool
}

// QuoteInputOf derives the quote inputs of a parsed request.
func QuoteInputOf(r Request) QuoteInput {
	return QuoteInput{Task: r.Task, Class: r.Class(), AudioS: ExpectedSeconds(r), LRC: r.LRC}
}

// ExtraLoadS is the fixed seconds added to the take's load term: lrc_fixed_s when the take runs the alignment pass.
func (q QuoteInput) ExtraLoadS(f Features) int {
	if q.LRC {
		return f.LRCFixedS
	}
	return 0
}

// LearnsRTF reports whether a take of this task updates its class RTF: only a full-length render of an enabled
// task of the class (turbo: text2music; base: complete and extract). repaint and extend read the turbo RTF.
func LearnsRTF(task string) bool {
	return task == TaskText2Music || task == TaskComplete || task == TaskExtract
}

// GenSeconds is the generation phase of a take: loadS + ceil(rtfMilli x audioS / 1000).
func GenSeconds(audioS, rtfMilli, loadS int) int { return loadS + (rtfMilli*audioS+999)/1000 }

// UnclampedTimeoutS is the take timeout before the backend cap: ceil(8 x gen / 5) + 80.
func UnclampedTimeoutS(genS int) int { return (8*genS+4)/5 + timeoutOverheadS }

// CheckRuntime refuses, rather than clamps, a take whose unclamped timeout exceeds the backend's maximum
// runtime (D-E2-17b). It returns nil when the take fits.
func CheckRuntime(audioS, rtfMilli, loadS, maxRuntimeS int) *engine.Issue {
	t := UnclampedTimeoutS(GenSeconds(audioS, rtfMilli, loadS))
	if t <= maxRuntimeS {
		return nil
	}
	i := issue("exceeds_max_runtime", "duration_ds", "expected timeout %d s exceeds max runtime %d s; fix=MM_ACESTEP_MAX_RUNTIME", t, maxRuntimeS)
	return &i
}

// MaxAudioForRuntime is the longest take, in seconds, that CheckRuntime accepts (0 when none fits).
func MaxAudioForRuntime(rtfMilli, loadS, maxRuntimeS int) int {
	lo, hi := 0, AbsoluteMaxS
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if CheckRuntime(mid, rtfMilli, loadS, maxRuntimeS) == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
