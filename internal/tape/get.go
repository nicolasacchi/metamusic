package tape

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const tapeCols = `id, record_id, record_revision, state, params_json, plan_json, contains_lossy, lossy_sources, total_samples, bytes, sha256,
	path, filename, progress_json, error_code, error_message, created_at, started_at, finished_at`

type progressDoc struct {
	Phase string `json:"phase"`
	Item  int    `json:"item"`
	Items int    `json:"items"`
}

// Get returns one tape; the signed link of a ready tape is minted for the principal of ctx, and only while its file exists.
func (s *Service) Get(ctx context.Context, id string) (Tape, error) {
	if s.o.Store == nil {
		return Tape{}, errors.New("tape: no store")
	}
	row := s.o.Store.R.QueryRowContext(ctx, `SELECT `+tapeCols+` FROM tapes WHERE id = ?`, id)
	t, path, name, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Tape{}, errNotFound(id)
	}
	if err != nil {
		return Tape{}, err
	}
	if t.State == StateReady && s.o.Signer != nil && path != "" && t.SHA256 != nil && t.Bytes != nil {
		if s.fileExists(path) {
			ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
			l := s.o.Signer.Sign(*t.SHA256, "flac", ttl, bucket)
			t.Download = &Download{URL: l.URL, Filename: name, Bytes: *t.Bytes, ExpiresAt: l.Expires.UTC().Format(time.RFC3339)}
		}
	}
	return t, nil
}

func (s *Service) fileExists(rel string) bool {
	if s.o.Data == nil {
		return false
	}
	_, err := s.o.Data.Stat(rel)
	return err == nil
}

type rowScanner interface{ Scan(dest ...any) error }

// scan reads one tapes row into a Tape and returns the stored path and file name.
func (s *Service) scan(r rowScanner) (Tape, string, string, error) {
	var (
		t                                              Tape
		state, params, plan                            string
		lossy                                          int
		totalSamples, bytes                            sql.NullInt64
		sha, path, filename, progress, errCode, errMsg sql.NullString
		created                                        string
		started, finished                              sql.NullString
	)
	if err := r.Scan(&t.ID, &t.RecordID, &t.RecordRevision, &state, &params, &plan, &lossy, &t.LossySources, &totalSamples, &bytes, &sha,
		&path, &filename, &progress, &errCode, &errMsg, &created, &started, &finished); err != nil {
		return Tape{}, "", "", err
	}
	t.State, t.ContainsLossy, t.CreatedAt = State(state), lossy == 1, created
	var pd paramsDoc
	if err := json.Unmarshal([]byte(params), &pd); err != nil {
		return Tape{}, "", "", fmt.Errorf("tape: params of %s: %w", t.ID, err)
	}
	var doc planDoc
	if err := json.Unmarshal([]byte(plan), &doc); err != nil {
		return Tape{}, "", "", fmt.Errorf("tape: plan of %s: %w", t.ID, err)
	}
	t.TargetLUFS, t.AcceptedWarnings = doc.TargetLUFS, doc.AcceptedWarnings
	if doc.SpreadLU != nil {
		t.SpreadLU = &Spread{Before: doc.SpreadLU.Before, After: doc.SpreadLU.After}
	}
	t.Items = make([]ItemPlan, len(doc.Items))
	for i, it := range doc.Items {
		t.Items[i] = ItemPlan{SlotID: it.SlotID, TakeID: it.TakeID, Position: it.Position, GainCDB: it.GainCDB, Status: GainStatus(it.Status),
			PredictedLUFS: it.PredictedLUFS, Lossy: it.Lossy, Resampled: it.Resampled, Upmixed: it.Upmixed, Downmixed: it.Downmixed}
	}
	t.Warnings = make([]SlotWarning, len(doc.Warnings))
	for i, w := range doc.Warnings {
		t.Warnings[i] = SlotWarning{SlotID: w.SlotID, Code: w.Code}
	}
	if len(doc.Chapters) > 0 {
		t.Chapters = make([]Chapter, len(doc.Chapters))
		for i, c := range doc.Chapters {
			t.Chapters[i] = Chapter{Index: c.Index, SlotID: c.SlotID, TakeID: c.TakeID, StartSample: c.StartSample, Samples: c.Samples, StartMS: c.StartMS}
		}
	}
	if progress.Valid && t.State == StateRunning {
		var pg progressDoc
		if json.Unmarshal([]byte(progress.String), &pg) == nil {
			t.Progress = &Progress{Phase: pg.Phase, Item: pg.Item, Items: pg.Items}
		}
	}
	if totalSamples.Valid {
		ms := totalSamples.Int64 * 1000 / Rate
		t.TotalMS = &ms
	}
	if bytes.Valid {
		b := bytes.Int64
		t.Bytes = &b
	}
	if sha.Valid {
		v := sha.String
		t.SHA256 = &v
	}
	if errCode.Valid {
		t.Error = &Failure{Code: errCode.String, Message: errMsg.String}
	}
	if started.Valid {
		v := started.String
		t.StartedAt = &v
	}
	if finished.Valid {
		v := finished.String
		t.FinishedAt = &v
	}
	return t, path.String, filename.String, nil
}

// Brief returns the newest tapes of a Record, at most limit.
func (s *Service) Brief(ctx context.Context, recordID string, limit int) ([]Brief, error) {
	if s.o.Store == nil {
		return nil, errors.New("tape: no store")
	}
	if limit < 1 {
		return nil, nil
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, state, created_at, finished_at, contains_lossy, bytes FROM tapes
		WHERE record_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, recordID, limit)
	if err != nil {
		return nil, fmt.Errorf("tape: brief: %w", err)
	}
	defer rows.Close()
	var out []Brief
	for rows.Next() {
		var b Brief
		var state string
		var lossy int
		var fin sql.NullString
		var bytes sql.NullInt64
		if err := rows.Scan(&b.ID, &state, &b.CreatedAt, &fin, &lossy, &bytes); err != nil {
			return nil, err
		}
		b.State, b.ContainsLossy = State(state), lossy == 1
		if fin.Valid {
			v := fin.String
			b.FinishedAt = &v
			if b.State == StateReady {
				if ft, err := store.ParseInstant(v); err == nil {
					e := store.FormatTime(ft.AddDate(0, 0, RetentionDays))
					b.ExpiresAt = &e
				}
			}
		}
		if bytes.Valid {
			v := bytes.Int64
			b.Bytes = &v
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
