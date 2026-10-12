package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ImportRunSummary describes one import run.
type ImportRunSummary struct {
	ID          string         `json:"id"`
	StartedAt   string         `json:"started_at"`
	HeartbeatAt string         `json:"heartbeat_at"`
	FinishedAt  *string        `json:"finished_at"`
	State       string         `json:"state"`
	InboxLabel  string         `json:"inbox_label"`
	PlanDigest  string         `json:"plan_digest"`
	Summary     map[string]any `json:"summary" nullable:"true"`
	Errors      int            `json:"errors"`
	Warnings    int            `json:"warnings"`
}

// ImportRunPage is one page of one report section.
type ImportRunPage struct {
	Run        ImportRunSummary `json:"run"`
	Section    string           `json:"section"`
	Items      []any            `json:"items" nullable:"false"`
	Total      int              `json:"total"`
	NextCursor *string          `json:"next_cursor"`
}

// ImportSections are the report sections get_import_run can page through.
var ImportSections = []string{"errors", "warnings", "unmatched", "ambiguous", "audio", "recipes", "drafts", "duplicates", "ignored"}

const runCols = `id, started_at, heartbeat_at, finished_at, state, inbox_label, plan_digest, summary_json`

type rowScanner interface{ Scan(...any) error }

func scanRun(r rowScanner) (ImportRunSummary, error) {
	var x ImportRunSummary
	var fin, sum sql.NullString
	if err := r.Scan(&x.ID, &x.StartedAt, &x.HeartbeatAt, &fin, &x.State, &x.InboxLabel, &x.PlanDigest, &sum); err != nil {
		return x, err
	}
	x.FinishedAt = ptrStr(fin)
	if sum.Valid {
		var m map[string]any
		if json.Unmarshal([]byte(sum.String), &m) == nil {
			x.Summary = m
			x.Errors = intOf(m["errors"])
			x.Warnings = intOf(m["warnings"])
		}
	}
	return x, nil
}

func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// ImportRuns lists runs, newest first.
func (s *Service) ImportRuns(ctx context.Context, limit int) ([]ImportRunSummary, error) {
	limit = clampLimit(limit, 50, 200)
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+runCols+` FROM import_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ImportRunSummary{}
	for rows.Next() {
		x, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ImportRun pages through one section of a run's report. The section default
// is "errors"; the full inventory is never returned at once.
func (s *Service) ImportRun(ctx context.Context, id, section, cursor string, limit int) (ImportRunPage, error) {
	if section == "" {
		section = "errors"
	}
	if !oneOf(section, ImportSections...) {
		return ImportRunPage{}, invalid("section", "unknown section %q", section)
	}
	off, err := decodeCursor(cursor)
	if err != nil {
		return ImportRunPage{}, err
	}
	limit = clampLimit(limit, 50, 200)
	var report sql.NullString
	run, err := func() (ImportRunSummary, error) {
		row := s.st.R.QueryRowContext(ctx, `SELECT `+runCols+`, report_json FROM import_runs WHERE id = ?`, id)
		var x ImportRunSummary
		var fin, sum sql.NullString
		if err := row.Scan(&x.ID, &x.StartedAt, &x.HeartbeatAt, &fin, &x.State, &x.InboxLabel, &x.PlanDigest, &sum, &report); err != nil {
			return x, err
		}
		x.FinishedAt = ptrStr(fin)
		if sum.Valid {
			var m map[string]any
			if json.Unmarshal([]byte(sum.String), &m) == nil {
				x.Summary, x.Errors, x.Warnings = m, intOf(m["errors"]), intOf(m["warnings"])
			}
		}
		return x, nil
	}()
	if errors.Is(err, sql.ErrNoRows) {
		return ImportRunPage{}, ErrNotFound
	}
	if err != nil {
		return ImportRunPage{}, fmt.Errorf("library: import run: %w", err)
	}
	page := ImportRunPage{Run: run, Section: section, Items: []any{}}
	if !report.Valid {
		return page, nil
	}
	key, status := section, ""
	if section == "unmatched" || section == "ambiguous" {
		key, status = "audio", section
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(report.String), &top); err != nil {
		return page, fmt.Errorf("library: report: %w", err)
	}
	var all []json.RawMessage
	if raw, ok := top[key]; ok {
		if err := json.Unmarshal(raw, &all); err != nil {
			return page, fmt.Errorf("library: report section: %w", err)
		}
	}
	if status != "" {
		kept := all[:0:0]
		for _, raw := range all {
			var st struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &st) == nil && st.Status == status {
				kept = append(kept, raw)
			}
		}
		all = kept
	}
	page.Total = len(all)
	pg := newPager(page)
	i := off
	for ; i < len(all) && len(page.Items) < limit; i++ {
		var v any
		if err := json.Unmarshal(all[i], &v); err != nil {
			return page, fmt.Errorf("library: report item: %w", err)
		}
		// measure the bytes actually sent (the decoded value), not the stored raw
		if !pg.add(sizeOf(v)) {
			break
		}
		page.Items = append(page.Items, v)
	}
	if i < len(all) {
		c := encodeCursor(i)
		page.NextCursor = &c
	}
	return page, nil
}
