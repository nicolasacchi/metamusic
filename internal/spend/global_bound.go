package spend

import "github.com/nicolasacchi/metamusic/internal/money"

// ComputeGlobalBound is the pure arithmetic of C151.5 / D-E2-30 over the backends' live inputs:
//
//	G = sum workers_max x price                                        (micro-USD per hour, the flat-out burn rate)
//	X = sum workers_max x ForSeconds(max_runtime + cold + kill_slack)  (the worst simultaneous exposure)
//
// A backend with workers_max <= 0 (inactive or disarmed) contributes nothing and is not listed. Callers pass non-negative
// prices and runtimes; negative values are floored at zero only so that the function never panics.
func ComputeGlobalBound(bs []BackendBound) GlobalBound {
	var g GlobalBound
	for _, b := range bs {
		if b.WorkersMax <= 0 {
			continue
		}
		w := money.MicroUSD(b.WorkersMax)
		price := max(b.PricePPH, 0)
		g.GMicroPerHour += w * price
		g.XMicro += w * money.ForSeconds(int64(max(b.MaxRuntimeS+b.ColdStartS+b.KillSlackS, 0)), price)
		g.Backends = append(g.Backends, b.Backend)
	}
	return g
}
