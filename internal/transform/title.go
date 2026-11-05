package transform

import (
	"strings"
	"unicode/utf8"
)

// DefaultTitle is the title of a transform (C202.4): "<primary parent title> — <label>" cut to MaxTitleRunes runes at a rune
// boundary, the separator being an em dash (U+2014). An explicit title wins (trimmed, and cut like any title). A transform
// without a parent (score_render, T1b) is just its label.
func DefaultTitle(explicit *string, primary, label string) string {
	if explicit != nil {
		if t := strings.TrimSpace(*explicit); t != "" {
			return cutRunes(t, MaxTitleRunes)
		}
	}
	switch {
	case primary == "" && label == "":
		return "Transform"
	case primary == "":
		return cutRunes(label, MaxTitleRunes)
	case label == "":
		return cutRunes(primary, MaxTitleRunes)
	}
	return cutRunes(primary+" — "+label, MaxTitleRunes)
}

// cutRunes cuts s to at most n runes; the cut is always on a rune boundary.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func titleMorph(in TitleIn) string {
	return "→ " + in.Secondary + " morph"
}

func titleShepard(in TitleIn) string {
	if paramString(paramsMap(in.Resolved), "direction") == "down" {
		return "shepard ↓"
	}
	return "shepard ↑"
}

func titleGlacier(in TitleIn) string {
	return "glacier ×" + fmtNum(paramFloat(paramsMap(in.Resolved), "factor", 20))
}

func titlePolytempo(in TitleIn) string {
	m := paramsMap(in.Resolved)
	return "polytempo " + fmtNum(paramFloat(m, "bpm_a", 0)) + "/" + fmtNum(paramFloat(m, "bpm_b", 0))
}

func titleEnsemble(in TitleIn) string {
	return "ensemble " + paramString(paramsMap(in.Resolved), "space")
}
