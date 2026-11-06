// Package filt holds the second-order filters of the hi-fi transforms (C219, C220): RBJ high-pass, low-pass and shelving
// sections from closed forms, and the Linkwitz-Riley 4th-order cascades. Coefficients use Sin, Cos and Sqrt of the standard
// library and dsp.Pow only; products are written float64(a*b) so no host fuses a multiply-add (D-T1-3).
package filt

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// chunk is the metering granularity of Process, so a cancelled context is seen every few thousand samples.
const chunk = 4096

// unitsPerSample is the work-unit price of one section on one sample (five multiply-adds).
const unitsPerSample = 5

// Biquad is a second-order section with a0 normalised to 1, run in transposed direct form II.
type Biquad struct{ B0, B1, B2, A1, A2 float64 }

func norm(b0, b1, b2, a0, a1, a2 float64) Biquad {
	return Biquad{B0: b0 / a0, B1: b1 / a0, B2: b2 / a0, A1: a1 / a0, A2: a2 / a0}
}

// Process filters x and returns a new slice of the same length. It spends five units per sample on m.
func (q Biquad) Process(x []float64, m *dsp.Meter) ([]float64, error) {
	y := make([]float64, len(x))
	var z1, z2 float64
	for start := 0; start < len(x); start += chunk {
		end := min(start+chunk, len(x))
		if err := m.Add(int64(end-start) * unitsPerSample); err != nil {
			return nil, err
		}
		for i := start; i < end; i++ {
			v := x[i]
			o := float64(q.B0*v) + z1
			z1 = float64(q.B1*v) - float64(q.A1*o) + z2
			z2 = float64(q.B2*v) - float64(q.A2*o)
			y[i] = o
		}
	}
	return y, nil
}

// Mag is the analytic magnitude response at frequency f for sample rate rate.
func (q Biquad) Mag(rate int, f float64) float64 {
	w := 2 * math.Pi * f / float64(rate)
	c1, s1 := math.Cos(w), math.Sin(w)
	c2, s2 := math.Cos(2*w), math.Sin(2*w)
	nr := q.B0 + float64(q.B1*c1) + float64(q.B2*c2)
	ni := -float64(q.B1*s1) - float64(q.B2*s2)
	dr := 1 + float64(q.A1*c1) + float64(q.A2*c2)
	di := -float64(q.A1*s1) - float64(q.A2*s2)
	return math.Sqrt((float64(nr*nr) + float64(ni*ni)) / (float64(dr*dr) + float64(di*di)))
}

// Butterworth is 1/sqrt(2), the Q of a maximally flat second-order section.
const Butterworth = 0.7071067811865476

func omega(rate int, f0 float64) (cos, sin float64) {
	w := 2 * math.Pi * f0 / float64(rate)
	return math.Cos(w), math.Sin(w)
}

// HighPass is the RBJ high-pass at f0 with quality q.
func HighPass(rate int, f0, q float64) Biquad {
	c, s := omega(rate, f0)
	al := s / (2 * q)
	return norm((1+c)/2, -(1 + c), (1+c)/2, 1+al, -2*c, 1-al)
}

// LowPass is the RBJ low-pass at f0 with quality q.
func LowPass(rate int, f0, q float64) Biquad {
	c, s := omega(rate, f0)
	al := s / (2 * q)
	return norm((1-c)/2, 1-c, (1-c)/2, 1+al, -2*c, 1-al)
}

// shelfTerms returns A, cos(w0) and 2*sqrt(A)*alpha of the RBJ shelving filters for gain gainDB and slope s.
func shelfTerms(rate int, f0, gainDB, slope float64) (a, c, k float64) {
	a = dsp.Pow(10, gainDB/40)
	var s float64
	c, s = omega(rate, f0)
	al := float64(s/2) * math.Sqrt(float64((a+1/a)*(1/slope-1))+2)
	return a, c, float64(2 * math.Sqrt(a) * al)
}

// LowShelf is the RBJ low shelf: gain gainDB below f0, slope s (1 is the steepest monotonic shelf).
func LowShelf(rate int, f0, gainDB, slope float64) Biquad {
	a, c, k := shelfTerms(rate, f0, gainDB, slope)
	return norm(
		float64(a*((a+1)-float64((a-1)*c)+k)),
		float64(2*a*((a-1)-float64((a+1)*c))),
		float64(a*((a+1)-float64((a-1)*c)-k)),
		(a+1)+float64((a-1)*c)+k,
		-2*((a-1)+float64((a+1)*c)),
		(a+1)+float64((a-1)*c)-k)
}

// HighShelf is the RBJ high shelf: gain gainDB above f0, slope s.
func HighShelf(rate int, f0, gainDB, slope float64) Biquad {
	a, c, k := shelfTerms(rate, f0, gainDB, slope)
	return norm(
		float64(a*((a+1)+float64((a-1)*c)+k)),
		float64(-2*a*((a-1)+float64((a+1)*c))),
		float64(a*((a+1)+float64((a-1)*c)-k)),
		(a+1)-float64((a-1)*c)+k,
		2*((a-1)-float64((a+1)*c)),
		(a+1)-float64((a-1)*c)-k)
}

// Cascade is sections run one after the other.
type Cascade []Biquad

// Process runs every section in order.
func (c Cascade) Process(x []float64, m *dsp.Meter) ([]float64, error) {
	var err error
	for _, q := range c {
		if x, err = q.Process(x, m); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// Mag is the product of the sections' magnitudes.
func (c Cascade) Mag(rate int, f float64) float64 {
	g := 1.0
	for _, q := range c {
		g = float64(g * q.Mag(rate, f))
	}
	return g
}

// LR4HighPass is the 4th-order Linkwitz-Riley high-pass at f0: two cascaded Butterworth sections, -6 dB at f0.
func LR4HighPass(rate int, f0 float64) Cascade {
	q := HighPass(rate, f0, Butterworth)
	return Cascade{q, q}
}

// LR4LowPass is the matching low-pass; its sum with LR4HighPass has a flat magnitude.
func LR4LowPass(rate int, f0 float64) Cascade {
	q := LowPass(rate, f0, Butterworth)
	return Cascade{q, q}
}
