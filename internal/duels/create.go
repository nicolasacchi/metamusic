package duels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Create makes a duel (C48.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Duel, error) {
	if s.unconfigured() {
		return Duel{}, errors.ErrUnsupported
	}
	if s.o.Clips == nil {
		return Duel{}, unavailable("clips")
	}
	if _, err := canJudge(p); err != nil {
		return Duel{}, err
	}
	if in.ATrackID == in.BTrackID {
		return Duel{}, problem.E(422, "DUEL_SAME_TRACK", "a duel needs two different tracks", map[string]any{"track_id": in.ATrackID})
	}
	refs, err := s.o.Library.TrackRefs(ctx, []string{in.ATrackID, in.BTrackID})
	if err != nil {
		return Duel{}, err
	}
	for _, id := range []string{in.ATrackID, in.BTrackID} {
		if _, ok := refs[id]; !ok {
			return Duel{}, problem.E(404, "NOT_FOUND", "track not found", map[string]any{"track_id": id})
		}
	}
	ra, rb := refs[in.ATrackID], refs[in.BTrackID]
	w, err := clip.ResolveWindow(math.Min(ra.DurationS, rb.DurationS), in.FromS, in.ToS)
	if err != nil {
		return Duel{}, err
	}
	blind := in.Blind == nil || *in.Blind
	swapped := blind && s.coin()
	slotA, slotB := ra, rb
	if swapped {
		slotA, slotB = rb, ra
	}
	d := duelRow{ID: s.newID(), SlotA: slotA.ID, SlotB: slotB.ID, Swapped: swapped, Blind: blind, FromMS: w.StartMS, ToMS: w.EndMS,
		ClipDef: clip.Def, Tool: s.o.Clips.ToolVersion(), Match: "off", CreatedBy: p.Key(), Kind: string(p.Kind)}
	specA, specB := d.spec(slotA, false), d.spec(slotB, true)
	if in.MatchLoudness == nil || *in.MatchLoudness {
		ma, err := s.o.Clips.Measure(ctx, specA)
		if err != nil {
			return Duel{}, err
		}
		mb, err := s.o.Clips.Measure(ctx, specB)
		if err != nil {
			return Duel{}, err
		}
		var target *float64
		d.GainACDB, d.GainBCDB, target, d.Match = clip.MatchGains(ma, mb)
		d.LUFSA = sql.NullFloat64{Float64: ma.IntegratedLUFS, Valid: true}
		d.LUFSB = sql.NullFloat64{Float64: mb.IntegratedLUFS, Valid: true}
		d.TPA = sql.NullFloat64{Float64: ma.TruePeakDBTP, Valid: true}
		d.TPB = sql.NullFloat64{Float64: mb.TruePeakDBTP, Valid: true}
		if target != nil {
			d.TargetLUFS = sql.NullFloat64{Float64: *target, Valid: true}
		}
		specA.GainCDB, specB.GainCDB = d.GainACDB, d.GainBCDB
	}
	// Both clips exist before the row does: a render failure leaves no duel behind.
	var objs [2]clip.Object
	if objs[0], err = s.o.Clips.Ensure(ctx, specA); err != nil {
		return Duel{}, err
	}
	if objs[1], err = s.o.Clips.Ensure(ctx, specB); err != nil {
		return Duel{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	d.CreatedAt, d.ExpireAt = now, now.Add(s.expiry)
	if _, err := s.o.Store.W.ExecContext(ctx, `INSERT INTO duels(id, slot_a_track_id, slot_b_track_id, swapped, blind, from_ms, to_ms, clip_def, tool_version,
		match, target_lufs, gain_a_cdb, gain_b_cdb, window_lufs_a, window_lufs_b, window_tp_a, window_tp_b, created_by, created_by_kind, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.SlotA, d.SlotB, d.Swapped, d.Blind, d.FromMS, d.ToMS, d.ClipDef, d.Tool, d.Match, d.TargetLUFS, d.GainACDB, d.GainBCDB,
		d.LUFSA, d.LUFSB, d.TPA, d.TPB, d.CreatedBy, d.Kind, rfc(d.CreatedAt), rfc(d.ExpireAt)); err != nil {
		return Duel{}, fmt.Errorf("duels: insert: %w", err)
	}
	return s.view(ctx, p, d, nil, &objs, false)
}
