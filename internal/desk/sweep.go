package desk

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

const auditionsRoot = "derived/auditions"

// sweepGrace is how old an orphan must be before the sweep removes it: a CLI command that builds the services while the server
// runs must not remove a directory whose row is about to be inserted by the server.
const sweepGrace = 10 * time.Minute

// Sweep removes what a crash left behind (C357.2): a plans/ entry whose id has no desk_plans row (a partial directory or a
// directory written before a failed insert), an audition work directory, and an audition MP3 without a row, each at least
// ten minutes old. It is run once at start. It returns the number of paths removed.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	if err := s.wired(); err != nil {
		return 0, err
	}
	removed := 0
	ents, err := fs.ReadDir(s.o.Data.FS(), plansRoot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return 0, err
	default:
		known, err := s.idSet(ctx, `SELECT id FROM desk_plans`)
		if err != nil {
			return 0, err
		}
		for _, e := range ents {
			if known[e.Name()] && e.IsDir() {
				continue
			}
			if s.removeTree(plansRoot+"/"+e.Name(), e) {
				removed++
			}
		}
	}
	top, err := fs.ReadDir(s.o.Data.FS(), auditionsRoot)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return removed, nil
	case err != nil:
		return removed, err
	}
	known, err := s.idSet(ctx, `SELECT mp3_path FROM desk_auditions`)
	if err != nil {
		return removed, err
	}
	for _, e := range top {
		rel := auditionsRoot + "/" + e.Name()
		if strings.HasPrefix(e.Name(), "work-") {
			if s.removeTree(rel, e) {
				removed++
			}
			continue
		}
		if !e.IsDir() {
			continue
		}
		files, err := fs.ReadDir(s.o.Data.FS(), rel)
		if err != nil {
			continue
		}
		for _, f := range files {
			if p := rel + "/" + f.Name(); !known[p] && s.removeTree(p, f) {
				removed++
			}
		}
	}
	return removed, ctx.Err()
}

func (s *Service) idSet(ctx context.Context, q string) (map[string]bool, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// removeTree removes a file or a (read-only) directory tree under the data root and reports whether it did.
func (s *Service) removeTree(rel string, e fs.DirEntry) bool {
	if fi, err := e.Info(); err != nil || s.o.Now().Sub(fi.ModTime()) < sweepGrace {
		return false
	}
	_ = s.o.Data.Chmod(rel, 0o700) // a partial directory is 0700
	if err := s.o.Data.RemoveAll(rel); err != nil {
		s.o.Log.Warn("desk sweep: could not remove an orphan", "path", rel, "err", redact.Error(err))
		return false
	}
	return true
}
