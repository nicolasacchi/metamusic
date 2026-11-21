package spend

// Tier B (the listening check) terms of the quote (M5b Ears, C339.1, D-EARS-3). The zero TakeIn.EarsS leaves every quote
// byte-identical.

// EarsExpectedS is the expected Tier B seconds of a take: load + ceil(rtf × audio / 1000), plus ceil(trRTF × audio / 1000) when the
// transcribe stage (the vocal pitch contour) runs. Integer arithmetic only.
func EarsExpectedS(audioS, loadS, rtfMilli, trRTFMilli int, transcribe bool) int {
	s := int64(loadS) + ceilDiv(int64(rtfMilli)*int64(audioS), 1000)
	if transcribe {
		s += ceilDiv(int64(trRTFMilli)*int64(audioS), 1000)
	}
	return int(s)
}

// EarsBudgetS is the worker's budget for the stage, ceil(3 × earsS / 2).
func EarsBudgetS(earsS int) int { return int(ceilDiv(3*int64(earsS), 2)) }

// TierBFits reports whether the take's Tier B fits the runtime cap: the /1 timeout was not clamped by MaxRuntimeS and the /1 timeout
// plus the Tier B budget stays within it. A take that does not fit cannot have a listening check.
func TierBFits(p QuoteParams, t TakeIn) bool {
	_, to := baseTimes(p, t)
	return to <= int64(p.MaxRuntimeS) && to+int64(EarsBudgetS(t.EarsS)) <= int64(p.MaxRuntimeS)
}
