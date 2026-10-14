package fake

import (
	"hash/fnv"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// Constants of the adversarial profile (C86.3 b).
const (
	// priceRiseNum/Den: the list price after PriceRiseAt is 3/2 of the base (above any margin).
	priceRiseNum, priceRiseDen = 3, 2
	// revisionNum/Den: a bucket revised by RevisedBucketsAfter is billed 11/10 plus one micro-USD.
	revisionNum, revisionDen = 11, 10
	// resultRetention is how long RunPod keeps job results (PurgeResultsAfter30m).
	resultRetention = 30 * time.Minute
)

// priceAt is the list price (per hour) in force at t.
func (b *Backend) priceAt(t time.Time) money.MicroUSD {
	if b.o.Adversarial.PriceRiseAt != nil && !t.Before(*b.o.Adversarial.PriceRiseAt) {
		return b.o.PricePPH * priceRiseNum / priceRiseDen
	}
	return b.o.PricePPH
}

// revised applies the late adjustment to a bucket whose end is long enough ago.
func (b *Backend) revised(total money.MicroUSD, bucketEnd, now time.Time) money.MicroUSD {
	d := b.o.Adversarial.RevisedBucketsAfter
	if d <= 0 || now.Before(bucketEnd.Add(d)) || total == 0 {
		return total
	}
	return total*revisionNum/revisionDen + 1
}

// acceptThenFail reports whether this submit is one of the fraction that fail after the job was accepted.
// It is a pure function of the job and attempt, so runs are reproducible.
func (b *Backend) acceptThenFail(jobID, attemptID string) bool {
	f := b.o.Adversarial.AcceptThenFailRun
	if f <= 0 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(jobID + "/" + attemptID))
	return float64(h.Sum32()%10000)/10000 < f
}

// applyDrift moves the endpoint away from its desired values once the first job was submitted ("raised
// mid-batch"); Sweep reports the difference and RevertDrift undoes it.
func (b *Backend) applyDrift() {
	if b.drifted {
		return
	}
	b.drifted = true
	if n := b.o.Adversarial.IdleDriftTo; n > 0 {
		b.idleS = n
	}
	if n := b.o.Adversarial.MaxDriftTo; n > 0 {
		b.maxW = n
	}
}

func (b *Backend) driftReport() (out []driftField) {
	if b.maxW != b.desiredMax {
		out = append(out, driftField{"workers.max", strconv.Itoa(b.desiredMax), strconv.Itoa(b.maxW)})
	}
	if b.idleS != b.desiredIdle {
		out = append(out, driftField{"workers.idleTimeout", strconv.Itoa(b.desiredIdle), strconv.Itoa(b.idleS)})
	}
	return out
}

type driftField struct{ field, want, got string }
