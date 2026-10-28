package crates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const crateColumns = `id, name, description, kind, subject, filter_json, revision, created_by, created_at, updated_by, updated_at, hidden_at`

type rowScanner interface{ Scan(...any) error }

func scanCrate(r rowScanner) (Crate, error) {
	var c Crate
	var kind, subject string
	var filter, hidden sql.NullString
	if err := r.Scan(&c.ID, &c.Name, &c.Description, &kind, &subject, &filter, &c.Revision, &c.CreatedBy, &c.CreatedAt, &c.UpdatedBy, &c.UpdatedAt, &hidden); err != nil {
		return Crate{}, err
	}
	c.Kind, c.Subject = Kind(kind), Subject(subject)
	if filter.Valid {
		c.Filter = json.RawMessage(filter.String)
	}
	if hidden.Valid {
		c.HiddenAt = &hidden.String
	}
	return c, nil
}

func notFound() error { return problem.E(http.StatusNotFound, "NOT_FOUND", "no such crate", nil) }

func invalidField(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

// cleanText trims and collapses whitespace and refuses control characters; max is in characters.
func cleanText(field, v string, lo, hi int) (string, error) {
	v = strings.Join(strings.Fields(v), " ")
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", invalidField(field, "must not contain control characters")
		}
	}
	if n := utf8.RuneCountInString(v); n < lo || n > hi {
		return "", invalidField(field, fmt.Sprintf("%d to %d characters", lo, hi))
	}
	return v, nil
}

func (s *Service) stamp() string { return store.FormatTime(s.now()) }

func (s *Service) id() string {
	if s.newID != nil {
		return s.newID()
	}
	return uuid.NewV7().String()
}

func (s *Service) load(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Crate, error) {
	c, err := scanCrate(q.QueryRowContext(ctx, `SELECT `+crateColumns+` FROM crates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Crate{}, notFound()
	}
	if err != nil {
		return Crate{}, fmt.Errorf("crates: read: %w", err)
	}
	return c, nil
}

// Get returns a Crate with its count.
func (s *Service) Get(ctx context.Context, id string) (Crate, int, error) {
	c, err := s.load(ctx, s.st.R, id)
	if err != nil {
		return Crate{}, 0, err
	}
	n, err := s.count(ctx, c)
	return c, n, err
}

// Create makes a Crate and returns it with its count (manual: items; smart: the matches now).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewCrate) (Crate, int, error) {
	name, err := cleanText("name", in.Name, 1, MaxName)
	if err != nil {
		return Crate{}, 0, err
	}
	desc, err := cleanText("description", in.Description, 0, MaxDescription)
	if err != nil {
		return Crate{}, 0, err
	}
	if in.Subject == "" {
		in.Subject = SubjectSong
	}
	if in.Subject != SubjectSong && in.Subject != SubjectTake {
		return Crate{}, 0, invalidField("subject", "must be song or take")
	}
	var filter any
	switch in.Kind {
	case KindManual:
		if len(in.Filter) > 0 {
			return Crate{}, 0, invalidField("filter", "a manual Crate has no filter")
		}
		if len(in.Items) > MaxEditBatch {
			return Crate{}, 0, invalidField("items", fmt.Sprintf("at most %d items on create", MaxEditBatch))
		}
	case KindSmart:
		if len(in.Items) > 0 {
			return Crate{}, 0, problem.E(http.StatusConflict, "CRATE_IS_SMART", "a smart Crate has no items", nil)
		}
		if len(in.Filter) == 0 {
			return Crate{}, 0, invalidField("filter", "a smart Crate needs a filter")
		}
		if _, err := ParseFilter(in.Subject, in.Filter); err != nil {
			return Crate{}, 0, err
		}
		filter = string(in.Filter)
	default:
		return Crate{}, 0, invalidField("kind", "must be manual or smart")
	}
	members, err := s.checkMembers(ctx, s.st.R, in.Subject, in.Items, true)
	if err != nil {
		return Crate{}, 0, err
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Crate{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, now := s.id(), s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO crates(id,name,description,kind,subject,filter_json,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, name, desc, string(in.Kind), string(in.Subject), filter, p.Key(), now, p.Key(), now); err != nil {
		return Crate{}, 0, fmt.Errorf("crates: create: %w", err)
	}
	for _, m := range members {
		if err := s.insertItem(ctx, tx, id, m, p, now); err != nil {
			return Crate{}, 0, err
		}
	}
	c, err := s.load(ctx, tx, id)
	if err != nil {
		return Crate{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return Crate{}, 0, err
	}
	n, err := s.count(ctx, c)
	return c, n, err
}

// Update changes metadata or the filter under the revision lock and returns the Crate with its count.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Crate, int, error) {
	if patch.Name == nil && patch.Description == nil && len(patch.Filter) == 0 && patch.Hidden == nil {
		return Crate{}, 0, invalidField("body", "nothing to change")
	}
	var name, desc *string
	if patch.Name != nil {
		n, err := cleanText("name", *patch.Name, 1, MaxName)
		if err != nil {
			return Crate{}, 0, err
		}
		name = &n
	}
	if patch.Description != nil {
		d, err := cleanText("description", *patch.Description, 0, MaxDescription)
		if err != nil {
			return Crate{}, 0, err
		}
		desc = &d
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Crate{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := s.load(ctx, tx, id)
	if err != nil {
		return Crate{}, 0, err
	}
	if len(patch.Filter) > 0 {
		if cur.Kind != KindSmart {
			return Crate{}, 0, invalidField("filter", "only a smart Crate has a filter")
		}
		if _, err := ParseFilter(cur.Subject, patch.Filter); err != nil {
			return Crate{}, 0, err
		}
	}
	if cur.Revision != patch.Revision {
		return Crate{}, 0, problem.E(http.StatusConflict, "STALE_REVISION", "the Crate changed; re-read it and retry", map[string]any{"revision": cur.Revision})
	}
	if name != nil {
		cur.Name = *name
	}
	if desc != nil {
		cur.Description = *desc
	}
	var filter any
	if cur.Kind == KindSmart {
		filter = string(cur.Filter)
		if len(patch.Filter) > 0 {
			filter = string(patch.Filter)
		}
	}
	now := s.stamp()
	hiddenAt, hiddenBy := any(cur.HiddenAt), any(nil)
	if cur.HiddenAt != nil {
		hiddenBy = s.hiddenBy(ctx, tx, id)
	}
	if patch.Hidden != nil {
		if *patch.Hidden && cur.HiddenAt == nil {
			hiddenAt, hiddenBy = now, p.Key()
		} else if !*patch.Hidden {
			hiddenAt, hiddenBy = nil, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE crates SET name=?, description=?, filter_json=?, hidden_at=?, hidden_by=?, revision=revision+1, updated_by=?, updated_at=? WHERE id=? AND revision=?`,
		cur.Name, cur.Description, filter, hiddenAt, hiddenBy, p.Key(), now, id, patch.Revision); err != nil {
		return Crate{}, 0, fmt.Errorf("crates: update: %w", err)
	}
	c, err := s.load(ctx, tx, id)
	if err != nil {
		return Crate{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return Crate{}, 0, err
	}
	n, err := s.count(ctx, c)
	return c, n, err
}

func (s *Service) hiddenBy(ctx context.Context, tx *sql.Tx, id string) any {
	var by sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT hidden_by FROM crates WHERE id = ?`, id).Scan(&by)
	if by.Valid {
		return by.String
	}
	return nil
}
