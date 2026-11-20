package abc

const (
	defaultVelocity = 80
	chordVelocity   = 60
	chordProgram    = 48
	chordVoiceID    = "chords"
	maxChannels     = 15 // 16 MIDI channels minus the percussion channel 9
)

// programFor resolves a voice's General MIDI program: %%MIDI program wins, else the options map by voice id, else 0.
func programFor(v Voice, o EventOptions) int {
	if v.Program != nil {
		return *v.Program
	}
	if p, ok := o.Programs[v.ID]; ok && p >= 0 && p <= 127 {
		return p
	}
	return 0
}

// assignChannels gives each voice its own channel in order, skipping 9; an explicit %%MIDI channel n (1-based) is used
// as written. extra is the number of synthetic voices that follow (the chord voice). It returns false when the voices do
// not fit.
func assignChannels(vs []Voice, extra int) ([]int, bool) {
	out := make([]int, len(vs)+extra)
	next := 0
	alloc := func() (int, bool) {
		if next == 9 {
			next++
		}
		if next > 15 {
			return 0, false
		}
		c := next
		next++
		return c, true
	}
	for i, v := range vs {
		if v.Channel != nil {
			out[i] = *v.Channel - 1
			continue
		}
		c, ok := alloc()
		if !ok {
			return nil, false
		}
		out[i] = c
	}
	for i := 0; i < extra; i++ {
		c, ok := alloc()
		if !ok {
			return nil, false
		}
		out[len(vs)+i] = c
	}
	return out, true
}
