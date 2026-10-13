package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// trackList is a repeatable --track flag.
type trackList []string

func (l *trackList) String() string     { return strings.Join(*l, ",") }
func (l *trackList) Set(v string) error { *l = append(*l, v); return nil }

// analyzeJSON is the --json report.
type analyzeJSON struct {
	DryRun   bool             `json:"dry_run"`
	Tracks   int              `json:"tracks"`
	Analysed int              `json:"analysed"`
	Failed   int              `json:"failed"`
	Ran      map[string]int   `json:"ran"`
	Failures []analyzeFailure `json:"failures"`
}

type analyzeFailure struct {
	TrackID string            `json:"track_id"`
	Failed  map[string]string `json:"failed"`
}

// cmdAnalyze runs Tier A analysis (C51.7): `metamusic analyze [--track ID]... [--force] [--dry-run] [--jobs N] [--json]`.
// Exit 0 ok; 1 any track failed; 2 usage or fatal, including a missing Tier A capability (named).
func cmdAnalyze(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("analyze", s)
	var tracks trackList
	fs.Var(&tracks, "track", "analyse only this track `ID` (repeatable)")
	force := fs.Bool("force", false, "re-run analyzers that already have a result")
	dry := fs.Bool("dry-run", false, "print what would run; write nothing")
	jobs := fs.Int("jobs", 1, "tracks analysed in parallel")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("analyze: unexpected argument " + fs.Arg(0))
	}
	if *jobs < 1 {
		return usageError("analyze: --jobs must be at least 1")
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fatal(errors.New("analyze: no database in the data directory"))
	}
	open := store.Open
	if *dry {
		open = store.OpenReadOnly
	}
	st, err := open(ctx, dbPath)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return fatal(err)
	}
	data, err := os.OpenRoot(abs)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = data.Close() }()
	lib := library.New(st, data, time.Now)
	media, err := app.NewMedia(ctx, cfg, st, data, lib, nil, time.Now, nil, nil)
	if err != nil {
		return fatal(err)
	}
	rep, err := media.Ears.Backfill(ctx, ears.BackfillOptions{TrackIDs: slices.Clone(tracks), Force: *force, DryRun: *dry, Jobs: *jobs})
	if err != nil {
		return fatal(fmt.Errorf("analyze: %w", err))
	}
	if rep.ToolMissing != "" {
		return fatal(fmt.Errorf("analyze: Tier A is unavailable: ffmpeg lacks %s", rep.ToolMissing))
	}
	wrote := 0
	for _, n := range rep.Ran {
		wrote += n
	}
	if !*dry && wrote > 0 {
		if err := auditAnalyze(ctx, st, fs, rep.Failed == 0); err != nil {
			return fatal(fmt.Errorf("analyze: audit: %w", err))
		}
	}
	printAnalyze(s, rep, *dry, *asJSON)
	if rep.Failed > 0 {
		return exitError{code: 1}
	}
	return nil
}

// auditAnalyze appends the one cli:analyze audit row of a run that wrote something, the way
// cli:restore does; the args digest covers the flags that were set.
func auditAnalyze(ctx context.Context, st *store.Store, fs *flag.FlagSet, ok bool) error {
	var set []string
	fs.Visit(func(f *flag.Flag) { set = append(set, f.Name+"="+f.Value.String()) })
	sort.Strings(set)
	sum := sha256.Sum256([]byte(strings.Join(set, "\n")))
	e := audit.Entry{At: time.Now(), Principal: auth.CLI, Transport: reqinfo.Transport("cli"), Client: "metamusic",
		Op: "cli:analyze", Method: "CLI", ArgsSHA256: hex.EncodeToString(sum[:]), Status: 200}
	if !ok {
		e.Status, e.ErrorCode = 500, "ANALYZE_FAILED"
	}
	return audit.NewStore(st, func() string { return uuid.NewV7().String() }).Insert(ctx, e)
}

func printAnalyze(s streams, rep ears.BackfillReport, dry, asJSON bool) {
	if asJSON {
		out := analyzeJSON{DryRun: dry, Tracks: rep.Tracks, Analysed: rep.Analysed, Failed: rep.Failed, Ran: rep.Ran, Failures: []analyzeFailure{}}
		if out.Ran == nil {
			out.Ran = map[string]int{}
		}
		for _, f := range rep.Failures {
			out.Failures = append(out.Failures, analyzeFailure{TrackID: f.TrackID, Failed: f.Failed})
		}
		enc := json.NewEncoder(s.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	names := make([]string, 0, len(rep.Ran))
	for n := range rep.Ran {
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s %d", n, rep.Ran[n]))
	}
	ran := "nothing to do"
	if len(parts) > 0 {
		ran = strings.Join(parts, ", ")
	}
	if dry {
		fmt.Fprintf(s.out, "analyze (dry run): %d tracks; would run: %s\n", rep.Tracks, ran)
		return
	}
	fmt.Fprintf(s.out, "analyze: %d tracks, %d analysed, %d failed; ran: %s\n", rep.Tracks, rep.Analysed, rep.Failed, ran)
	for _, f := range rep.Failures {
		keys := make([]string, 0, len(f.Failed))
		for k, v := range f.Failed {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		fmt.Fprintf(s.out, "  failed %s: %s\n", f.TrackID, strings.Join(keys, " "))
	}
}
