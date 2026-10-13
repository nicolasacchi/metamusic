package ears

import (
	"context"
)

// Dossier builds the get_track_analysis payload (C51.4).
func (s *Service) Dossier(ctx context.Context, trackID string) (Dossier, error) {
	st, err := s.load(ctx, trackID)
	if err != nil {
		return Dossier{}, err
	}
	d := Dossier{
		TrackID: trackID, Tier: st.tier, Complete: st.tier == "A", Missing: st.missing,
		Summary: summarize(st), Loudness: st.loudness, Spectrum: st.spectrum,
		Structure: st.structure(), Flags: st.flags, BlindSpots: st.blindSpots(false),
		Spectrogram: st.spectrogramInfo(), Provenance: st.provenance(),
	}
	if st.silence != nil {
		v := SilenceBlockOrNull(*st.silence)
		d.Silence = &v
	}
	if st.clipping != nil {
		v := ClippingBlockOrNull(*st.clipping)
		d.Clipping = &v
	}
	if st.tokens != nil {
		v := TokenLoopsBlockOrNull(*st.tokens)
		d.TokenLoops = &v
	}
	return d, nil
}

// structure is the section view with the score-plan duration check.
func (st *state) structure() Structure {
	out := Structure{DurationS: st.d.DurationS, Timed: st.d.Timed, Sections: []StructureSection{}}
	for _, sec := range st.d.Sections {
		out.Sections = append(out.Sections, StructureSection{Idx: sec.Idx, Label: sec.Label, Kind: sec.Kind, StartS: sec.StartS, EndS: sec.EndS})
	}
	if src, conf := st.sectionSource(); src != "" {
		out.Source, out.Confidence = &src, conf
	}
	if st.d.Timed && st.secs.ExpectedDurationS != nil && *st.secs.ExpectedDurationS > 0 {
		exp := *st.secs.ExpectedDurationS
		ratio := r3(st.d.DurationS / exp)
		out.ExpectedDurationS, out.Ratio = &exp, &ratio
	}
	return out
}

func (st *state) spectrogramInfo() SpectrogramInfo {
	if !st.spectro.current() {
		return SpectrogramInfo{}
	}
	b, w, h, q := st.spectro.Bytes, st.spectro.Params.Width, st.spectro.Params.Height, st.spectro.Params.Quality
	return SpectrogramInfo{Available: true, Bytes: &b, Width: &w, Height: &h, Quality: &q}
}

// provenance names the version of every analyzer behind the payload.
func (st *state) provenance() Provenance {
	m := map[string]string{}
	for an, r := range st.rows {
		m[an] = r.Version
	}
	if st.spectro.current() {
		m[anSpectrogram] = st.spectro.Params.Def + " ffmpeg " + st.spectro.Params.FFmpeg
	}
	return Provenance{Analyzers: m}
}
