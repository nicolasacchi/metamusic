package ops

func init() {
	// a quiet input: the safety finish must lift it to the target, so the dispatcher's output differs from the operator's
	d5Jobs[KindScoreRender] = d5Fixture{params: `{"score":{"abc":"X:1\nK:C\nCDEF|"}}`, gain: 0.25}
}
