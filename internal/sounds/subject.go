package sounds

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Querier is the read side of a transaction or a pool.
type Querier = querier

// Subject kinds.
const (
	SubjectTake  = "take"
	SubjectDraft = "draft"
	SubjectNone  = "none"
)

// Subject is the representative of a Song: the one Take (or the draft) its derived facts come from.
type Subject struct {
	Kind string // SubjectTake | SubjectDraft | SubjectNone
	ID   string // the track id or the draft id; empty for none
}

// subject is Subject with the compute input and digest of a loaded representative.
type subject struct {
	Kind   string // take | draft | none
	ID     string
	In     input
	Digest string
}

// SubjectOf is the one rule that says which Take (or draft) represents a Song: its Pick take, else its newest
// non-discarded take (recorded_at DESC, id DESC), else the draft it was created from, else none. The Sound
// compute and every other consumer of "the Song's subject" use it, so the two can never drift (D-L3-39). It does not
// look at retirement: callers skip retired Songs.
func SubjectOf(ctx context.Context, q Querier, songID string) (Subject, error) {
	takeID, draft, err := pickSubject(ctx, q, songID)
	if err != nil {
		return Subject{}, err
	}
	switch {
	case takeID != "":
		return Subject{Kind: SubjectTake, ID: takeID}, nil
	case draft != nil:
		return Subject{Kind: SubjectDraft, ID: draft.ID}, nil
	}
	return Subject{Kind: SubjectNone}, nil
}

// draftRow is the draft a Song was created from.
type draftRow struct {
	ID, Request string
	Revision    int
}

// pickSubject applies the rule: a take id, or else the Song's draft, or neither.
func pickSubject(ctx context.Context, q querier, songID string) (takeID string, draft *draftRow, err error) {
	var t sql.NullString
	err = q.QueryRowContext(ctx, `SELECT take_id FROM song_picks WHERE song_id = ? AND take_id IS NOT NULL`, songID).Scan(&t)
	if err != nil && err != sql.ErrNoRows {
		return "", nil, fmt.Errorf("sounds: song pick: %w", err)
	}
	if !t.Valid {
		err = q.QueryRowContext(ctx, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id
			WHERE ts.song_id = ? AND t.status <> 'discarded' ORDER BY t.recorded_at DESC, t.id DESC LIMIT 1`, songID).Scan(&t)
		if err != nil && err != sql.ErrNoRows {
			return "", nil, fmt.Errorf("sounds: newest take: %w", err)
		}
	}
	if t.Valid {
		return t.String, nil, nil
	}
	return draftOf(ctx, q, songID)
}

// draftOf reads the draft a Song was created from, if any.
func draftOf(ctx context.Context, q querier, songID string) (string, *draftRow, error) {
	var id, req sql.NullString
	var rev int
	err := q.QueryRowContext(ctx, `SELECT d.id, d.request_json, d.revision FROM songs s JOIN recipe_drafts d ON d.id = s.draft_id WHERE s.id = ?`, songID).Scan(&id, &req, &rev)
	if err != nil && err != sql.ErrNoRows {
		return "", nil, fmt.Errorf("sounds: song draft: %w", err)
	}
	if id.Valid {
		return "", &draftRow{ID: id.String, Request: req.String, Revision: rev}, nil
	}
	return "", nil, nil
}

// loadSubject is SubjectOf plus the compute input and digest of the representative.
func loadSubject(ctx context.Context, q querier, version int, songID string) (subject, error) {
	takeID, draft, err := pickSubject(ctx, q, songID)
	if err != nil {
		return subject{}, err
	}
	if takeID != "" {
		in, ok, err := loadTake(ctx, q, takeID)
		if err != nil {
			return subject{}, err
		}
		if ok {
			return subject{Kind: SubjectTake, ID: takeID, In: in, Digest: takeDigest(version, in)}, nil
		}
		// A take that vanished between the two statements: the draft decides, as before.
		_, draft, err = draftOf(ctx, q, songID)
		if err != nil {
			return subject{}, err
		}
	}
	if draft != nil {
		var r struct {
			Style  string `json:"style"`
			Lyrics string `json:"lyrics"`
		}
		_ = json.Unmarshal([]byte(draft.Request), &r)
		in := input{Engine: "yue2", Style: r.Style, Lyrics: r.Lyrics}
		return subject{Kind: SubjectDraft, ID: draft.ID, In: in,
			Digest: sum256(jsonString(struct {
				Revision int    `json:"revision"`
				Style    string `json:"style"`
				Lyrics   string `json:"lyrics_sha256"`
			}{draft.Revision, in.Style, sum256(in.Lyrics)}))}, nil
	}
	return subject{Kind: SubjectNone, Digest: sum256("none")}, nil
}
