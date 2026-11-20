package morning

import "math"

// Slate geometry. A slate is the audible chapter number between takes: a
// one second tone followed by half a second of silence.
const (
	SampleRate    = 48000
	slateToneMS   = 1000
	slateSilentMS = 500
	slateFadeMS   = 20
	slateLevel    = 0.1 // -20 dBFS
	pcm24Max      = 8388607
	// SlateSamples is the length of one slate in frames.
	SlateSamples = (slateToneMS + slateSilentMS) * SampleRate / 1000
)

// SlateFreq is the tone frequency of slate n (1-based): 440 Hz stepped up one
// semitone per chapter, wrapping every twelve.
func SlateFreq(n int) float64 {
	k := ((n-1)%12 + 12) % 12
	return 440 * math.Pow(2, float64(k)/12)
}

// SlatePCM renders slate n as 48 kHz stereo signed 24-bit little-endian PCM:
// a 1.0 s sine at -20 dBFS with 20 ms raised-cosine fades, then 0.5 s of
// zeros. The result is SlateSamples*6 bytes.
func SlatePCM(n int) []byte {
	out := make([]byte, SlateSamples*6)
	tone := slateToneMS * SampleRate / 1000
	fade := slateFadeMS * SampleRate / 1000
	f := SlateFreq(n)
	for i := 0; i < tone; i++ {
		env := 1.0
		switch {
		case i < fade:
			env = 0.5 * (1 - math.Cos(math.Pi*float64(i)/float64(fade)))
		case i >= tone-fade:
			env = 0.5 * (1 - math.Cos(math.Pi*float64(tone-1-i)/float64(fade)))
		}
		v := int32(math.Round(slateLevel * env * pcm24Max * math.Sin(2*math.Pi*f*float64(i)/SampleRate)))
		b0, b1, b2 := byte(v), byte(v>>8), byte(v>>16) // #nosec G115 -- little-endian 24-bit PCM: truncation to the low bytes is the encoding
		o := i * 6
		out[o], out[o+1], out[o+2] = b0, b1, b2
		out[o+3], out[o+4], out[o+5] = b0, b1, b2
	}
	return out
}
