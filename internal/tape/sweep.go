package tape

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// orphanAge is how old an unreferenced file under derived/tapes/ must be before the sweep removes it: younger files may be
// a render that is being claimed right now.
const orphanAge = 24 * time.Hour

// Sweep expires ready tapes older than olderThan and removes their files and stray files; it returns the number expired
// (D-L2-19). Rows move to expired in one transaction and the files go after the commit, so a crash leaves a file with no
// ready row (an orphan the next sweep removes), never a ready row with no file.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	if s.o.Store == nil {
		return 0, nil
	}
	now := s.now()
	cutoff := now.Add(-olderThan)
	var paths []string
	n := 0
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		paths, n = nil, 0
		rows, err := tx.QueryContext(ctx, `SELECT id, path, finished_at FROM tapes WHERE state = 'ready'`)
		if err != nil {
			return err
		}
		type old struct{ id, path string }
		var due []old
		for rows.Next() {
			var id, p, fin string
			if err := rows.Scan(&id, &p, &fin); err != nil {
				_ = rows.Close()
				return err
			}
			// instants of different widths are compared parsed, never as strings (D-L1-34)
			if ft, err := store.ParseInstant(fin); err == nil && ft.Before(cutoff) {
				due = append(due, old{id, p})
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, d := range due {
			r, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'expired' WHERE id = ? AND state = 'ready'`, d.id)
			if err != nil {
				return err
			}
			if k, _ := r.RowsAffected(); k == 1 {
				n++
				paths = append(paths, d.path)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("tape: sweep: %w", err)
	}
	for _, p := range paths {
		s.removeIfUnreferenced(ctx, p)
	}
	if err := s.sweepOrphans(ctx, now); err != nil {
		return n, err
	}
	return n, nil
}

// sweepOrphans removes files under derived/tapes/ that no ready row names and that are older than a day.
func (s *Service) sweepOrphans(ctx context.Context, now time.Time) error {
	if s.o.Data == nil {
		return nil
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT path FROM tapes WHERE state = 'ready'`)
	if err != nil {
		return fmt.Errorf("tape: sweep: %w", err)
	}
	named := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return err
		}
		named[p] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var stray []string
	werr := fs.WalkDir(s.o.Data.FS(), "derived/tapes", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || named[p] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > orphanAge {
			stray = append(stray, p)
		}
		return nil
	})
	if werr != nil {
		return fmt.Errorf("tape: sweep walk: %w", werr)
	}
	for _, p := range stray {
		if !strings.HasPrefix(p, "derived/tapes/") {
			continue
		}
		// re-check right before removing: a tape may have claimed the name since the snapshot above
		var k int
		if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM tapes WHERE path = ? AND state = 'ready'`, p).Scan(&k); err != nil || k > 0 {
			continue
		}
		if err := s.o.Data.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("tape: sweep %s: %w", p, err)
		}
	}
	return nil
}
