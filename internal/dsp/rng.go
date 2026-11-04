package dsp

import (
	"math"
	"math/rand/v2"
)

// Randomness comes only from math/rand/v2's NewPCG and Uint64, with our own conversions (D-T1-3b): the method algorithms of
// rand/v2 (Float64, NormFloat64, ...) are not a stability promise and are not used. This is the one file allowed to import
// math/rand (TestDSP_RandOnlyPCG).

// Rand is a deterministic random source.
type Rand struct{ p *rand.PCG }

// NewRand returns a source seeded by seed. The PCG's second word is a fixed odd mix of the seed, so equal seeds give equal
// streams on every host.
func NewRand(seed uint64) *Rand {
	return &Rand{p: rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)}
}

// Uint64 returns the next 64 random bits.
func (r *Rand) Uint64() uint64 { return r.p.Uint64() }

// Float64 returns a value in [0, 1): float64(u>>11) * 0x1p-53.
func (r *Rand) Float64() float64 { return float64(r.Uint64()>>11) * 0x1p-53 }

// Norm returns a standard normal value by our own Box-Muller on dsp.Log, Sqrt and Cos.
func (r *Rand) Norm() float64 {
	u1 := 1 - r.Float64() // (0, 1]
	u2 := r.Float64()
	return math.Sqrt(-2*Log(u1)) * math.Cos(2*math.Pi*u2)
}
