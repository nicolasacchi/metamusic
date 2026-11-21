package generation

import "context"

// EarsSeams are the M5b Ears seams (C340.5, C345.6). generation never imports diction or ears: the app injects implementations through
// (*Service).UseEars, a setter because the Diction Lab is built after this service. A zero EarsSeams changes nothing.
type EarsSeams struct {
	// DictionRisk returns the diction_risk warnings of a take's lyrics (the words the personal lexicon says were often misheard,
	// C345.6). It is called once per planned take; an error leaves the warnings out (an estimate never fails for a warning).
	DictionRisk func(ctx context.Context, lyrics string) ([]Issue, error)
}

// UseEars wires the Ears seams.
func (s *Service) UseEars(e EarsSeams) { s.ears = e }
