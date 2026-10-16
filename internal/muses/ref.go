package muses

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Ref reads the text of a Muse as a MuseRef; the only way other packages read Muse text (C89, C99.2). A hidden Muse
// still resolves.
func (s *Service) Ref(ctx context.Context, id string) (MuseRef, bool, error) {
	var r MuseRef
	var artist sql.NullString
	err := s.st.R.QueryRowContext(ctx, `SELECT id, kind, title, artist FROM muses WHERE id = ?`, id).Scan(&r.ID, &r.Kind, &r.Title, &artist)
	if errors.Is(err, sql.ErrNoRows) {
		return MuseRef{}, false, nil
	}
	if err != nil {
		return MuseRef{}, false, err
	}
	if artist.Valid {
		r.Artist = &artist.String
	}
	return r, true, nil
}

// Refs resolves many Muses in one query; ids with no Muse are absent from the map.
func (s *Service) Refs(ctx context.Context, ids []string) (map[string]MuseRef, error) {
	out := make(map[string]MuseRef, len(ids))
	const chunk = 400
	for i := 0; i < len(ids); i += chunk {
		part := ids[i:min(i+chunk, len(ids))]
		args := make([]any, len(part))
		ph := ""
		for j, id := range part {
			args[j] = id
			if j > 0 {
				ph += ","
			}
			ph += "?"
		}
		rows, err := s.st.R.QueryContext(ctx, `SELECT id, kind, title, artist FROM muses WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r MuseRef
			var artist sql.NullString
			if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &artist); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if artist.Valid {
				r.Artist = &artist.String
			}
			out[r.ID] = r
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CheckAssignableTx is the check update_song makes before writing a Muse id: 422 UNKNOWN_MUSE for an unknown id, 409
// MUSE_HIDDEN for a hidden Muse. Songs may not name the muses table (C99.2), so the check lives here.
func CheckAssignableTx(ctx context.Context, tx *sql.Tx, id string) error {
	var hidden sql.NullString
	switch err := tx.QueryRowContext(ctx, `SELECT hidden_at FROM muses WHERE id = ?`, id).Scan(&hidden); {
	case errors.Is(err, sql.ErrNoRows):
		return problem.E(http.StatusUnprocessableEntity, "UNKNOWN_MUSE", "no such muse: "+id, map[string]any{"muse_id": id})
	case err != nil:
		return err
	}
	if hidden.Valid {
		return problem.E(http.StatusConflict, "MUSE_HIDDEN", "the muse is hidden", map[string]any{"muse_id": id})
	}
	return nil
}

type nullString struct {
	String string
	Valid  bool
}

func (n *nullString) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		n.Valid = false
	case string:
		n.String, n.Valid = x, true
	case []byte:
		n.String, n.Valid = string(x), true
	default:
		return errors.New("muses: unexpected column type")
	}
	return nil
}

func (n nullString) ptr() *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}
