package morning

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

// orphanAge is how old an unreferenced file under derived/morning/ must be before the sweep removes it: a younger one may be a side
// that a build has claimed and not yet recorded.
const orphanAge = 24 * time.Hour

// Sweep removes the side files of crates older than keep_days (C380.4) and returns how many files it removed. A file is shared by
// every crate with its sha, so it goes only when every crate that references it is old enough. Rows are never deleted: they are
// marked removed in one transaction and the files go after the commit, so a crash leaves a file with no live row (an orphan the
// next sweep removes), never a live row with no file.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	if s.o.Store == nil {
		return 0, nil
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	now := s.now()
	cutoff := now.Add(-time.Duration(set.KeepDays) * 24 * time.Hour)
	var due []string // the shas whose every referencing crate is old
	paths := map[string]string{}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		due, paths = nil, map[string]string{}
		rows, err := tx.QueryContext(ctx, `SELECT s.sha256, s.path, c.created_at, s.removed_at IS NULL FROM morning_crate_sides s JOIN morning_crates c ON c.id = s.crate_id`)
		if err != nil {
			return err
		}
		young := map[string]bool{}
		live := map[string]bool{}
		for rows.Next() {
			var (
				sha, path, at string
				isLive        bool
			)
			if err := rows.Scan(&sha, &path, &at, &isLive); err != nil {
				_ = rows.Close()
				return err
			}
			paths[sha] = path
			t, perr := store.ParseInstant(at) // instants of different widths are compared parsed (D-L1-34)
			if perr != nil || !t.Before(cutoff) {
				young[sha] = true
			}
			if isLive {
				live[sha] = true
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for sha := range live {
			if !young[sha] {
				due = append(due, sha)
			}
		}
		for _, sha := range due {
			if _, err := tx.ExecContext(ctx, `UPDATE morning_crate_sides SET removed_at = ? WHERE sha256 = ? AND removed_at IS NULL`, store.FormatTime(now), sha); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("morning: sweep: %w", err)
	}
	removed := 0
	for _, sha := range due {
		p := paths[sha]
		if !strings.HasPrefix(p, morningDir+"/") || s.o.Data == nil {
			continue
		}
		if err := s.o.Data.Remove(p); err == nil {
			removed++
		} else if !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("morning: sweep %s: %w", p, err)
		}
	}
	n, err := s.sweepOrphans(ctx, now)
	return removed + n, err
}

// sweepOrphans removes files under derived/morning/ that no live side row names and that are older than orphanAge.
func (s *Service) sweepOrphans(ctx context.Context, now time.Time) (int, error) {
	if s.o.Data == nil {
		return 0, nil
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT path FROM morning_crate_sides WHERE removed_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("morning: sweep: %w", err)
	}
	named := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		named[p] = true
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var stray []string
	werr := fs.WalkDir(s.o.Data.FS(), morningDir, func(p string, d fs.DirEntry, err error) error {
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
			return nil //nolint:nilerr // a file that vanished mid-walk is not an error
		}
		if now.Sub(info.ModTime()) > orphanAge {
			stray = append(stray, p)
		}
		return nil
	})
	if werr != nil {
		return 0, fmt.Errorf("morning: sweep walk: %w", werr)
	}
	n := 0
	for _, p := range stray {
		// re-check right before removing: a build may have recorded the file since the snapshot above
		var k int
		if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM morning_crate_sides WHERE path = ? AND removed_at IS NULL`, p).Scan(&k); err != nil || k > 0 {
			continue
		}
		if err := s.o.Data.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return n, fmt.Errorf("morning: sweep %s: %w", p, err)
		}
		n++
	}
	return n, nil
}
