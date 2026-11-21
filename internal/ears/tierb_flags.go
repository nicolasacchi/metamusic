package ears

// tierBFlagInput is what the Tier B ear checks read (C338.2).
type tierBFlagInput struct {
	State     string
	Code      string
	Words     int // recognised words
	RefWords  int // lyric words
	Diction   *tierBDictionDoc
	Adherence *TierBAdherence
}

// tierBFlags builds the flag input of a state; nil when the take has no listening check to report on.
func (st *state) tierBFlags() *tierBFlagInput {
	switch st.tbState {
	case TierBNone, TierBPending, "":
		return nil
	}
	in := &tierBFlagInput{State: st.tbState, Diction: st.tbDiction, Adherence: st.tbAdherence}
	if r := st.tbRaw; r != nil {
		in.Words, in.RefWords = r.Counts.Words, r.Counts.RefWords
		if r.Code != nil {
			in.Code = *r.Code
		}
	}
	return in
}

// tierBFlags applies the ear checks of a listening check, in table order: diction_poor, alignment_failed, vocals_not_detected,
// listening_check_failed, melody_off_plan, sung_transposed. The flags are suspicions from a recogniser that can mishear sung
// words, never a verdict.
func tierBFlags(in *tierBFlagInput) []Flag {
	if in == nil {
		return nil
	}
	var out []Flag
	add := func(code, severity string, ev map[string]any) {
		out = append(out, Flag{Code: code, Severity: severity, Evidence: ev})
	}
	if d := in.Diction; d != nil {
		var worst *storedSection
		for i := range d.Sections {
			if s := &d.Sections[i]; s.RefWords >= dictionPoorWords && s.CER > dictionPoorCER && (worst == nil || s.CER > worst.CER) {
				worst = s
			}
		}
		if worst != nil {
			add("diction_poor", "warn", map[string]any{"section": worst.Label, "cer": worst.CER})
		}
		if d.Measured.Status == measuredFailed {
			add("alignment_failed", "info", map[string]any{"ref_words": d.Overall.RefWords})
		}
	}
	if in.State == TierBOK && in.Words <= noVocalsMaxWords && in.RefWords >= noVocalsMinLyric {
		add("vocals_not_detected", "info", map[string]any{"words": in.Words, "lyric_words": in.RefWords})
	}
	switch in.State {
	case TierBError, TierBSkipped, TierBMissing:
		ev := map[string]any{"state": in.State}
		if in.Code != "" {
			ev["code"] = in.Code
		}
		add("listening_check_failed", "info", ev)
	}
	if a := in.Adherence; a != nil {
		if a.MelodyChroma != nil && *a.MelodyChroma < melodyOffPlanMax && len(a.Sections) >= melodyOffMinSecs {
			add("melody_off_plan", "warn", map[string]any{"melody_chroma": *a.MelodyChroma, "sections": len(a.Sections)})
		}
		if n := transposedSections(a); n > 0 && 2*n >= len(a.Sections) {
			add("sung_transposed", "info", map[string]any{"transpose_mode": *a.TransposeMode, "sections": n})
		}
	}
	return out
}

// transposedSections counts the compared sections whose best transposition is not zero; 0 when the mode is zero or absent.
func transposedSections(a *TierBAdherence) int {
	if a.TransposeMode == nil || *a.TransposeMode == 0 {
		return 0
	}
	n := 0
	for _, s := range a.Sections {
		if s.Transpose != nil && *s.Transpose != 0 {
			n++
		}
	}
	return n
}
