// Package align is the alignment maths of the T1 transforms (C204): the tempo ramp, the key shift, the octave fold and
// the local beat phase and tempo measured on analysis.OnsetEnvelope (D-T1-9, D-T1-20).
//
// Like the rest of internal/dsp it is deterministic (D-T1-3): float64 throughout, no goroutine, no clock, and only the
// tree's own dsp.Exp/Log/Pow instead of the banned math functions (TestDSP_BannedMath scans this package too). The one
// exception to "own maths" is analysis.OnsetEnvelope, which calls math.Log1p from outside the tree; T1-X0 found it pure Go
// on the pinned targets and TestOnset_PinnedForT1 pins its output, so a later analysis change fails loudly and forces a
// dsp_version bump.
package align
