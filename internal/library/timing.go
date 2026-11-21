package library

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/anchor"
)

// Timing is the timed section list of a take as the anchor leaf wants it (C252): the rows of the highest-ranked
// source (manual > measured > plan) through selectSections, the one selection rule of Track and Sections. A take
// whose best source is `lyrics` (untimed; the only untimed source) or that has no sections gets an empty Sections
// slice and an empty Source. Ordinals count the occurrences of each kind in section order, 1-based. DurationS is
// the take's duration. ErrNotFound for an unknown track. q is a *sql.DB or *sql.Tx.
func Timing(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, trackID string) (anchor.Timing, error) {
	dur, bySource, err := timingSources(ctx, q, trackID)
	out := anchor.Timing{DurationS: dur}
	if err != nil {
		return out, err
	}
	best, sel, timed := selectSections(bySource)
	if best == "" || !timed {
		return out, nil
	}
	return timingOf(dur, best, sel), nil
}

// TimingChain is every fully timed source of a take as a Timing, the best first (manual > measured > plan). Timing answers
// with the best source alone; a caller that maps a moment onto lyric sections and must not lose it to a gap of the best
// source (a measured take starts at its first sung word, so its intro belongs to no section) walks the chain instead.
// ErrNotFound for an unknown track.
func TimingChain(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, trackID string) ([]anchor.Timing, error) {
	dur, bySource, err := timingSources(ctx, q, trackID)
	if err != nil {
		return nil, err
	}
	var out []anchor.Timing
	for _, src := range []string{"manual", "measured", "plan"} {
		rows := bySource[src]
		timed := len(rows) > 0
		for _, so := range rows {
			if so.StartS == nil || so.EndS == nil {
				timed = false
			}
		}
		if timed {
			out = append(out, timingOf(dur, src, rows))
		}
	}
	return out, nil
}

// TimingWithChain is Timing and TimingChain in one read: the best source as Timing returns it (it carries the duration of an
// untimed take too) and every fully timed source, best first. For callers that map moments with anchor.ComputeChain.
func TimingWithChain(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, trackID string) (anchor.Timing, []anchor.Timing, error) {
	best, err := Timing(ctx, q, trackID)
	if err != nil {
		return best, nil, err
	}
	chain, err := TimingChain(ctx, q, trackID)
	return best, chain, err
}

func timingOf(dur float64, src string, sel []SectionOut) anchor.Timing {
	out := anchor.Timing{DurationS: dur, Source: src}
	ordinal := map[string]int{}
	for _, so := range sel {
		ordinal[so.Kind]++
		out.Sections = append(out.Sections, anchor.Section{Kind: so.Kind, Ordinal: ordinal[so.Kind], Label: so.Label, StartS: *so.StartS, EndS: *so.EndS})
	}
	return out
}

// timingSources reads the take's duration and its section rows by source.
func timingSources(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, trackID string) (float64, map[string][]SectionOut, error) {
	var out anchor.Timing
	rows, err := q.QueryContext(ctx, `SELECT t.duration_s, s.source, s.idx, s.kind, s.label, s.raw_label, s.start_s, s.end_s
		FROM tracks t LEFT JOIN sections s ON s.track_id = t.id WHERE t.id = ? ORDER BY s.idx`, trackID)
	if err != nil {
		return 0, nil, fmt.Errorf("library: timing: %w", err)
	}
	defer rows.Close()
	bySource := map[string][]SectionOut{}
	found := false
	for rows.Next() {
		var dur float64
		var src, kind, label, raw sql.NullString
		var idx sql.NullInt64
		var st, en sql.NullFloat64
		if err := rows.Scan(&dur, &src, &idx, &kind, &label, &raw, &st, &en); err != nil {
			return 0, nil, fmt.Errorf("library: timing: %w", err)
		}
		found, out.DurationS = true, dur
		if !src.Valid {
			continue
		}
		so := SectionOut{Idx: int(idx.Int64), Kind: kind.String, Label: label.String, Raw: raw.String, Source: src.String}
		if st.Valid && en.Valid {
			so.StartS, so.EndS = &st.Float64, &en.Float64
		}
		bySource[so.Source] = append(bySource[so.Source], so)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("library: timing: %w", err)
	}
	if !found {
		return 0, nil, ErrNotFound
	}
	return out.DurationS, bySource, nil
}
