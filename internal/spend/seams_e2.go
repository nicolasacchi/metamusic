package spend

import "github.com/nicolasacchi/metamusic/internal/money"

// E2 seam types (C152, C151.5). The bodies live in engine_rtf.go and global_bound.go.

// EngineRTFRow is one measured or seeded real-time factor of (engine, task class, gpu class); LoadS is the class's fixed
// seconds (engine_rtf.fixed_s).
type EngineRTFRow struct {
	Engine, TaskClass, GPUClass string
	RTFMilli, LoadS, Samples    int
	Source                      string // "measured" | "seed"
}

// BackendBound is one backend's contribution to the global worker bound (C151.5).
type BackendBound struct {
	Backend                             string
	WorkersMax                          int            // the live workers.max of the endpoint (0 while inactive or disarmed)
	PricePPH                            money.MicroUSD // list price with margin
	MaxRuntimeS, ColdStartS, KillSlackS int
}

// GlobalBound is the burn rate G = sum workers_max x price (micro-USD per hour) and the worst simultaneous exposure
// X = sum workers_max x ForSeconds(max_runtime + cold + kill_slack) of all backends.
type GlobalBound struct {
	GMicroPerHour, XMicro money.MicroUSD
	Backends              []string // the backends with workers_max > 0
}
