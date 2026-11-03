// Package dsp is the pure-Go sound-processing core of the T1 transforms (C203): planar float64 buffers, an own FFT with
// precomputed twiddles, deterministic Exp/Log/Pow, a PCG wrapper, a work meter, the resampler and the convolution.
//
// It imports nothing of the application (TestImportGraph_T1): the standard library, the exported analysis.OnsetEnvelope only
// (D-T1-20) and the dsp subtree. No operator starts a goroutine and none reads a clock (D-T1-3). T1-F1 froze the signatures
// as stubs; T1-D1 implements them.
package dsp
