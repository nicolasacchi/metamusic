package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l2VerifyTapeFiles is the `library verify --files` check of R8 (C122.5): every ready tape's file exists with the recorded
// size (the data directory comes from config.LoadOffline(getenv)). It returns the number of errors it printed; each line has
// the L1 form "<code> <subject> <detail>" and names ids and relative paths only.
func l2VerifyTapeFiles(ctx context.Context, st *store.Store, getenv func(string) string, out io.Writer) (errors int, err error) {
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return 0, err
	}
	rows, err := st.R.QueryContext(ctx, `SELECT id, path, bytes FROM tapes WHERE state = 'ready' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, rel string
		var want int64
		if err := rows.Scan(&id, &rel, &want); err != nil {
			return errors, err
		}
		if !strings.HasPrefix(rel, "derived/tapes/") || strings.Contains(rel, "..") {
			fmt.Fprintf(out, "tape_file_path %s %s\n", id, rel)
			errors++
			continue
		}
		fi, serr := os.Stat(filepath.Join(cfg.DataDir, filepath.FromSlash(rel)))
		switch {
		case serr != nil:
			fmt.Fprintf(out, "tape_file_missing %s %s\n", id, rel)
			errors++
		case fi.Size() != want:
			fmt.Fprintf(out, "tape_file_size %s %s has %d bytes, recorded %d\n", id, rel, fi.Size(), want)
			errors++
		}
	}
	return errors, rows.Err()
}
