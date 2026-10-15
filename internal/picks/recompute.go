package picks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var errUnconfigured = errors.ErrUnsupported

// current is a (take, source) pair as stored.
type current struct {
	Take   *string
	Source Source
}

func (c current) equal(o current) bool {
	if c.Source != o.Source {
		return false
	}
	if (c.Take == nil) != (o.Take == nil) {
		return false
	}
	return c.Take == nil || *c.Take == *o.Take
}

// pinOp is an explicit pin or unpin applied inside a recompute (C91.6).
type pinOp struct {
	Set        bool // true pins Take, false unpins
	Take       string
	By         actor
	ProposalID *string
}

// storedPick is a song_picks row.
type storedPick struct {
	cur                    current
	pinnedTake, by, pinnAt *string
	evidence               string
	exists                 bool
}

func loadPickTx(ctx context.Context, tx *sql.Tx, songID string) (storedPick, error) {
	var p storedPick
	var take sql.NullString
	var src string
	err := tx.QueryRowContext(ctx, `SELECT take_id, source, pinned_take_id, pinned_by, pinned_at, evidence_json FROM song_picks WHERE song_id = ?`, songID).
		Scan(&take, &src, &p.pinnedTake, &p.by, &p.pinnAt, &p.evidence)
	if errors.Is(err, sql.ErrNoRows) {
		p.cur = current{Source: SourceNone}
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("picks: load pick: %w", err)
	}
	p.exists = true
	p.cur = current{Source: Source(src)}
	if take.Valid {
		p.cur.Take = &take.String
	}
	return p, nil
}

// loadInputTx gathers the Derive input of a Song (C91.5).
func loadInputTx(ctx context.Context, tx *sql.Tx, songID string, st map[string]ladder.Strength) ([]TakeInput, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.status, t.recorded_at, t.created_at,
		(SELECT r.stars FROM ratings r WHERE r.track_id = t.id AND r.source = 'human')
		FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.id`, songID)
	if err != nil {
		return nil, 0, fmt.Errorf("picks: load takes: %w", err)
	}
	defer rows.Close()
	var takes []TakeInput
	for rows.Next() {
		var t TakeInput
		var rec, cre string
		var stars sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Status, &rec, &cre, &stars); err != nil {
			return nil, 0, err
		}
		r, err := store.ParseInstant(rec)
		if err != nil {
			return nil, 0, fmt.Errorf("picks: recorded_at of %s: %w", t.ID, err)
		}
		c, err := store.ParseInstant(cre)
		if err != nil {
			return nil, 0, fmt.Errorf("picks: created_at of %s: %w", t.ID, err)
		}
		t.RecordedAt, t.CreatedAt = r.UnixMilli(), c.UnixMilli()
		if stars.Valid {
			n := int(stars.Int64)
			t.HumanStars = &n
		}
		if s, ok := st[t.ID]; ok {
			u, sd := s.U, s.SD
			t.U, t.SD = &u, &sd
		}
		takes = append(takes, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		WHERE v.source = 'human' AND d.blind = 1
		  AND d.slot_a_track_id IN (SELECT track_id FROM take_songs WHERE song_id = ?)
		  AND d.slot_b_track_id IN (SELECT track_id FROM take_songs WHERE song_id = ?)`, songID, songID).Scan(&n); err != nil {
		return nil, 0, fmt.Errorf("picks: intra duels: %w", err)
	}
	return takes, n, nil
}

// reasonFor is the event reason table of C91.2.
func reasonFor(prev, next current, prevPinned *string, op *pinOp, pinIneligible bool) string {
	switch {
	case next.Source == SourcePinned:
		if op != nil && op.Set {
			return "pin"
		}
		if prevPinned != nil && next.Take != nil && *prevPinned == *next.Take {
			return "pin_restored"
		}
		return "pin"
	case prev.Source == SourcePinned:
		if op != nil && !op.Set {
			return "unpin"
		}
		_ = pinIneligible
		return "pin_ineligible"
	case next.Source == SourceNone:
		return "emptied"
	case prev.Source == SourceNone:
		return "initial"
	}
	return string(next.Source)
}

// recomputeSongTx recomputes one Song, optionally applying an explicit pin operation first. changed reports whether the
// pin columns or the (take, source) pair changed.
func (s *Service) recomputeSongTx(ctx context.Context, tx *sql.Tx, env store.Env, songID string, st map[string]ladder.Strength, op *pinOp, ch *Changes) (changed bool, err error) {
	prev, err := loadPickTx(ctx, tx, songID)
	if err != nil {
		return false, err
	}
	pin := pinFacts{TakeID: prev.pinnedTake, By: prev.by, At: prev.pinnAt}
	var proposalID *string
	switch {
	case op != nil && op.Set:
		now := store.FormatTime(env.Now)
		take, by := op.Take, op.By.Key
		pin = pinFacts{TakeID: &take, By: &by, At: &now}
		proposalID = op.ProposalID
	case op != nil:
		pin = pinFacts{}
	}
	takes, intra, err := loadInputTx(ctx, tx, songID, st)
	if err != nil {
		return false, err
	}
	in := Input{Takes: takes, PinnedTakeID: pin.TakeID, IntraDuels: intra}
	if prev.cur.Source == SourceLadder {
		in.IncumbentTakeID = prev.cur.Take
	}
	res := Derive(in)
	next := current{Take: res.TakeID, Source: res.Source}

	if proposalID == nil && prev.cur.Source == SourcePinned && next.equal(prev.cur) {
		proposalID = storedProposalID(prev.evidence)
	}
	evidence, err := finalEvidence(res, pin, proposalID)
	if err != nil {
		return false, err
	}
	now := store.FormatTime(env.Now)
	pinChanged := !samePtr(prev.pinnedTake, pin.TakeID)

	if !prev.exists && next.Source == SourceNone {
		return false, nil // no eligible take and no row: nothing to record
	}
	if prev.exists && next.equal(prev.cur) {
		if _, err := tx.ExecContext(ctx, `UPDATE song_picks SET evidence_json = ?, computed_at = ?, pinned_take_id = ?, pinned_by = ?, pinned_at = ? WHERE song_id = ?`,
			string(evidence), now, pin.TakeID, pin.By, pin.At, songID); err != nil {
			return false, fmt.Errorf("picks: refresh pick: %w", err)
		}
		ch.Refreshed++
		return pinChanged, nil
	}

	a := systemActorRow
	reason := reasonFor(prev.cur, next, prev.pinnedTake, op, res.PinIneligible)
	if op != nil && (reason == "pin" || reason == "unpin") {
		a = op.By
	}
	eid, _, err := insertEventTx(ctx, tx, env, songID, prev.cur, next, reason, a, evidence)
	if err != nil {
		return false, err
	}
	if prev.exists {
		_, err = tx.ExecContext(ctx, `UPDATE song_picks SET take_id = ?, source = ?, pinned_take_id = ?, pinned_by = ?, pinned_at = ?, evidence_json = ?,
			last_event_id = ?, changed_at = ?, computed_at = ? WHERE song_id = ?`,
			next.Take, string(next.Source), pin.TakeID, pin.By, pin.At, string(evidence), eid, now, now, songID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO song_picks(song_id,take_id,source,pinned_take_id,pinned_by,pinned_at,evidence_json,last_event_id,changed_at,computed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, songID, next.Take, string(next.Source), pin.TakeID, pin.By, pin.At, string(evidence), eid, now, now)
	}
	if err != nil {
		return false, fmt.Errorf("picks: write pick: %w", err)
	}
	ch.Events++
	ch.Songs = append(ch.Songs, songID)
	return true, nil
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// expireProposalsTx closes the open proposals whose take is ineligible or is the current Pick (C91.5).
func expireProposalsTx(ctx context.Context, tx *sql.Tx, env store.Env, songID string) (int, error) {
	res, err := tx.ExecContext(ctx, `UPDATE pick_proposals SET state = 'expired', decided_at = ?, decided_by = ?
		WHERE song_id = ? AND state = 'open'
		  AND (take_id IN (SELECT id FROM tracks WHERE status = 'discarded')
		       OR take_id IS (SELECT take_id FROM song_picks WHERE song_id = ?))`,
		store.FormatTime(env.Now), systemActor, songID, songID)
	if err != nil {
		return 0, fmt.Errorf("picks: expire proposals: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RecomputeTx recomputes the Picks of the Songs inside the Reconciler's transaction (C91.5).
func (s *Service) RecomputeTx(ctx context.Context, tx *sql.Tx, env Env, songIDs []string, st map[string]ladder.Strength) (Changes, error) {
	var ch Changes
	for _, id := range songIDs {
		if _, err := s.recomputeSongTx(ctx, tx, env, id, st, nil, &ch); err != nil {
			return ch, err
		}
		n, err := expireProposalsTx(ctx, tx, env, id)
		if err != nil {
			return ch, err
		}
		ch.Expired += n
	}
	return ch, nil
}

// env builds the Env of the service's own transactions (pin, sweep).
func (s *Service) env() store.Env {
	return store.Env{Now: s.now(), NewID: s.newID, Actor: systemActor}
}

// strengthsFor fetches the ladder strengths of every take of songIDs outside any write transaction.
func (s *Service) strengthsFor(ctx context.Context, songIDs []string) (map[string]ladder.Strength, error) {
	if s.l == nil || len(songIDs) == 0 {
		return nil, nil
	}
	var ids []string
	for _, song := range songIDs {
		rows, err := s.st.R.QueryContext(ctx, `SELECT track_id FROM take_songs WHERE song_id = ?`, song)
		if err != nil {
			return nil, fmt.Errorf("picks: takes of song: %w", err)
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				_ = rows.Close()
				return nil, err
			}
			ids = append(ids, t)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	st, _, err := s.l.Strengths(ctx, ids)
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return nil, nil
		}
		return nil, fmt.Errorf("picks: ladder strengths: %w", err)
	}
	return st, nil
}

const sweepSQL = `SELECT s.id FROM songs s WHERE s.retired_at IS NULL
 AND (SELECT COUNT(*) FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = s.id AND t.status <> 'discarded') >= 2
 AND EXISTS (SELECT 1 FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id WHERE v.source = 'human' AND d.blind = 1
   AND d.slot_a_track_id IN (SELECT track_id FROM take_songs WHERE song_id = s.id)
   AND d.slot_b_track_id IN (SELECT track_id FROM take_songs WHERE song_id = s.id))
 ORDER BY s.id`

// Sweep recomputes every live Song with >= 2 eligible takes and >= 1 counted human verdict (C91.5).
func (s *Service) Sweep(ctx context.Context) (Changes, error) {
	var total Changes
	if s.st == nil {
		return total, errUnconfigured
	}
	rows, err := s.st.R.QueryContext(ctx, sweepSQL)
	if err != nil {
		return total, fmt.Errorf("picks: sweep list: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return total, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return total, err
	}
	for batch := range slices.Chunk(ids, BatchKeys) {
		st, err := s.strengthsFor(ctx, batch)
		if err != nil {
			return total, err
		}
		ch, err := s.recomputeBatch(ctx, batch, st)
		total.Events += ch.Events
		total.Refreshed += ch.Refreshed
		total.Expired += ch.Expired
		total.Songs = append(total.Songs, ch.Songs...)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s *Service) recomputeBatch(ctx context.Context, ids []string, st map[string]ladder.Strength) (Changes, error) {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Changes{}, fmt.Errorf("picks: sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ch, err := s.RecomputeTx(ctx, tx, s.env(), ids, st)
	if err != nil {
		return Changes{}, err
	}
	if err := tx.Commit(); err != nil {
		return Changes{}, fmt.Errorf("picks: sweep commit: %w", err)
	}
	return ch, nil
}

// Get reads the Pick of a Song (C91.4).
func (s *Service) Get(ctx context.Context, songID string) (PickView, error) {
	if s.st == nil {
		return PickView{}, errUnconfigured
	}
	return s.view(ctx, s.st.R, songID)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) view(ctx context.Context, q queryer, songID string) (PickView, error) {
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM songs WHERE id = ?`, songID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PickView{}, notFound(songID)
		}
		return PickView{}, fmt.Errorf("picks: song: %w", err)
	}
	v := PickView{Source: SourceNone}
	var src, ev string
	var pinnedBy *string
	err := q.QueryRowContext(ctx, `SELECT p.take_id, p.source, p.pinned_take_id, p.pinned_by, p.pinned_at, p.evidence_json, p.changed_at, p.last_event_id,
		(SELECT e.seq FROM pick_events e WHERE e.id = p.last_event_id)
		FROM song_picks p WHERE p.song_id = ?`, songID).Scan(&v.TakeID, &src, &v.PinnedTakeID, &pinnedBy, &v.PinnedAt, &ev, &v.ChangedAt, &v.LastEventID, &v.Seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v.Evidence = json.RawMessage(`{"def":"` + EvidenceDef + `","rule":"none","eligible":0}`)
	case err != nil:
		return PickView{}, fmt.Errorf("picks: read pick: %w", err)
	default:
		v.Source = Source(src)
		v.Evidence = json.RawMessage(ev)
		if pinnedBy != nil {
			v.PinnedBy = pinnedBy
			kind, id, _ := strings.Cut(*pinnedBy, ":")
			name := id
			var an sql.NullString
			if err := q.QueryRowContext(ctx, `SELECT actor_name FROM pick_events WHERE song_id = ? AND actor = ? AND reason = 'pin' ORDER BY seq DESC LIMIT 1`, songID, *pinnedBy).Scan(&an); err == nil && an.Valid {
				name = an.String
			}
			v.PinnedByKind, v.PinnedByName = &kind, &name
		}
		var e struct {
			PinIneligible *PinIneligibleEvidence `json:"pin_ineligible"`
		}
		_ = json.Unmarshal([]byte(ev), &e)
		v.PinIneligible = e.PinIneligible != nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM provenance_dirty d JOIN take_songs ts ON ts.track_id = d.id WHERE d.kind = 'track' AND ts.song_id = ?),
		(SELECT COUNT(*) FROM pick_proposals WHERE song_id = ? AND state = 'open')`, songID, songID).Scan(&v.Pending, &v.OpenProposals); err != nil {
		return PickView{}, fmt.Errorf("picks: pending: %w", err)
	}
	return v, nil
}
