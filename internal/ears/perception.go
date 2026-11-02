package ears

import "context"

// Perception builds the /context perception block (C51.8). The loudness, bandwidth and section
// values are computed exactly as M2a's trackctx computed them: from the track detail's
// last-row analyses and its selected sections.
func (s *Service) Perception(ctx context.Context, trackID string) (Perception, error) {
	st, err := s.load(ctx, trackID)
	if err != nil {
		return Perception{}, err
	}
	p := Perception{Tier: st.tier, Timed: st.d.Timed, Flags: st.flagCodes(), BlindSpots: st.blindSpots(false)}
	if l := st.d.Analysis.Loudness; l != nil {
		lufs, tp, lra := l.IntegratedLUFS, l.TruePeakDBTP, l.LRA
		p.LUFSI, p.TruePeakDB, p.LRALU = &lufs, &tp, &lra
	}
	if sp := st.d.Analysis.Spectrum; sp != nil && sp.BandwidthHz != nil {
		bw := float64(*sp.BandwidthHz)
		p.BandwidthHz = &bw
	}
	if t := st.tempo; t != nil && t.BPM != nil {
		bpm, conf := *t.BPM, t.Confidence
		p.TempoBPM, p.TempoConfidence = &bpm, &conf
	}
	if k := st.key; k != nil && k.Key != nil && k.Camelot != nil {
		p.Key = &PerceptionKey{Name: *k.Key, Camelot: *k.Camelot, Confidence: k.R, AmbiguousRelative: k.AmbiguousRelative}
	}
	if e := st.energy; e != nil && e.Energy != nil {
		p.Energy = &PerceptionEnergy{Value: *e.Energy, OnsetRate: e.OnsetRate, CentroidHz: e.CentroidHz}
	}
	p.Sections = make([]PerceptionSection, len(st.d.Sections))
	for i, sec := range st.d.Sections {
		p.Sections[i] = PerceptionSection{Label: sec.Label, Start: sec.StartS, End: sec.EndS, Source: sec.Source, Confidence: sec.Confidence}
	}
	return p, nil
}
