package nightshift

import (
	"context"
	"database/sql"
	"regexp"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Settings is the night shift's behaviour (C372, D-M6N-1): when, how many, how adventurous. No money field lives here, the mandate
// holds all of it. The migration ships the row switched OFF, so nothing happens until the Owner enables it in the app.
type Settings struct {
	Enabled         bool
	At              string // "HH:MM" local (MM_TZ)
	MaxTakes        int    // 1..12
	ExplorePPM      int    // 0..500000
	MinStars        int    // 1..5
	ListeningChecks string // "on" | "off"
	HaltedAt        *time.Time
	HaltedBy        *string
	HaltReason      *string
	UpdatedBy       string
	UpdatedAt       time.Time
}

// Halted reports whether the Owner (or an agent on the Owner's behalf) stopped the night shift and it has not been re-enabled.
func (s Settings) Halted() bool { return s.HaltedAt != nil }

// Limits of C372.
const (
	MinTakes     = 1
	MaxTakes     = 12
	MaxExplore   = 500_000
	ListeningOn  = "on"
	ListeningOff = "off"
	MaxReason    = 500
)

var atRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// SettingsPatch is the body of update_night_shift: nil fields stay as they are.
type SettingsPatch struct {
	Enabled         *bool
	At              *string
	MaxTakes        *int
	ExplorePPM      *int
	MinStars        *int
	ListeningChecks *string
}

func invalid(field, rule string) error {
	return problem.E(422, "VALIDATION_FAILED", field+": "+rule, map[string]any{"field": field, "rule": rule})
}

// Validate checks a patch against C372's ranges (the rule names are stable).
func (p SettingsPatch) Validate() error {
	switch {
	case p.At != nil && !atRE.MatchString(*p.At):
		return invalid("at", "hh_mm")
	case p.MaxTakes != nil && (*p.MaxTakes < MinTakes || *p.MaxTakes > MaxTakes):
		return invalid("max_takes", "range")
	case p.ExplorePPM != nil && (*p.ExplorePPM < 0 || *p.ExplorePPM > MaxExplore):
		return invalid("explore_ppm", "range")
	case p.MinStars != nil && (*p.MinStars < 1 || *p.MinStars > 5):
		return invalid("min_stars", "range")
	case p.ListeningChecks != nil && *p.ListeningChecks != ListeningOn && *p.ListeningChecks != ListeningOff:
		return invalid("listening_checks", "on_or_off")
	}
	return nil
}

const settingsCols = `enabled, at, max_takes, explore_ppm, min_stars, listening_checks, halted_at, halted_by, halt_reason, updated_by, updated_at`

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readSettings(ctx context.Context, q querier) (Settings, error) {
	var (
		s                 Settings
		en                int
		upd               string
		hAt, hBy, hReason sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT `+settingsCols+` FROM night_shift_settings WHERE id = 1`).
		Scan(&en, &s.At, &s.MaxTakes, &s.ExplorePPM, &s.MinStars, &s.ListeningChecks, &hAt, &hBy, &hReason, &s.UpdatedBy, &upd)
	if err != nil {
		return Settings{}, err
	}
	s.Enabled = en == 1
	if hAt.Valid {
		t, err := store.ParseTime(hAt.String)
		if err != nil {
			return Settings{}, err
		}
		s.HaltedAt, s.HaltedBy, s.HaltReason = &t, &hBy.String, &hReason.String
	}
	s.UpdatedAt, err = store.ParseTime(upd)
	return s, err
}

// Settings reads the settings row.
func (r *Runner) Settings(ctx context.Context) (Settings, error) {
	return readSettings(ctx, r.o.Store.R)
}

// UpdateSettings applies a patch (update_night_shift). Only the Owner as a person may; `enabled: true` also clears a halt. It
// changes behaviour only: the mandate alone bounds money, and nothing here can create or widen one.
func (r *Runner) UpdateSettings(ctx context.Context, p auth.Principal, patch SettingsPatch) (Settings, error) {
	switch {
	case p.IsOwner():
	case p.IsPerson() && !p.IsOwnerSide():
		return Settings{}, problem.OwnerOnly("night shift")
	default:
		return Settings{}, problem.E(403, "HUMAN_ONLY", "only the Owner in the app changes the night shift", nil)
	}
	if err := patch.Validate(); err != nil {
		return Settings{}, err
	}
	var out Settings
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		s, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		clearHalt := false
		if patch.Enabled != nil {
			s.Enabled = *patch.Enabled
			clearHalt = *patch.Enabled
		}
		if patch.At != nil {
			s.At = *patch.At
		}
		if patch.MaxTakes != nil {
			s.MaxTakes = *patch.MaxTakes
		}
		if patch.ExplorePPM != nil {
			s.ExplorePPM = *patch.ExplorePPM
		}
		if patch.MinStars != nil {
			s.MinStars = *patch.MinStars
		}
		if patch.ListeningChecks != nil {
			s.ListeningChecks = *patch.ListeningChecks
		}
		if clearHalt {
			s.HaltedAt, s.HaltedBy, s.HaltReason = nil, nil, nil
		}
		s.UpdatedBy, s.UpdatedAt = p.Key(), r.o.Now()
		en := 0
		if s.Enabled {
			en = 1
		}
		var hAt, hBy, hReason any
		if s.HaltedAt != nil {
			hAt, hBy, hReason = store.FormatTime(*s.HaltedAt), *s.HaltedBy, *s.HaltReason
		}
		if _, err := tx.ExecContext(ctx, `UPDATE night_shift_settings SET enabled = ?, at = ?, max_takes = ?, explore_ppm = ?, min_stars = ?,
			listening_checks = ?, halted_at = ?, halted_by = ?, halt_reason = ?, updated_by = ?, updated_at = ? WHERE id = 1`,
			en, s.At, s.MaxTakes, s.ExplorePPM, s.MinStars, s.ListeningChecks, hAt, hBy, hReason, s.UpdatedBy, store.FormatTime(s.UpdatedAt)); err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}
