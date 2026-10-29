package curation

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Set changes the opt-ins (C127.1). Only a human principal may: anything else is 403 HUMAN_ONLY before the patch or the
// store is looked at. An empty patch is 422 VALIDATION_FAILED; values that already hold are returned as they are, without a
// write, so updated_at and updated_by keep naming the last real change. A real change writes all four keys in one transaction.
func (s *Service) Set(ctx context.Context, p auth.Principal, patch SettingsPatch) (Settings, error) {
	if p.Kind != auth.KindHuman {
		return Settings{}, problem.E(403, "HUMAN_ONLY", "only the operator can change the curation settings", nil)
	}
	if patch.ExportMuseNames == nil && patch.SubsonicMuseNames == nil {
		return Settings{}, problem.E(422, "VALIDATION_FAILED", "send export_muse_names, subsonic_muse_names or both",
			map[string]any{"field": "body"})
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("curation: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := readSettings(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	next := cur
	if patch.ExportMuseNames != nil {
		next.ExportMuseNames = *patch.ExportMuseNames
	}
	if patch.SubsonicMuseNames != nil {
		next.SubsonicMuseNames = *patch.SubsonicMuseNames
	}
	if next.ExportMuseNames == cur.ExportMuseNames && next.SubsonicMuseNames == cur.SubsonicMuseNames {
		return cur, nil
	}
	next.UpdatedAt, next.UpdatedBy = store.FormatTime(s.now()), p.Key()
	for _, kv := range [][2]string{
		{KeyExport, boolText(next.ExportMuseNames)}, {KeySubsonic, boolText(next.SubsonicMuseNames)},
		{KeyUpdatedAt, next.UpdatedAt}, {KeyUpdatedBy, next.UpdatedBy},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			return Settings{}, fmt.Errorf("curation: write %s: %w", kv[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("curation: commit: %w", err)
	}
	return next, nil
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// queryer is the read half shared by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readSettings reads the four keys; absent flags are false.
func readSettings(ctx context.Context, q queryer) (Settings, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN (?, ?, ?, ?)`, KeyExport, KeySubsonic, KeyUpdatedAt, KeyUpdatedBy)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = rows.Close() }()
	var out Settings
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, err
		}
		switch k {
		case KeyExport:
			out.ExportMuseNames = v == "true"
		case KeySubsonic:
			out.SubsonicMuseNames = v == "true"
		case KeyUpdatedAt:
			out.UpdatedAt = v
		case KeyUpdatedBy:
			out.UpdatedBy = v
		}
	}
	return out, rows.Err()
}
