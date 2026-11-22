package morning

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Settings are the Morning Crate's behaviour (C382). They hold no money: the crate only reads takes that already exist.
type Settings struct {
	Enabled          bool      `json:"enabled"`
	At               string    `json:"at"` // local HH:MM in MM_TZ
	Minutes          int       `json:"minutes"`
	SideMinutes      int       `json:"side_minutes"`
	MaxTakes         int       `json:"max_takes"`
	Anchors          int       `json:"anchors"`
	IncludeTruncated bool      `json:"include_truncated"`
	KeepDays         int       `json:"keep_days"`
	WaitMinutes      int       `json:"wait_minutes"`
	UpdatedBy        string    `json:"updated_by"` // principal key
	UpdatedAt        time.Time `json:"updated_at"`
}

// SettingsPatch changes the fields that are set.
type SettingsPatch struct {
	Enabled          *bool
	At               *string
	Minutes          *int
	SideMinutes      *int
	MaxTakes         *int
	Anchors          *int
	IncludeTruncated *bool
	KeepDays         *int
	WaitMinutes      *int
}

// Options turns the settings into the planner's options.
func (s Settings) Options() Options {
	o := DefaultOptions()
	o.Minutes, o.SideMinutes, o.MaxTakes, o.Anchors, o.IncludeTruncated = s.Minutes, s.SideMinutes, s.MaxTakes, s.Anchors, s.IncludeTruncated
	return o
}

// validAt reports whether at is a local wall-clock time "HH:MM" (00:00..23:59).
func validAt(at string) bool {
	if len(at) != 5 || at[2] != ':' {
		return false
	}
	h, err1 := strconv.Atoi(at[:2])
	m, err2 := strconv.Atoi(at[3:])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 && at[0] != '+' && at[0] != '-' && at[3] != '+' && at[3] != '-'
}

func badSetting(field, rule string) *problem.Error {
	return problem.E(422, "MORNING_SETTINGS_INVALID", "morning crate setting "+field+" is out of range: "+rule, map[string]any{"field": field, "rule": rule})
}

// validate checks every bound of the table (they repeat the CHECKs so a breach answers 422, not a constraint error).
func (s Settings) validate() error {
	ranges := []struct {
		field string
		v     int
		lo    int
		hi    int
	}{
		{"minutes", s.Minutes, 5, 120}, {"side_minutes", s.SideMinutes, 5, 15}, {"max_takes", s.MaxTakes, 1, 30},
		{"anchors", s.Anchors, 0, 2}, {"keep_days", s.KeepDays, 1, 30}, {"wait_minutes", s.WaitMinutes, 0, 120},
	}
	for _, r := range ranges {
		if r.v < r.lo || r.v > r.hi {
			return badSetting(r.field, fmt.Sprintf("%d..%d", r.lo, r.hi))
		}
	}
	if !validAt(s.At) {
		return badSetting("at", "HH:MM, 00:00..23:59")
	}
	return nil
}

// ownerSide admits the Owner, the Owner's devices and the Owner's agents; Editors, Listeners and anonymous callers never see or change
// anything of the Morning Crate (D-M6N-8).
func ownerSide(p auth.Principal) error {
	if p.Key() == "" || !p.IsOwnerSide() {
		return problem.OwnerOnly("the morning crate")
	}
	return nil
}

// Settings reads the single settings row.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	return readSettings(ctx, s.o.Store.R)
}

func readSettings(ctx context.Context, q Querier) (Settings, error) {
	var (
		st             Settings
		en, trunc      int
		updatedAtStamp string
	)
	err := q.QueryRowContext(ctx, `SELECT enabled, at, minutes, side_minutes, max_takes, anchors, include_truncated, keep_days, wait_minutes, updated_by, updated_at
		FROM morning_crate_settings WHERE id = 1`).Scan(&en, &st.At, &st.Minutes, &st.SideMinutes, &st.MaxTakes, &st.Anchors, &trunc, &st.KeepDays, &st.WaitMinutes,
		&st.UpdatedBy, &updatedAtStamp)
	if err != nil {
		return Settings{}, fmt.Errorf("morning: settings: %w", err)
	}
	st.Enabled, st.IncludeTruncated = en == 1, trunc == 1
	if st.UpdatedAt, err = store.ParseInstant(updatedAtStamp); err != nil {
		return Settings{}, fmt.Errorf("morning: settings: %w", err)
	}
	return st, nil
}

// UpdateSettings applies a patch, validated as a whole, and records who changed it.
func (s *Service) UpdateSettings(ctx context.Context, p auth.Principal, in SettingsPatch) (Settings, error) {
	if err := ownerSide(p); err != nil {
		return Settings{}, err
	}
	var out Settings
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		cur, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if in.At != nil {
			cur.At = *in.At
		}
		if in.Minutes != nil {
			cur.Minutes = *in.Minutes
		}
		if in.SideMinutes != nil {
			cur.SideMinutes = *in.SideMinutes
		}
		if in.MaxTakes != nil {
			cur.MaxTakes = *in.MaxTakes
		}
		if in.Anchors != nil {
			cur.Anchors = *in.Anchors
		}
		if in.IncludeTruncated != nil {
			cur.IncludeTruncated = *in.IncludeTruncated
		}
		if in.KeepDays != nil {
			cur.KeepDays = *in.KeepDays
		}
		if in.WaitMinutes != nil {
			cur.WaitMinutes = *in.WaitMinutes
		}
		if err := cur.validate(); err != nil {
			return err
		}
		cur.UpdatedBy, cur.UpdatedAt = p.Key(), s.now().UTC().Truncate(time.Millisecond)
		if _, err := tx.ExecContext(ctx, `UPDATE morning_crate_settings SET enabled=?, at=?, minutes=?, side_minutes=?, max_takes=?, anchors=?, include_truncated=?,
			keep_days=?, wait_minutes=?, updated_by=?, updated_at=? WHERE id = 1`, b2i(cur.Enabled), cur.At, cur.Minutes, cur.SideMinutes, cur.MaxTakes, cur.Anchors,
			b2i(cur.IncludeTruncated), cur.KeepDays, cur.WaitMinutes, cur.UpdatedBy, store.FormatTime(cur.UpdatedAt)); err != nil {
			return fmt.Errorf("morning: update settings: %w", err)
		}
		out = cur
		return s.completeAudit(ctx, tx, 200, nil)
	})
	return out, err
}

// dailyAt is the schedule's view of the settings: the time and whether the crate is enabled.
func (s *Service) dailyAt(ctx context.Context) (string, bool, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return "", false, err
	}
	return st.At, st.Enabled, nil
}
