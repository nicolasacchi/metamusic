package ir

import "math"

// Early reflections (C206.3): image sources of a shoebox of (size, 0.7 size, 0.4 size) metres, source at (0.3, 0.4, 0.3) and
// two receivers at (0.7 -+ 0.05 width, 0.6, 0.3) in room fractions, reflection order 1 and 2 (24 images; the direct sound
// is the dry path of the ensemble and is not part of the response), amplitude 0.8^order / r, fractional delay by a 16-tap
// windowed sinc, faded out by a raised cosine between erFadeStartS and erFadeEndS after the first arrival of the direct path.

const (
	speedOfSound = 343.0
	reflGain     = 0.8
	maxOrder     = 2
	sincTaps     = 16
	erFadeStartS = 0.040
	erFadeEndS   = 0.080
	minRadiusM   = 0.1
)

type image struct {
	order int
	// dist[0] and dist[1] are the path lengths to the left and right receiver.
	dist [2]float64
}

// imageAxis returns the coordinate of the n-th image of s along an axis of length l: even images are translations, odd ones
// mirrors; |n| is the number of wall reflections along the axis.
func imageAxis(n int, s, l float64) float64 {
	if n%2 == 0 {
		return float64(n)*l + s
	}
	return float64(n+1)*l - s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// images enumerates the image sources of order 1..maxOrder and their distances to both receivers.
func (p Params) images() (imgs []image, direct [2]float64) {
	dim := [3]float64{p.SizeM, 0.7 * p.SizeM, 0.4 * p.SizeM}
	src := [3]float64{0.3 * dim[0], 0.4 * dim[1], 0.3 * dim[2]}
	var rcv [2][3]float64
	for c := 0; c < 2; c++ {
		sign := float64(2*c - 1)
		rcv[c] = [3]float64{(0.7 + sign*0.05*p.Width) * dim[0], 0.6 * dim[1], 0.3 * dim[2]}
	}
	dist := func(pt [3]float64, c int) float64 {
		var s float64
		for a := 0; a < 3; a++ {
			d := pt[a] - rcv[c][a]
			s += float64(d * d)
		}
		return math.Max(minRadiusM, math.Sqrt(s))
	}
	direct = [2]float64{dist(src, 0), dist(src, 1)}
	for nx := -maxOrder; nx <= maxOrder; nx++ {
		for ny := -maxOrder; ny <= maxOrder; ny++ {
			for nz := -maxOrder; nz <= maxOrder; nz++ {
				order := abs(nx) + abs(ny) + abs(nz)
				if order == 0 || order > maxOrder {
					continue
				}
				pt := [3]float64{imageAxis(nx, src[0], dim[0]), imageAxis(ny, src[1], dim[1]), imageAxis(nz, src[2], dim[2])}
				imgs = append(imgs, image{order: order, dist: [2]float64{dist(pt, 0), dist(pt, 1)}})
			}
		}
	}
	return imgs, direct
}

// erFade is the raised-cosine fade-out applied to a reflection arriving excess seconds after the direct sound.
func erFade(excess float64) float64 {
	switch {
	case excess <= erFadeStartS:
		return 1
	case excess >= erFadeEndS:
		return 0
	}
	u := (excess - erFadeStartS) / (erFadeEndS - erFadeStartS)
	return 0.5 * (1 + math.Cos(math.Pi*u))
}

// addSinc adds amp * (a band-limited impulse at fractional position pos) to y.
func addSinc(y []float64, pos, amp float64) {
	n0 := int(math.Floor(pos))
	frac := pos - float64(n0)
	for k := -sincTaps/2 + 1; k <= sincTaps/2; k++ {
		x := float64(k) - frac
		w := 0.5 * (1 + math.Cos(math.Pi*x/float64(sincTaps/2)))
		s := 1.0
		if x != 0 {
			s = math.Sin(math.Pi*x) / (math.Pi * x)
		}
		if i := n0 + k; i >= 0 && i < len(y) {
			y[i] += float64(amp * float64(s*w))
		}
	}
}

// earlyReflections returns the early part of both channels, long enough for the last fade plus the sinc support. Delays are
// the predelay plus the excess path over the direct sound of the same receiver.
func earlyReflections(p Params, rate int) (l, r []float64) {
	imgs, direct := p.images()
	n := int(math.Ceil((p.PredelayMS/1000+erFadeEndS)*float64(rate))) + sincTaps
	out := [2][]float64{make([]float64, n), make([]float64, n)}
	for _, im := range imgs {
		for c := 0; c < 2; c++ {
			excess := (im.dist[c] - direct[c]) / speedOfSound
			g := erFade(excess)
			if g == 0 {
				continue
			}
			amp := float64(g * powInt(reflGain, im.order) / im.dist[c])
			addSinc(out[c], (p.PredelayMS/1000+excess)*float64(rate), amp)
		}
	}
	return out[0], out[1]
}

func powInt(x float64, n int) float64 {
	r := 1.0
	for ; n > 0; n-- {
		r *= x
	}
	return r
}
