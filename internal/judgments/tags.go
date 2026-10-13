package judgments

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

var tagPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} &'+/._-]*$`)

const (
	maxTagsPerCall  = 20
	maxTagsPerTrack = 30
)

// NormalizeTag returns the canonical form of a tag: Unicode NFC, lower case,
// trimmed, whitespace runs collapsed to one space, 1..40 runes of letters,
// digits and ` &'+/._-` (not starting with a symbol). Failures are INVALID_TAG.
func NormalizeTag(s string) (string, error) {
	t := norm.NFC.String(strings.ToLower(norm.NFC.String(s)))
	t = strings.Join(strings.Fields(t), " ")
	if n := utf8.RuneCountInString(t); n < 1 || n > 40 {
		return "", &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_TAG", Msg: "a tag is 1 to 40 characters", Field: "tags"}
	}
	if !tagPattern.MatchString(t) {
		return "", &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_TAG", Msg: "a tag holds letters, digits and the characters & ' + / . _ - only", Field: "tags"}
	}
	return t, nil
}

func normalizeAll(field string, in []string) ([]string, error) {
	if len(in) > maxTagsPerCall {
		return nil, inv(field, "at most %d tags per call", maxTagsPerCall)
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		n, err := NormalizeTag(raw)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// SetTags adds and removes tags. A human or device remove wins over every
// source; an agent only removes its own. An agent add never creates a human row.
func (s *Service) SetTags(ctx context.Context, p auth.Principal, trackID string, add, remove []string) (TrackTags, error) {
	if len(add) == 0 && len(remove) == 0 {
		return TrackTags{}, inv("body", "give at least one tag to add or remove")
	}
	adds, err := normalizeAll("add", add)
	if err != nil {
		return TrackTags{}, err
	}
	rems, err := normalizeAll("remove", remove)
	if err != nil {
		return TrackTags{}, err
	}
	if err := s.requireTrack(ctx, trackID); err != nil {
		return TrackTags{}, err
	}
	source, removeFrom := "agent", `('agent')`
	if p.ActsAsHuman() {
		source, removeFrom = "human", `('human','inherited','agent')`
	}
	now := s.stamp()
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return TrackTags{}, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, name := range rems {
		var id int64
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, name).Scan(&id); {
		case err == sql.ErrNoRows:
			continue
		case err != nil:
			return TrackTags{}, fmt.Errorf("judgments: tag lookup: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE track_tags SET removed_at=?, removed_by=?, updated_at=? WHERE track_id=? AND tag_id=? AND source IN `+removeFrom+` AND removed_at IS NULL`,
			now, p.Key(), now, trackID, id); err != nil {
			return TrackTags{}, fmt.Errorf("judgments: remove tag: %w", err)
		}
	}
	for _, name := range adds {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags(name,created_at) VALUES(?,?) ON CONFLICT(name) DO NOTHING`, name, now); err != nil {
			return TrackTags{}, fmt.Errorf("judgments: create tag: %w", err)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name=?`, name).Scan(&id); err != nil {
			return TrackTags{}, fmt.Errorf("judgments: tag lookup: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_tags(track_id,tag_id,source,inherited_from,principal_id,created_at,updated_at)
			VALUES(?,?,?,NULL,?,?,?) ON CONFLICT(track_id,tag_id,source) DO UPDATE SET removed_at=NULL, removed_by=NULL,
			principal_id=excluded.principal_id, updated_at=excluded.updated_at WHERE track_tags.removed_at IS NOT NULL`,
			trackID, id, source, p.Key(), now, now); err != nil {
			return TrackTags{}, fmt.Errorf("judgments: add tag: %w", err)
		}
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_tags WHERE track_id=? AND source=? AND removed_at IS NULL`, trackID, source).Scan(&active); err != nil {
		return TrackTags{}, err
	}
	if active > maxTagsPerTrack {
		return TrackTags{}, inv("add", "a track holds at most %d active tags per source", maxTagsPerTrack)
	}
	if err := tx.Commit(); err != nil {
		return TrackTags{}, err
	}
	return s.Tags(ctx, trackID)
}

// Tags returns a track's effective tags.
func (s *Service) Tags(ctx context.Context, trackID string) (TrackTags, error) {
	if err := s.requireTrack(ctx, trackID); err != nil {
		return TrackTags{}, err
	}
	return s.lib.Tags(ctx, trackID)
}

// TagCounts lists every active tag with its track counts, most used first.
func (s *Service) TagCounts(ctx context.Context) ([]TagCount, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT g.name, COUNT(DISTINCT tt.track_id),
		COUNT(DISTINCT CASE WHEN tt.source='human' THEN tt.track_id END),
		COUNT(DISTINCT CASE WHEN tt.source='agent' THEN tt.track_id END),
		COUNT(DISTINCT CASE WHEN tt.source='inherited' THEN tt.track_id END)
		FROM track_tags tt JOIN tags g ON g.id = tt.tag_id WHERE tt.removed_at IS NULL
		GROUP BY g.id ORDER BY 2 DESC, g.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("judgments: tag counts: %w", err)
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var c TagCount
		if err := rows.Scan(&c.Name, &c.Tracks, &c.BySource.Human, &c.BySource.Agent, &c.BySource.Inherited); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
