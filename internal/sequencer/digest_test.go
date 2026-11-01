package sequencer

import "testing"

func TestDigest_Canonical(t *testing.T) {
	t.Parallel()
	o := Options{Strategy: StrategyArc, First: "t1"}
	base := Digest(tSet(), o)
	if len(base) != 64 || base != Digest(tSet(), o) {
		t.Fatalf("digest = %q", base)
	}
	mutate := map[string]func([]Song, *Options){
		"strategy": func(_ []Song, o *Options) { o.Strategy = StrategySteady },
		"first":    func(_ []Song, o *Options) { o.First = "t2" },
		"last":     func(_ []Song, o *Options) { o.Last = "t2" },
		"id":       func(s []Song, _ *Options) { s[0].ID = "x" },
		"bpm":      func(s []Song, _ *Options) { s[0].BPMx100++ },
		"energy":   func(s []Song, _ *Options) { s[0].Energyx10000++ },
		"key num":  func(s []Song, _ *Options) { s[0].Key = &Camelot{1, 'A'} },
		"key let":  func(s []Song, _ *Options) { s[0].Key = &Camelot{s[0].Key.Num, 'B'} },
		"key nil":  func(s []Song, _ *Options) { s[0].Key = nil },
		"basis":    func(s []Song, _ *Options) { s[0].Bases.Energy = "predicted" },
		"basis k":  func(s []Song, _ *Options) { s[0].Bases.Key = "declared" },
		"basis b":  func(s []Song, _ *Options) { s[0].Bases.BPM = "plan" },
		"order":    func(s []Song, _ *Options) { s[0], s[1] = s[1], s[0] },
		"drop":     func(s []Song, _ *Options) { s[5] = s[4] },
	}
	for name, f := range mutate {
		s, o2 := tSet(), o
		f(s, &o2)
		if Digest(s, o2) == base {
			t.Errorf("%s change leaves the digest unchanged", name)
		}
	}
	// Field boundaries are unambiguous.
	a := []Song{{ID: "ab", Bases: Bases{BPM: "c"}}}
	b := []Song{{ID: "a", Bases: Bases{BPM: "bc"}}}
	if Digest(a, Options{}) == Digest(b, Options{}) {
		t.Error("ambiguous encoding")
	}
	if Digest(nil, Options{}) == Digest([]Song{{}}, Options{}) {
		t.Error("empty and one blank song collide")
	}
	// First/Last are not interchangeable.
	if Digest(nil, Options{First: "a"}) == Digest(nil, Options{Last: "a"}) {
		t.Error("first and last collide")
	}
}
