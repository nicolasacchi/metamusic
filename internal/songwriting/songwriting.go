// Package songwriting holds the operator's songwriting duration targets. They live in the existing settings
// table (no migration): a song shorter than the minimum, or outside the target band, draws a lint warning
// (yue2.Lint) and the targets are shown to LLM agents (get_songwriting_settings, the track context bundle).
package songwriting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// The settings keys.
const (
	KeyMin       = "songwriting.min_duration_s"
	KeyTargetMin = "songwriting.target_min_duration_s"
	KeyTargetMax = "songwriting.target_max_duration_s"
)

// Defaults (the operator wants songs longer than 3 minutes, aiming at 4 to 5) and the accepted range.
const (
	DefaultMin       = 180
	DefaultTargetMin = 240
	DefaultTargetMax = 300
	Floor            = 30
	Ceiling          = 600
)

// ErrInvalid is returned for an empty patch, a value outside Floor..Ceiling, or an order other than
// min <= target_min <= target_max.
var ErrInvalid = errors.New("songwriting: invalid duration targets")

// Targets are the effective settings, in seconds. (No schema docs here: the tool schema counts against the tools/list
// size ceiling; the operation description explains the fields.)
type Targets struct {
	MinDurationS       int `json:"min_duration_s"`
	TargetMinDurationS int `json:"target_min_duration_s"`
	TargetMaxDurationS int `json:"target_max_duration_s"`
}

// Patch changes some of the settings; nil fields stay as they are.
type Patch struct {
	MinDurationS       *int
	TargetMinDurationS *int
	TargetMaxDurationS *int
}

// Defaults returns the targets used while nothing is stored.
func Defaults() Targets {
	return Targets{MinDurationS: DefaultMin, TargetMinDurationS: DefaultTargetMin, TargetMaxDurationS: DefaultTargetMax}
}

// Valid reports whether the targets are inside the accepted range and ordered.
func (t Targets) Valid() bool {
	in := func(v int) bool { return v >= Floor && v <= Ceiling }
	return in(t.MinDurationS) && in(t.TargetMinDurationS) && in(t.TargetMaxDurationS) &&
		t.MinDurationS <= t.TargetMinDurationS && t.TargetMinDurationS <= t.TargetMaxDurationS
}

// Service reads and writes the targets.
type Service struct{ st *store.Store }

// New builds the service over the settings table of st.
func New(st *store.Store) *Service { return &Service{st: st} }

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func load(ctx context.Context, q queryer) (Targets, error) {
	t := Defaults()
	for _, f := range []struct {
		key string
		dst *int
	}{{KeyMin, &t.MinDurationS}, {KeyTargetMin, &t.TargetMinDurationS}, {KeyTargetMax, &t.TargetMaxDurationS}} {
		var v string
		err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, f.key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Targets{}, fmt.Errorf("songwriting: read %s: %w", f.key, err)
		}
		if n, perr := strconv.Atoi(v); perr == nil {
			*f.dst = n
		}
	}
	if !t.Valid() { // a hand-edited row must not make lint nonsensical
		return Defaults(), nil
	}
	return t, nil
}

// Get returns the effective targets: stored values over the defaults.
func (s *Service) Get(ctx context.Context) (Targets, error) { return load(ctx, s.st.R) }

// Set applies the patch atomically and returns the new targets. The result must be valid (ErrInvalid otherwise,
// nothing written).
func (s *Service) Set(ctx context.Context, p Patch) (Targets, error) {
	if p.MinDurationS == nil && p.TargetMinDurationS == nil && p.TargetMaxDurationS == nil {
		return Targets{}, ErrInvalid
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Targets{}, fmt.Errorf("songwriting: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	t, err := load(ctx, tx)
	if err != nil {
		return Targets{}, err
	}
	if p.MinDurationS != nil {
		t.MinDurationS = *p.MinDurationS
	}
	if p.TargetMinDurationS != nil {
		t.TargetMinDurationS = *p.TargetMinDurationS
	}
	if p.TargetMaxDurationS != nil {
		t.TargetMaxDurationS = *p.TargetMaxDurationS
	}
	if !t.Valid() {
		return Targets{}, ErrInvalid
	}
	for _, kv := range []struct {
		key string
		v   int
		set bool
	}{{KeyMin, t.MinDurationS, p.MinDurationS != nil}, {KeyTargetMin, t.TargetMinDurationS, p.TargetMinDurationS != nil}, {KeyTargetMax, t.TargetMaxDurationS, p.TargetMaxDurationS != nil}} {
		if !kv.set {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv.key, strconv.Itoa(kv.v)); err != nil {
			return Targets{}, fmt.Errorf("songwriting: write %s: %w", kv.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Targets{}, fmt.Errorf("songwriting: commit: %w", err)
	}
	return t, nil
}
