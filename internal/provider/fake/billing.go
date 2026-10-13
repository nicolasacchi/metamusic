package fake

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ceilSecs is the number of started seconds of d.
func ceilSecs(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

// costSecs prices `secs` seconds of a lifetime that began at start, honouring a list-price rise.
func (b *Backend) costSecs(start time.Time, secs int64) money.MicroUSD {
	p0 := b.o.PricePPH
	r := b.o.Adversarial.PriceRiseAt
	if r == nil || secs == 0 {
		return money.ForSeconds(secs, p0)
	}
	before := min(max(ceilSecs(r.Sub(start)), 0), secs)
	p1 := p0 * priceRiseNum / priceRiseDen
	return money.ForSeconds(before, p0) + money.ForSeconds(secs-before, p1)
}

// lifeEnd is the end of the worker's billed lifetime as of x.
func lifeEnd(w *worker, x time.Time) time.Time {
	if !w.stop.IsZero() && w.stop.Before(x) {
		return w.stop
	}
	return x
}

// lifeCost is what the worker has cost from its start until x: every started second, cold start,
// execution and idle alike.
func (b *Backend) lifeCost(w *worker, x time.Time) money.MicroUSD {
	end := lifeEnd(w, x)
	if !end.After(w.start) {
		return 0
	}
	return b.costSecs(w.start, ceilSecs(end.Sub(w.start)))
}

// AccruedCost is the ground-truth cost of every simulated worker lifetime up to t (C86.3): the sum over
// workers of their started seconds at the list price in force. The reality check compares it with what
// the app has settled and reserved.
func (b *Backend) AccruedCost(t time.Time) money.MicroUSD {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.After(b.now) {
		b.advance(context.Background(), t)
	}
	var sum money.MicroUSD
	for _, w := range b.workers {
		sum += b.lifeCost(w, t)
	}
	return sum
}

// AccruedCostByBatch attributes accrued cost to batches (C86.3): a worker's cold start belongs to the
// batch of the job that caused it, and from the moment it begins executing a job everything up to the
// next job (execution and idle tail) belongs to that job's batch. The values sum to AccruedCost(t).
func (b *Backend) AccruedCostByBatch(t time.Time) map[string]money.MicroUSD {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.After(b.now) {
		b.advance(context.Background(), t)
	}
	out := map[string]money.MicroUSD{}
	for _, w := range b.workers {
		end := lifeEnd(w, t)
		if !end.After(w.start) {
			continue
		}
		owner := w.creatorBatch
		var prevCost money.MicroUSD
		prev := w.start
		for _, s := range w.serves {
			at := s.from
			if at.After(end) {
				break
			}
			if at.After(prev) {
				c := b.lifeCost(w, at)
				out[owner] += c - prevCost
				prevCost, prev = c, at
			}
			owner = s.batch
		}
		out[owner] += b.lifeCost(w, t) - prevCost
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}

// bucketCost is the cost accrued inside [from, to): the overlap of every worker's billed interval,
// summed first and rounded up once per price segment.
func (b *Backend) bucketCost(from, to, now time.Time) money.MicroUSD {
	if to.After(now) {
		to = now
	}
	var msBefore, msAfter int64
	for _, w := range b.workers {
		end := lifeEnd(w, now)
		if !end.After(w.start) {
			continue
		}
		end = w.start.Add(time.Duration(ceilSecs(end.Sub(w.start))) * time.Second)
		lo, hi := maxTime(w.start, from), minTime(end, to)
		if !hi.After(lo) {
			continue
		}
		if r := b.o.Adversarial.PriceRiseAt; r != nil {
			mid := minTime(maxTime(*r, lo), hi)
			msBefore += mid.Sub(lo).Milliseconds()
			msAfter += hi.Sub(mid).Milliseconds()
			continue
		}
		msBefore += hi.Sub(lo).Milliseconds()
	}
	total := money.ForMillis(msBefore, b.o.PricePPH)
	if msAfter > 0 {
		total += money.ForMillis(msAfter, b.o.PricePPH*priceRiseNum/priceRiseDen)
	}
	return total
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Billing implements provider.Backend: hourly buckets (UTC hours), each rounded up, optionally revised
// upwards later (Adversarial.RevisedBucketsAfter). Hours without cost are omitted.
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	b.n.billing.Add(1)
	if b.revoked() {
		return nil, provider.ErrAccess
	}
	if b.o.Permissions.BillingForbidden {
		return nil, provider.ErrNotPermitted
	}
	now := b.enter(ctx)
	defer b.mu.Unlock()
	var out []provider.BillBucket
	for h := from.UTC().Truncate(time.Hour); h.Before(to); h = h.Add(time.Hour) {
		end := h.Add(time.Hour)
		total := b.bucketCost(h, end, now)
		if total == 0 {
			continue
		}
		total = b.revised(total, end, now)
		out = append(out, provider.BillBucket{Start: h, End: end, Total: total, Compute: total})
	}
	return out, nil
}
