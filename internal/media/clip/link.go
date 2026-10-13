package clip

import (
	"context"
	"errors"
	"math"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Match loudness bounds of get_clip (LUFS).
const (
	minMatchLUFS = -40.0
	maxMatchLUFS = -6.0
)

// Link answers get_clip: a signed URL of the (rendered) clip of trackID.
func (s *Service) Link(ctx context.Context, p auth.Principal, trackID string, q Query) (Link, error) {
	if s.lib == nil || s.signer == nil {
		// not wired (the CLI, bare stub rigs): the API reports errors.ErrUnsupported as 501
		return Link{}, errors.ErrUnsupported
	}
	refs, err := s.lib.TrackRefs(ctx, []string{trackID})
	if err != nil {
		return Link{}, err
	}
	ref, ok := refs[trackID]
	if !ok {
		return Link{}, library.ErrNotFound
	}
	w, err := ResolveWindow(ref.DurationS, q.FromS, q.ToS)
	if err != nil {
		return Link{}, err
	}
	if m := q.MatchLUFS; m != nil && (math.IsNaN(*m) || *m < minMatchLUFS || *m > maxMatchLUFS) {
		return Link{}, problem.E(422, "VALIDATION_FAILED", "match_lufs must be between -40 and -6", map[string]any{"field": "match_lufs"})
	}
	sp := Spec{MasterSHA256: ref.MasterSHA256, MasterPath: ref.MasterPath, SampleRate: ref.SampleRate, Channels: ref.Channels, W: w}
	l := Link{TrackID: trackID, ContentType: "audio/flac", SampleRate: OutRate, BitsPerSample: 24,
		FromS: float64(w.StartMS) / 1000, ToS: float64(w.EndMS) / 1000, DurationS: w.DurationS(), Match: "off"}
	if q.MatchLUFS != nil {
		m, err := s.Measure(ctx, sp)
		if err != nil {
			return Link{}, err
		}
		var gain int
		gain, l.Match = TargetGain(m, *q.MatchLUFS)
		sp.GainCDB = gain
		l.GainDB = float64(gain) / 100
		l.WindowLUFS, l.WindowTPDBTP = &m.IntegratedLUFS, &m.TruePeakDBTP
		t := *q.MatchLUFS
		l.TargetLUFS = &t
	}
	o, err := s.Ensure(ctx, sp)
	if err != nil {
		return Link{}, err
	}
	ttl, bucket := serve.TTLFor(p)
	sl := s.signer.Sign(o.Key, "flac", ttl, bucket)
	l.URL, l.Key, l.ExpiresAt, l.TTLS = sl.URL, o.Key, sl.Expires, int64(ttl.Seconds())
	l.Bytes, l.Channels = o.Bytes, o.Channels
	return l, nil
}
