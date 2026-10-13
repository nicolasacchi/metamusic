package ears_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "u1", Name: "u1"}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "a1", Name: "a1"}
)

func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a *problem.Error", err)
	}
	return pe
}

func TestGetTrackSpectrogram_InlineAndLink(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	file := filepath.Join(r.dir, "derived", r.ears.SpectroSHA[:2], r.ears.SpectroSHA+".spectro.jpg")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	v, err := r.svc.Spectrogram(ctx, human, lt.TrackCitta, true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Image == nil || v.Image.MIMEType != "image/jpeg" {
		t.Fatalf("image = %+v", v.Image)
	}
	got, err := base64.StdEncoding.DecodeString(v.Image.DataBase64)
	if err != nil || string(got) != string(raw) {
		t.Errorf("inline image differs from the file (%v)", err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != v.SHA256 || v.SHA256 != r.ears.SpectroSHA || v.Bytes != int64(len(raw)) {
		t.Errorf("sha/bytes = %s/%d", v.SHA256, v.Bytes)
	}
	if v.ContentType != "image/jpeg" || v.Width != 1308 || v.Height != 560 || v.Quality != 85 || v.TrackID != lt.TrackCitta {
		t.Errorf("view = %+v", v)
	}
	if p := v.Plot; p.X0 != 142 || p.Y0 != 112 || p.Width != 1024 || p.Height != 384 || p.DurationS != 6 || p.FreqScale != "log" {
		t.Errorf("plot = %+v", p)
	}
	if v.Overlay.LineTiming != "none" || v.Overlay.Timed {
		t.Errorf("overlay = %+v (Citta has no timed sections)", v.Overlay)
	}
	wantBlind := "a spectrogram shows energy over time and frequency, not pitch accuracy or words"
	if !strings.Contains(strings.Join(v.BlindSpots, "|"), wantBlind) {
		t.Errorf("blind spots = %v", v.BlindSpots)
	}
	// the link verifies
	u, err := url.Parse(v.URL)
	if err != nil || u.Path != "/media/"+v.SHA256+".jpg" || u.IsAbs() {
		t.Fatalf("url = %q (%v), want a relative /media/<sha>.jpg link", v.URL, err)
	}
	if err := r.opts.Signer.Verify(v.SHA256, "jpg", u.Query().Get("exp"), u.Query().Get("sig")); err != nil {
		t.Errorf("link does not verify: %v", err)
	}
	if v.TTLS != 12*3600 || v.ExpiresAt == "" {
		t.Errorf("human ttl = %d expires %q", v.TTLS, v.ExpiresAt)
	}

	// inline=false: link only
	v2, err := r.svc.Spectrogram(ctx, agent, lt.TrackCitta, false)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Image != nil || v2.TTLS != 15*60 || v2.URL == "" {
		t.Errorf("link-only view = %+v", v2)
	}
}

func TestGetTrackSpectrogram_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("missing rendition", func(t *testing.T) {
		r := newRig(t)
		_, err := r.svc.Spectrogram(ctx, human, lt.TrackTwinA, true)
		pe := problemOf(t, err)
		if pe.Status != 409 || pe.Code != "ANALYSIS_UNAVAILABLE" || pe.Details["analyzer"] != "spectrogram" || pe.Details["hint"] != "ask the operator to run analysis" {
			t.Errorf("err = %+v", pe)
		}
	})
	t.Run("unknown track", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.svc.Spectrogram(ctx, human, "nope", true); err == nil || errors.As(err, new(*problem.Error)) {
			t.Errorf("err = %v, want a not-found error", err)
		}
	})
	t.Run("missing file is an internal error", func(t *testing.T) {
		r := newRig(t, withTierARows())
		if err := os.Remove(filepath.Join(r.dir, "derived", r.ears.SpectroSHA[:2], r.ears.SpectroSHA+".spectro.jpg")); err != nil {
			t.Fatal(err)
		}
		for _, inline := range []bool{true, false} {
			_, err := r.svc.Spectrogram(ctx, human, lt.TrackCitta, inline)
			if err == nil || errors.As(err, new(*problem.Error)) {
				t.Errorf("inline=%v: err = %v, want a plain error (500 INTERNAL at the API)", inline, err)
			}
		}
	})
	t.Run("no signer", func(t *testing.T) {
		r := newRig(t, withTierARows(), func(_ *rig, o *ears.Options) { o.Signer = nil })
		_, err := r.svc.Spectrogram(ctx, human, lt.TrackCitta, true)
		if pe := problemOf(t, err); pe.Status != 503 {
			t.Errorf("err = %+v", pe)
		}
	})
}

func TestGetTrackSpectrogram_OverlayFollowsStoredParams(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.AnalyzeTrack(ctx, lt.TrackLuna, ears.AnalyzeOptions{}); err != nil {
		t.Fatal(err)
	}
	v, err := r.svc.Spectrogram(ctx, human, lt.TrackLuna, false)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Overlay.Timed || v.Overlay.Source == nil || *v.Overlay.Source != "plan" || v.Overlay.Confidence == nil {
		t.Errorf("overlay = %+v, want timed from the plan", v.Overlay)
	}
	if v.Plot.DurationS != 12 || v.Plot.Width < 512 || v.Plot.X0 != 142 {
		t.Errorf("plot = %+v", v.Plot)
	}
	// sections change after the render: the view still describes what was drawn
	for i, s := range []struct {
		l    string
		a, b float64
	}{{"a", 0, 6}, {"b", 6, 12}} {
		r.exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?, 'manual', ?, 'other', ?, ?, ?, ?, 1)`, lt.TrackLuna, i, s.l, s.l, s.a, s.b)
	}
	v2, _ := r.svc.Spectrogram(ctx, human, lt.TrackLuna, false)
	if v2.Overlay.Source == nil || *v2.Overlay.Source != "plan" {
		t.Errorf("overlay after a section change = %+v, want the rendered one", v2.Overlay)
	}
}
