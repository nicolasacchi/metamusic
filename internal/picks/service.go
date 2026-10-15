package picks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Ladder is the ladder source (same method set as ears.Judgments.Strengths); app.Build wires duels.Service to it, so
// picks never imports duels (C87).
type Ladder interface {
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// Service records and serves Picks (C91). The method set is frozen by L1-B2.
type Service struct {
	st    *store.Store
	l     Ladder
	now   func() time.Time
	newID func() string
}

// New builds the Service. A nil Ladder makes the ladder rule never apply.
func New(st *store.Store, l Ladder, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, l: l, now: now, newID: newID}
}

func notFound(songID string) error {
	return problem.E(404, "NOT_FOUND", "song not found", map[string]any{"song_id": songID})
}

func humanOnly(hint string) error {
	d := map[string]any{}
	if hint != "" {
		d["hint"] = hint
	}
	return problem.E(403, "HUMAN_ONLY", "only the operator can do this", d)
}

// requireLiveSongTx is the C90.5 check: 404 for an unknown Song, 409 SONG_RETIRED for a retired one.
func requireLiveSongTx(ctx context.Context, tx *sql.Tx, songID string) error {
	var retired sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT retired_at FROM songs WHERE id = ?`, songID).Scan(&retired)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(songID)
	}
	if err != nil {
		return fmt.Errorf("picks: song: %w", err)
	}
	if retired.Valid {
		return problem.E(409, "SONG_RETIRED", "the song was merged into another", map[string]any{"song_id": songID})
	}
	return nil
}

// requireEligibleTakeTx checks that takeID is a take of the Song (422 TAKE_NOT_IN_SONG) and not discarded (422 TAKE_NOT_ELIGIBLE).
func requireEligibleTakeTx(ctx context.Context, tx *sql.Tx, songID, takeID string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT t.status FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? AND ts.track_id = ?`, songID, takeID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return problem.E(422, "TAKE_NOT_IN_SONG", "the take does not belong to the song", map[string]any{"song_id": songID, "take_id": takeID})
	}
	if err != nil {
		return fmt.Errorf("picks: take: %w", err)
	}
	if status == "discarded" {
		return problem.E(422, "TAKE_NOT_ELIGIBLE", "the take is discarded", map[string]any{"take_id": takeID, "status": status})
	}
	return nil
}

// principalActor is the actor triple of a pin or unpin. The pick_events schema admits human, agent and system kinds
// only, so a paired device is recorded as kind human with its own key; renderers derive "device" from the key prefix.
func principalActor(p auth.Principal) actor {
	kind := string(p.Kind)
	if p.Kind == auth.KindDevice {
		kind = string(auth.KindHuman)
	}
	return actor{Key: p.Key(), Kind: kind, Name: p.Name}
}
