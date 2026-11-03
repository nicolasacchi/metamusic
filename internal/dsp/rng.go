package dsp

// STUB(T1-D1): the PCG wrapper of D-T1-3(b). Randomness comes only from math/rand/v2's NewPCG and Uint64, with our own
// conversion (float64(u>>11) * 0x1p-53); this is the one file allowed to import math/rand.

// Rand is a deterministic random source.
type Rand struct{ seed uint64 }

// NewRand returns a source seeded by seed.
func NewRand(seed uint64) *Rand { return &Rand{seed: seed} }

// Uint64 returns the next 64 random bits.
func (r *Rand) Uint64() uint64 { panic("STUB(T1-D1): dsp.Rand.Uint64") }

// Float64 returns a value in [0, 1): float64(u>>11) * 0x1p-53.
func (r *Rand) Float64() float64 { panic("STUB(T1-D1): dsp.Rand.Float64") }

// Norm returns a standard normal value by our own Box-Muller on dsp.Log, Sqrt and Cos.
func (r *Rand) Norm() float64 { panic("STUB(T1-D1): dsp.Rand.Norm") }
