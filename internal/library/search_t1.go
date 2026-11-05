package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// maxLyricsOf bounds the partners lyrics_of returns (C211.4).
const maxLyricsOf = 200

// searchOrigins are the values of the origin filter. import and job are the stored meaning; transform is derived (D-T1-4):
// the Take has a transform_id, and job then means a generation job (job_id set), never a transform.
var searchOrigins = []string{"import", "job", "transform"}

// originWhere is the predicate of an origin filter (OR across the listed origins).
func originWhere(origins []string) string {
	parts := make([]string, 0, len(origins))
	for _, o := range origins {
		switch o {
		case "import":
			parts = append(parts, `(t.origin = 'import')`)
		case "job":
			parts = append(parts, `(t.job_id IS NOT NULL)`)
		case "transform":
			parts = append(parts, `(t.transform_id IS NOT NULL)`)
		}
	}
	return ` AND (` + strings.Join(parts, ` OR `) + `)`
}

// NormalizeLyrics is the lyrics equality of lyrics_of: CRLF and CR become LF, trailing whitespace of each line and the
// leading and trailing whitespace of the whole text are dropped.
func NormalizeLyrics(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\f\v")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// resolveLyricsOf turns p.LyricsOf into an id list on p: the lossless, non-reference, non-transform Takes (the target
// itself excluded) whose normalised lyrics equal the target's. Empty or whitespace-only lyrics and a transform target
// match nothing, because every instrumental would otherwise match every other.
func (s *Service) resolveLyricsOf(ctx context.Context, p *SearchParams) error {
	if p.LyricsOf == "" {
		return nil
	}
	p.lyricsSet = true
	var engine, lyrics string
	switch err := s.st.R.QueryRowContext(ctx, `SELECT r.engine, r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, p.LyricsOf).Scan(&engine, &lyrics); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("library: lyrics_of target: %w", err)
	}
	want := NormalizeLyrics(lyrics)
	if engine == EngineDSP || want == "" {
		p.None = true
		return nil
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT t.id, r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.id <> ? AND t.lossy = 0 AND r.engine NOT IN ('dsp', 'unknown') AND r.lyrics <> '' ORDER BY t.created_at, t.id`, p.LyricsOf)
	if err != nil {
		return fmt.Errorf("library: lyrics_of candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, l string
		if err := rows.Scan(&id, &l); err != nil {
			return fmt.Errorf("library: lyrics_of candidates: %w", err)
		}
		if NormalizeLyrics(l) == want {
			p.lyricsIDs = append(p.lyricsIDs, id)
			if len(p.lyricsIDs) == maxLyricsOf {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(p.lyricsIDs) == 0 {
		p.None = true
	}
	return nil
}

// lyricsWhere restricts the search to the ids resolveLyricsOf found.
func lyricsWhere(p SearchParams, args *[]any) string {
	if !p.lyricsSet || p.None {
		return ""
	}
	for _, id := range p.lyricsIDs {
		*args = append(*args, id)
	}
	return ` AND t.id IN (` + placeholders(len(p.lyricsIDs)) + `)`
}
