package ears

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// unavailableHint is the one hint every ANALYSIS_UNAVAILABLE answer carries.
const unavailableHint = "ask the operator to run analysis"

// analysisUnavailable is the 409 answer for an analyzer that has not run.
func analysisUnavailable(analyzer string) *problem.Error {
	return problem.E(http.StatusConflict, "ANALYSIS_UNAVAILABLE", analyzer+" has not been computed for this track",
		map[string]any{"analyzer": analyzer, "hint": unavailableHint})
}

// Spectrogram builds the get_track_spectrogram payload (C51.5).
func (s *Service) Spectrogram(ctx context.Context, p auth.Principal, trackID string, inline bool) (SpectrogramView, error) {
	st, err := s.load(ctx, trackID)
	if err != nil {
		return SpectrogramView{}, err
	}
	sp := st.spectro
	if sp == nil {
		return SpectrogramView{}, analysisUnavailable(anSpectrogram)
	}
	if s.o.Signer == nil {
		return SpectrogramView{}, problem.E(http.StatusServiceUnavailable, "UNAVAILABLE", "media links are not available", map[string]any{"component": "signer"})
	}
	if s.o.Data == nil {
		return SpectrogramView{}, errors.ErrUnsupported
	}
	if _, err := s.o.Data.Stat(sp.Path); err != nil {
		return SpectrogramView{}, fmt.Errorf("ears: spectrogram file: %w", err)
	}
	v := SpectrogramView{
		TrackID: trackID, ContentType: "image/jpeg", SHA256: sp.SHA256, Bytes: sp.Bytes,
		Width: sp.Params.Width, Height: sp.Params.Height, Quality: sp.Params.Quality,
		Plot: SpectrogramPlot{X0: sp.Params.Geometry.X0, Y0: sp.Params.Geometry.Y0, Width: sp.Params.Geometry.W, Height: sp.Params.Geometry.H,
			DurationS: sp.Params.Geometry.DurationS, FreqScale: "log"},
		Overlay:    st.overlayOf(sp),
		BlindSpots: st.blindSpots(true),
	}
	if sp.Params.FreqScale != "" {
		v.Plot.FreqScale = sp.Params.FreqScale
	}
	ttl, bucket := serve.TTLFor(p)
	link := s.o.Signer.Sign(sp.SHA256, "jpg", ttl, bucket)
	v.URL, v.ExpiresAt, v.TTLS = link.URL, link.Expires.UTC().Format(time.RFC3339), int64(ttl/time.Second)
	if inline {
		raw, err := s.o.Data.ReadFile(sp.Path)
		if err != nil {
			return SpectrogramView{}, fmt.Errorf("ears: spectrogram file: %w", err)
		}
		if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != sp.SHA256 {
			return SpectrogramView{}, fmt.Errorf("ears: spectrogram file does not match its recorded sha256")
		}
		v.Image = &ImageData{MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(raw)}
	}
	return v, nil
}

// overlayOf describes what the stored picture drew: the recorded overlay when the params carry it,
// else the track's current sections.
func (st *state) overlayOf(sp *spectroRow) SpectrogramOverlay {
	o := SpectrogramOverlay{LineTiming: "none"}
	if info := sp.Params.Overlay; info != nil {
		o.Timed, o.Source, o.Confidence = info.Timed, info.Source, info.Confidence
		return o
	}
	if src, conf := st.sectionSource(); src != "" {
		o.Timed, o.Source, o.Confidence = true, &src, conf
	}
	return o
}
