package curation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service reads and writes the curation settings. The reads are final (F2) so the catalog, tape and recordview never wait for
// lane A; Set and Stats are lane A's (stub_a.go).
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the service. A nil now means time.Now.
func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

// Get returns the effective settings: absent flags are false.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN (?, ?, ?, ?)`, KeyExport, KeySubsonic, KeyUpdatedAt, KeyUpdatedBy)
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

// flag reads one boolean setting; absent is false.
func (s *Service) flag(ctx context.Context, key string) (bool, error) {
	var v string
	err := s.st.R.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "true", err
}

// SubsonicMuseNames reports whether the automatic Subsonic groupings carry the Muse's name (albums.Settings).
func (s *Service) SubsonicMuseNames(ctx context.Context) (bool, error) {
	return s.flag(ctx, KeySubsonic)
}

// ExportMuseNames reports whether the tape carries "inspired by" lines.
func (s *Service) ExportMuseNames(ctx context.Context) (bool, error) { return s.flag(ctx, KeyExport) }
