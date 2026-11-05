package spend

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// E2 seams (C152, C151.5, D-E2-30, D-E2-32). The signatures are frozen by the contract freeze (E2-F2); lane E2-SP fills the
// bodies in this file's successors (engine_rtf.go, global_bound.go). Callers in other lanes (generation: EngineRTF;
// jobs ingest: RecordEngineRTF; the provider ops: GlobalBound) compile against these today.

// ErrNotImplemented marks a seam whose body has not landed; callers treat it as "unavailable", never as zero.
var ErrNotImplemented = errors.New("spend: not implemented yet")

// EngineRTFRow is one measured or seeded real-time factor of (engine, task class, gpu class); LoadS is the class's fixed
// seconds (engine_rtf.fixed_s).
type EngineRTFRow struct {
	Engine, TaskClass, GPUClass string
	RTFMilli, LoadS, Samples    int
	Source                      string // "measured" | "seed"
}

// EngineRTF is the maximum rtf_milli and load_s over the allowed GPU classes of a backend's pools: one class's value is its
// engine_rtf row if present, else the committed seed (internal/spend/engine_rtf.seed.json), else ok is false and the
// caller answers 503 GENERATION_DISABLED reason no_rtf. yue2 keeps RTFMilli over rtf_stats and never calls this.
//
// STUB(E2-SP)
func (s *Service) EngineRTF(ctx context.Context, q Querier, engine, class string, gpuClasses []string) (rtfMilli, loadS int, ok bool, err error) {
	return 0, 0, false, ErrNotImplemented
}

// EngineRTFRows lists the rows list_engines and get_provider_status show for an engine (measured rows over seeds).
//
// STUB(E2-SP)
func (s *Service) EngineRTFRows(ctx context.Context, q Querier, engine string) ([]EngineRTFRow, error) {
	return nil, ErrNotImplemented
}

// RecordEngineRTF is the EWMA step of NextRTF on e2e = generate_s + save_s - load_s for the class, written with
// updated_by (job:<id> or canary:<id>); ingest calls it inside the take transaction for engines whose IngestPolicy
// LearnsRTF is false and whose task is a full-length render of an enabled task of the class (C152 learning rule).
//
// STUB(E2-SP)
func (s *Service) RecordEngineRTF(ctx context.Context, tx *sql.Tx, engine, class, gpuClass, by string, e2eMS, audioMS int64) error {
	return ErrNotImplemented
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

// ComputeGlobalBound is pure arithmetic over the backends' inputs.
//
// STUB(E2-SP)
func ComputeGlobalBound(bs []BackendBound) GlobalBound {
	panic("STUB(E2-SP): spend.ComputeGlobalBound")
}
