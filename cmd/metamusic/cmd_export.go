package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// cmdValidateBatch checks a songs.jsonl with the mirror of `yue2 batch` (C32):
// exit 0 when it is accepted, 1 when it has issues, 2 on usage errors.
func cmdValidateBatch(_ context.Context, args []string, s streams) error {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		return usageError("validate-batch: expected FILE")
	}
	raw, err := os.ReadFile(args[0]) //nolint:gosec // the operator names the file on the command line
	if err != nil {
		return usageError(fmt.Sprintf("validate-batch: %v", err))
	}
	rep := yue2.ValidateBatch(raw, os.DirFS(filepath.Dir(args[0])))
	for _, is := range rep.Issues {
		fmt.Fprintf(s.out, "line %d: %s %s", is.Line, is.Severity, is.Code)
		if is.Field != "" {
			fmt.Fprintf(s.out, " (%s)", is.Field)
		}
		if is.Fatal {
			fmt.Fprint(s.out, " [fatal]")
		}
		fmt.Fprintln(s.out)
	}
	fmt.Fprintf(s.out, "%d lines, %d requests, %d issues\n", rep.Lines, rep.Requests, len(rep.Issues))
	if !rep.OK {
		return exitError{code: 1}
	}
	return nil
}

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// cmdExport writes songs.jsonl for a selection (C39): without --out the JSONL goes to stdout (refused
// with exit 2 when ABC files are needed); with --out it and the ABC files go into an empty or new
// directory. The validator result goes to stderr; exit 0 OK, 1 issues (the bytes are still written), 2 usage.
func cmdExport(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("export", s)
	var (
		source    = fs.String("source", "", "export an imported JSONL source `PATH` byte for byte")
		tracks    = fs.Bool("tracks", false, "export the recipes of the selected tracks")
		drafts    = fs.Bool("drafts", false, "export drafts")
		forBatch  = fs.Bool("for-batch", false, "--source only: make ids unique so yue2 batch accepts the file")
		out       = fs.String("out", "", "write songs.jsonl and ABC files into this empty or new `DIR`")
		minStars  = fs.Int("min-stars", 0, "--tracks: at least this many human stars")
		playlist  = fs.String("playlist", "", "--tracks: only tracks of this playlist `ID`")
		collect   = fs.String("collection", "", "--drafts: only drafts in this collection")
		origin    = fs.String("origin", "", "--drafts: only drafts of this origin")
		statuses  listFlag
		tags      listFlag
		dStatuses listFlag
	)
	fs.Var(&statuses, "status", "--tracks: track status (repeatable)")
	fs.Var(&tags, "tag", "--tracks: track tag (repeatable)")
	fs.Var(&dStatuses, "draft-status", "--drafts: draft status (repeatable; default backlog)")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("export: unexpected argument " + fs.Arg(0))
	}
	kinds := 0
	for _, b := range []bool{*source != "", *tracks, *drafts} {
		if b {
			kinds++
		}
	}
	if kinds != 1 {
		return usageError("export: give exactly one of --source PATH, --tracks, --drafts")
	}
	var sel export.Selection
	switch {
	case *source != "":
		sel = export.Selection{Kind: "source", SourcePath: *source, ForBatch: *forBatch}
	case *tracks:
		sel = export.Selection{Kind: "tracks", Tracks: export.TrackFilter{Status: statuses, Tags: tags, PlaylistID: *playlist}}
		if *minStars != 0 {
			sel.Tracks.MinStars = minStars
		}
	default:
		sel = export.Selection{Kind: "drafts", Drafts: export.DraftFilter{Status: dStatuses, Collection: *collect, Origin: *origin}}
	}

	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fatal(errors.New("export: no database in the data directory"))
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	lib := library.New(st, nil, time.Now)
	res, err := export.Build(ctx, exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil)), sel)
	switch {
	case errors.Is(err, export.ErrUnknownSource):
		return usageError("export: no imported JSONL file has the path " + *source)
	case errors.Is(err, export.ErrEmpty):
		return exitError{code: 1, err: errors.New("export: nothing to export")}
	case errors.Is(err, library.ErrNotFound):
		return usageError("export: no such playlist")
	case err != nil:
		var ve *library.ValidationError
		if errors.As(err, &ve) {
			return usageError("export: " + ve.Error())
		}
		return fatal(err)
	}

	if *out == "" {
		if len(res.ABCFiles) > 0 {
			return usageError("export: the selection needs ABC files; give --out DIR")
		}
		if _, err := s.out.Write(res.JSONL); err != nil {
			return fatal(err)
		}
	} else if err := writeExportDir(*out, res); err != nil {
		return err
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(s.err, "warning: %s: %s\n", w.Code, w.Message)
	}
	for _, is := range res.Validation.Issues {
		fmt.Fprintf(s.err, "line %d: %s %s", is.Line, is.Severity, is.Code)
		if is.Field != "" {
			fmt.Fprintf(s.err, " (%s)", is.Field)
		}
		if is.Fatal {
			fmt.Fprint(s.err, " [fatal]")
		}
		fmt.Fprintln(s.err)
	}
	fmt.Fprintf(s.err, "%d lines, %d requests, %d issues\n", res.Validation.Lines, res.Validation.Requests, len(res.Validation.Issues))
	if !res.Validation.OK {
		return exitError{code: 1}
	}
	return nil
}

// writeExportDir writes songs.jsonl and the ABC files into an empty or new directory.
func writeExportDir(dir string, res export.Result) error {
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return usageError("export: --out directory is not empty")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return usageError(fmt.Sprintf("export: %v", err))
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = root.Close() }()
	write := func(rel string, b []byte) error {
		if !fs.ValidPath(rel) {
			return fmt.Errorf("export: refusing path %q", rel)
		}
		if d := path.Dir(rel); d != "." {
			if err := root.MkdirAll(d, 0o750); err != nil {
				return err
			}
		}
		return root.WriteFile(rel, b, 0o640)
	}
	if err := write("songs.jsonl", res.JSONL); err != nil {
		return fatal(err)
	}
	for p, b := range res.ABCFiles {
		if err := write(p, b); err != nil {
			return fatal(err)
		}
	}
	return nil
}
