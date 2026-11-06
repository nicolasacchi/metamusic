package ops

// The dispatcher suites (run_test.go) need one fixed job per registered kind: binaural adds its row here. It self-finishes
// (selfFinishing in units.go), so the dispatcher must not run safety.Finish on it again.
func init() {
	d5Jobs[KindBinaural] = d5Fixture{params: `{"model":"hrtf","preset":"studio","room":"studio","tail_s":0.5}`}
}

// The units row of binaural: a 30 s input at 48 kHz, the default studio room (tail_s 1), the output the service would estimate.
func init() {
	d5UnitCases = append(d5UnitCases, d5UnitCase{kind: KindBinaural, in: sec30, out: sec30 + 48000, params: `{}`, want: 3511645596})
}
