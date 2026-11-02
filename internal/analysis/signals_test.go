package analysis

import "math"

const sigRate = 11025

// triadSignal is a I-IV-V-I progression of harmonic-rich triads (6 partials, 1/n amplitudes) in the key tonic (pitch
// class, C = 0) / minor, A4 = 440 Hz shifted by cents. Every chord lasts chordS seconds; the 4-chord cycle is rendered
// once and repeated to seconds. The tonic triad sits between A2 and G#3 so every partial of the cycle stays under 4 kHz.
func triadSignal(tonic int, minor bool, cents, seconds, chordS float64) []float32 {
	third := 4
	chords := [4][3]int{{0, third, 7}, {5, 5 + third, 12}, {7, 11, 14}, {0, third, 7}}
	if minor {
		third = 3
		chords = [4][3]int{{0, third, 7}, {5, 8, 12}, {7, 11, 14}, {0, third, 7}}
	}
	root := 45 + ((tonic-9)%12+12)%12 // A2 .. G#3
	chordN := int(chordS * sigRate)
	cycle := make([]float32, 4*chordN)
	fade := sigRate / 33
	for i := range cycle {
		pos := i % chordN
		env := 1.0
		if pos < fade {
			env = float64(pos) / float64(fade)
		} else if chordN-pos < fade {
			env = float64(chordN-pos) / float64(fade)
		}
		t := float64(i) / sigRate
		var v float64
		for _, semi := range chords[i/chordN] {
			f := 440 * math.Pow(2, (float64(root+semi-69)+cents/100)/12)
			for h := 1; h <= 6; h++ {
				v += math.Sin(2*math.Pi*f*float64(h)*t) / float64(h)
			}
		}
		cycle[i] = float32(0.08 * env * v)
	}
	x := make([]float32, int(seconds*sigRate))
	for i := range x {
		x[i] = cycle[i%len(cycle)]
	}
	return x
}

// sigRNG is a deterministic noise source in [-1, 1).
type sigRNG uint64

func (l *sigRNG) next() float64 {
	*l = *l*6364136223846793005 + 1442695040888963407
	return float64(uint64(*l)>>11)/float64(1<<53)*2 - 1
}

func whiteNoise(seconds, amp float64, seed uint64) []float32 {
	r := sigRNG(seed)
	x := make([]float32, int(seconds*sigRate))
	for i := range x {
		x[i] = float32(amp * r.next())
	}
	return x
}

// clickTrack is a noise burst (60 ms, exponentially decaying) every 1/perSec seconds on top of low-level noise.
func clickTrack(seconds, perSec, amp float64, seed uint64) []float32 {
	r := sigRNG(seed)
	x := make([]float32, int(seconds*sigRate))
	period := int(sigRate / perSec)
	burst := sigRate * 6 / 100
	for i := range x {
		v := 0.002 * r.next()
		if pos := i % period; pos < burst {
			v += amp * math.Exp(-float64(pos)/(0.012*sigRate)) * r.next()
		}
		x[i] = float32(v)
	}
	return x
}

// padSignal is a slow-swelling chord of sines (A3, E4, A4 and their 2nd to 4th partials at 1/n) at the given peak amplitude.
func padSignal(seconds, amp float64) []float32 {
	x := make([]float32, int(seconds*sigRate))
	for i := range x {
		t := float64(i) / sigRate
		env := 0.6 + 0.4*math.Sin(2*math.Pi*t/8)
		var v float64
		for _, f := range []float64{220, 330, 440} {
			for h := 1; h <= 4; h++ {
				v += math.Sin(2*math.Pi*f*float64(h)*t) / float64(h)
			}
		}
		x[i] = float32(amp * env * v / 6)
	}
	return x
}
