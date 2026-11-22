package spend

import (
	"errors"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// Pod timing constants (C393.1): the grace after the batch deadline before the overrun trip, and one pass of the
// lifecycle loop.
const PodOverrunGraceS, PodPassS = 300, 30

// ErrPodRuntime means the batch cannot finish inside MM_POD_MAX_RUNTIME.
var ErrPodRuntime = errors.New("spend: pod batch exceeds MM_POD_MAX_RUNTIME")

// ErrPodNoTakes means a pod quote needs at least one take.
var ErrPodNoTakes = errors.New("spend: pod quote needs at least one take")

// PodQuoteParams are the inputs of a pod batch quote (C393.1).
type PodQuoteParams struct {
	// Base carries the price (WithMarginPPM of the maximum price.secure over the allowed GPU types), the RTF, the kill slack
	// and the per-take runtime cap. ColdStartS and IdleS are ignored (the pod's boot and idle are pod terms) and WorkersMax is 1.
	Base QuoteParams
	// BootS, IdleS, SlackS and OverrunS (PodOverrunGraceS + PodPassS) make one pod's overhead; MaxRuntimeS bounds the window;
	// MaxRecreate is the number of re-creations the ceiling pays for.
	BootS, IdleS, SlackS, OverrunS, MaxRuntimeS, MaxRecreate int
}

// PodQuote is the quote of one pod batch.
type PodQuote struct {
	// Batch is the shape an estimate stores, with AttemptOverhead 0: the pod overhead is a separate reserve.
	Batch            BatchQuote
	PodOverheadS     int
	PodOverheadMicro money.MicroUSD
	// DeadlineS is the absolute batch deadline from the first create, with one retry inside the window.
	DeadlineS int
}

// QuotePod quotes takes on a pod, integer arithmetic only (C393.1). With t_i the take timeouts, K the kill slack,
// T = Σ t_i, Tmax = max t_i, O = BootS + IdleS + SlackS + K + OverrunS and F = ForSeconds(·, PricePPH):
//
//	take_i   = F(t_i + K)           max      = Σ take_i + F(O)
//	retry    = F(Tmax + K) + MaxRecreate × F(O)     ceiling = max + retry
//	expected = Σ F(min(exp_i, t_i)) + F(BootS + IdleS)
//	deadline = BootS + T + Tmax + IdleS + SlackS    (> MaxRuntimeS: ErrPodRuntime)
func QuotePod(p PodQuoteParams, takes []TakeIn) (PodQuote, error) {
	if len(takes) == 0 {
		return PodQuote{}, ErrPodNoTakes
	}
	base := p.Base
	base.ColdStartS, base.IdleS, base.WorkersMax = 0, 0, 1
	q := QuoteTakes(base, takes)
	var total, tmax, sumExp int
	var maxTake money.MicroUSD
	for _, t := range q.Takes {
		total += t.TimeoutS
		tmax = max(tmax, t.TimeoutS)
		sumExp += t.ExpectedS
		maxTake = max(maxTake, t.TakeMicro)
	}
	price := base.PricePPH
	overheadS := p.BootS + p.IdleS + p.SlackS + base.KillSlackS + p.OverrunS
	overhead := money.ForSeconds(int64(overheadS), price)
	deadline := p.BootS + total + tmax + p.IdleS + p.SlackS
	if deadline > p.MaxRuntimeS {
		return PodQuote{}, ErrPodRuntime
	}
	q.MaxMicro += overhead // the per-take MaxMicro stays the take reserve: the overhead is a pod-level term
	q.RetryAllowanceMicro = maxTake + money.MicroUSD(max(p.MaxRecreate, 0))*overhead
	q.CeilingMicro = q.MaxMicro + q.RetryAllowanceMicro
	q.ExpectedMicro += money.ForSeconds(int64(p.BootS+p.IdleS), price)
	q.ETAS = p.BootS + sumExp
	return PodQuote{Batch: q, PodOverheadS: overheadS, PodOverheadMicro: overhead, DeadlineS: deadline}, nil
}
