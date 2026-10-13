package analysis

import "fmt"

// BlindSpotFacts are the inputs of BlindSpots (C51.3).
type BlindSpotFacts struct {
	TierA         bool     // every applicable Tier A analyzer present
	HasSemantic   bool     // the take has a semantic.npy artifact
	Timed         bool     // section times exist
	SectionSource string   // "plan" | "measured" | "manual" | "" (untimed)
	Confidence    *float64 // confidence of the section times, when known
	Spectrogram   bool     // the payload is or embeds a spectrogram view
	Lossy         bool     // the analysed master is (or, for a comparison, includes) an MP3 decode
}

// BlindSpots is the one list of things the machine cannot judge (D-M2b-21): it
// feeds every Ears payload and /context perception.blind_spots. The six fixed
// entries come first (the first five are M2a C35's wording), then the
// conditional ones in a fixed order.
func BlindSpots(f BlindSpotFacts) []string {
	out := []string{
		"timbre",
		"emotional delivery",
		"mix balance beyond loudness",
		"hook catchiness",
		"diction and intelligibility",
		"melody and harmony adherence to the score",
	}
	if f.Lossy {
		out = append(out, "analysed from a lossy MP3 decode: bandwidth, clipping and true peak are not comparable with FLAC takes")
	}
	if !f.TierA {
		out = append(out, "silence, clipping and token loops (Tier A not run yet)")
	} else if !f.HasSemantic {
		out = append(out, "token loops (no semantic.npy for this take)")
	}
	switch {
	case f.Timed && f.SectionSource == "plan":
		if f.Confidence != nil {
			out = append(out, fmt.Sprintf("section times are score-plan estimates (confidence %.1f)", *f.Confidence))
		} else {
			out = append(out, "section times are score-plan estimates")
		}
	case !f.Timed:
		out = append(out, "section times unknown (no aligned score)")
	}
	if f.Spectrogram {
		out = append(out, "a spectrogram shows energy over time and frequency, not pitch accuracy or words")
	}
	return out
}
