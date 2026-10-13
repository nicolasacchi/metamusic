package ears

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const maxSummary = 300

func firstSpan(ev map[string]any) (analysis.Span, bool) {
	sp, _ := ev["spans"].([]analysis.Span)
	if len(sp) == 0 {
		return analysis.Span{}, false
	}
	return sp[0], true
}

// describe is the short phrase of a warning in the summary.
func describe(f Flag) string {
	switch f.Code {
	case "clipping":
		if sp, ok := firstSpan(f.Evidence); ok {
			c, _ := f.Evidence["clipped_s"].(float64)
			return fmt.Sprintf("clipping %.1f s at %s", c, mmss(sp.StartS))
		}
	case "internal_silence":
		if sp, ok := firstSpan(f.Evidence); ok {
			return fmt.Sprintf("internal silence %.1f s at %s", sp.EndS-sp.StartS, mmss(sp.StartS))
		}
	case "truncated":
		return "truncated output"
	case "token_loop_suspected":
		for _, k := range []string{"at_s", "window_at_s"} {
			if at, ok := f.Evidence[k].(float64); ok {
				return "token loop suspected at " + mmss(at)
			}
		}
	}
	return strings.ReplaceAll(f.Code, "_", " ")
}

// summarize is the deterministic one-line dossier summary (at most 300 characters).
func summarize(st *state) string {
	var metrics []string
	if l := st.loudness; l != nil {
		metrics = append(metrics, fmt.Sprintf("%.1f LUFS", l.IntegratedLUFS), fmt.Sprintf("true peak %.1f dBTP", l.TruePeakDBTP))
	}
	if sp := st.spectrum; sp != nil && sp.BandwidthHz != nil {
		metrics = append(metrics, fmt.Sprintf("bandwidth %.1f kHz", float64(*sp.BandwidthHz)/1000))
	}
	out := "no loudness or bandwidth measured"
	if len(metrics) > 0 {
		out = strings.Join(metrics, ", ")
	}
	var warns []string
	for _, f := range st.flags {
		if f.Severity == "warn" {
			warns = append(warns, describe(f))
		}
	}
	switch {
	case len(warns) == 1:
		out += "; 1 warning: " + warns[0]
	case len(warns) > 1:
		out += fmt.Sprintf("; %d warnings: %s", len(warns), strings.Join(warns, "; "))
	case st.tier == "A":
		out += "; no warnings"
	}
	if st.tier != "A" {
		out += "; Tier A incomplete (missing: " + strings.Join(st.missing, ", ") + ")"
	}
	if utf8.RuneCountInString(out) > maxSummary {
		out = string([]rune(out)[:maxSummary-1]) + "…"
	}
	return out
}
