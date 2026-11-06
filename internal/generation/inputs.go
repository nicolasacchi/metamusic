package generation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Source and reference resolution (C149.3, C165). A source is pinned at estimate time by its master digest and length and
// frozen in items_json; create does not read it again (a master never changes, rule 7). A reference is a window of a master
// cut by clip.Service.Reference into a 30.000 s clip whose content digest (clip_sha256) is pinned the same way; create
// renders it again and a different digest is 409 ESTIMATE_STALE reason reference_changed.

// maxSrcDS is the longest source an edit accepts: 600 s (the structural ceiling of an ACE-Step render).
const maxSrcDS = 6000

// refMinDS and refMaxDS bound a reference window (2..30 s); refShortDS is the shortest window without a warning (5 s).
const (
	refMinDS   = 20
	refMaxDS   = 300
	refShortDS = 50
	// refDefaultDS is the default reference window, the last 20 s of the track (clip.RefDefaultTailMS).
	refDefaultDS = 200
)

// ds converts API seconds to deciseconds, half away from zero (C149.2: 12.34 s is 123, 12.35 s is 124).
func ds(s float64) int { return int(math.Round(s * 10)) }

// secs is the API value of deciseconds: one decimal, never a float on the wire beyond that.
func secs(d int) float64 { return float64(d) / 10 }

func f64(d int) *float64 { v := secs(d); return &v }

// validSeconds reports whether s is a usable API value (finite, non-negative).
func validSeconds(s float64) bool { return !math.IsNaN(s) && !math.IsInf(s, 0) && s >= 0 }

// engineCapabilityErr is 422 ENGINE_CAPABILITY {engine, mode?, capability}; api.ErrEngineCapability has the same shape (generation
// cannot import api).
func engineCapabilityErr(engineName, mode, capability string) error {
	d := map[string]any{"engine": engineName, "capability": capability}
	if mode != "" {
		d["mode"] = mode
	}
	return perr(http.StatusUnprocessableEntity, "ENGINE_CAPABILITY", "the engine does not offer this: "+capability, d)
}

// srcPin is a resolved source: the request pin and the track row it was read from.
type srcPin struct {
	Ref   acestep.SrcRef
	Track library.TrackRef
}

// trackRef reads one track's media facts, once per estimate.
func (p *planner) trackRef(id string) (library.TrackRef, error) {
	if t, ok := p.refs[id]; ok {
		return t, nil
	}
	m, err := p.s.o.Library.TrackRefs(p.ctx, []string{id})
	if err != nil {
		return library.TrackRef{}, fmt.Errorf("generation: track ref: %w", err)
	}
	t, ok := m[id]
	if !ok {
		return t, unknownTrack(id)
	}
	p.refs[id] = t
	return t, nil
}

// checkFormat refuses a master the ACE-Step edit path cannot read: 44.1 or 48 kHz, one or two channels.
func checkFormat(engineName, mode string, t library.TrackRef) error {
	if (t.SampleRate != 44100 && t.SampleRate != 48000) || t.Channels < 1 || t.Channels > 2 {
		return engineCapabilityErr(engineName, mode, "src_format")
	}
	return nil
}

// resolveSrc pins the source track of an edit: its master digest and length in deciseconds. The master was already checked
// for a known recipe, a lossless file and a renderable take by checkMothers.
func (p *planner) resolveSrc(mode, id string) (srcPin, error) {
	t, err := p.trackRef(id)
	if err != nil {
		return srcPin{}, err
	}
	if err := checkFormat(acestep.Name, mode, t); err != nil {
		return srcPin{}, err
	}
	d := ds(t.DurationS)
	if d > maxSrcDS {
		return srcPin{}, engineCapabilityErr(acestep.Name, mode, "src_too_long")
	}
	return srcPin{Ref: acestep.SrcRef{TrackID: t.ID, MasterSHA256: t.MasterSHA256, DurationDS: d}, Track: t}, nil
}

// refPin is a resolved reference: the request pin, the window in milliseconds and what create needs to render the clip again.
type refPin struct {
	Ref   acestep.RefRef
	Spec  clip.RefSpec
	Short bool // the window is under 5 s: reference_window_short
}

// frozenRef is the part of a reference that items_json keeps for create (C149.3): the clip spec fields and the pinned digest.
type frozenRef struct {
	TrackID      string `json:"track_id"`
	MasterSHA256 string `json:"master_sha256"`
	MasterPath   string `json:"master_path"`
	SampleRate   int    `json:"sample_rate"`
	Channels     int    `json:"channels"`
	FromMS       int64  `json:"from_ms"`
	ToMS         int64  `json:"to_ms"`
	ClipSHA256   string `json:"clip_sha256"`
}

func (f frozenRef) spec() clip.RefSpec {
	return clip.RefSpec{MasterSHA256: f.MasterSHA256, MasterPath: f.MasterPath, SampleRate: f.SampleRate, Channels: f.Channels, FromMS: f.FromMS, ToMS: f.ToMS}
}

func (r refPin) frozen() frozenRef {
	return frozenRef{TrackID: r.Ref.TrackID, MasterSHA256: r.Spec.MasterSHA256, MasterPath: r.Spec.MasterPath, SampleRate: r.Spec.SampleRate,
		Channels: r.Spec.Channels, FromMS: r.Spec.FromMS, ToMS: r.Spec.ToMS, ClipSHA256: r.Ref.ClipSHA256}
}

// refWindow resolves the window of a reference in deciseconds: both ends absent is the last 20 s; one end absent is 20 s
// from or to the given one. It does not validate the bounds.
func refWindow(durationDS int, from, to *float64) (fromDS, toDS int) {
	switch {
	case from == nil && to == nil:
		f, t := clip.RefTailWindow(int64(durationDS) * 100)
		return int(f / 100), int(t / 100)
	case to == nil:
		fromDS = ds(*from)
		return fromDS, min(fromDS+refDefaultDS, durationDS)
	case from == nil:
		toDS = ds(*to)
		return max(toDS-refDefaultDS, 0), toDS
	}
	return ds(*from), ds(*to)
}

// resolveRef renders the reference clip of an item and pins its digest. item is the item index of the error.
func (p *planner) resolveRef(item int, r Reference) (refPin, error) {
	t, err := p.trackRef(r.TrackID)
	if err != nil {
		return refPin{}, err
	}
	if err := checkFormat(acestep.Name, "reference", t); err != nil {
		return refPin{}, err
	}
	for _, v := range []*float64{r.FromS, r.ToS} {
		if v != nil && !validSeconds(*v) {
			return refPin{}, invalidItem(item, "reference", "reference_window")
		}
	}
	durDS := ds(t.DurationS)
	from, to := refWindow(durDS, r.FromS, r.ToS)
	if n := to - from; from < 0 || n < refMinDS || n > refMaxDS || to > durDS {
		return refPin{}, invalidItem(item, "reference", "reference_window")
	}
	if p.s.e2.refClips == nil {
		return refPin{}, perr(http.StatusServiceUnavailable, "UNAVAILABLE", "reference clips are not available", map[string]any{"component": "clip"})
	}
	spec := clip.RefSpec{MasterSHA256: t.MasterSHA256, MasterPath: t.MasterPath, SampleRate: t.SampleRate, Channels: t.Channels,
		FromMS: int64(from) * 100, ToMS: int64(to) * 100}
	obj, err := p.s.e2.refClips.Reference(p.ctx, spec)
	if err != nil {
		return refPin{}, refClipErr(item, err)
	}
	return refPin{Ref: acestep.RefRef{TrackID: t.ID, ClipSHA256: obj.SHA256, FromDS: from, ToDS: to}, Spec: spec, Short: to-from < refShortDS}, nil
}

// refClipErr maps the clip renderer's refusals (C165, orchestrator decision): a bad window is INVALID_ITEM reference_window,
// an unreadable master format is ENGINE_CAPABILITY src_format and a silent window is INVALID_ITEM reference_silent.
func refClipErr(item int, err error) error {
	var pe *problem.Error
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case clip.CodeRefWindow:
		return invalidItem(item, "reference", "reference_window")
	case "CLIP_UNSUPPORTED_RATE", "CLIP_UNSUPPORTED_CHANNELS":
		return engineCapabilityErr(acestep.Name, "reference", "src_format")
	case clip.CodeRefSilent:
		return invalidItem(item, "reference", "reference_silent")
	}
	return err
}

// ensureRef renders a frozen reference again at create and compares the digest (C149.3).
func (s *Service) ensureRef(ctx context.Context, f frozenRef) error {
	if s.e2.refClips == nil {
		return perr(http.StatusServiceUnavailable, "UNAVAILABLE", "reference clips are not available", map[string]any{"component": "clip"})
	}
	obj, err := s.e2.refClips.Reference(ctx, f.spec())
	if err != nil {
		var pe *problem.Error
		if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
			return staleErr(map[string]any{"reason": "reference_changed", "track_id": f.TrackID})
		}
		return err
	}
	if obj.SHA256 != f.ClipSHA256 {
		return staleErr(map[string]any{"reason": "reference_changed", "track_id": f.TrackID})
	}
	return nil
}
