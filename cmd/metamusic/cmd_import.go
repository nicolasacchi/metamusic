package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/importer"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const maxPlanReport = 64 << 20

// importJSON is the --json summary (no recipe or reference values, only counts and codes).
type importJSON struct {
	ReportID       string           `json:"report_id"`
	DryRun         bool             `json:"dry_run"`
	State          string           `json:"state,omitempty"`
	PlanDigest     string           `json:"plan_digest"`
	Report         string           `json:"report,omitempty"`
	MappingTodo    string           `json:"mapping_todo,omitempty"`
	Summary        importer.Summary `json:"summary"`
	TracksImported int              `json:"tracks_imported,omitempty"`
	TracksFailed   int              `json:"tracks_failed,omitempty"`
	TierA          string           `json:"tier_a,omitempty"`
	SpreadLU       *float64         `json:"loudness_spread_lu,omitempty"`
}

func toolsOf(cfg config.Config) mediatool.Tools {
	return mediatool.Tools{FFmpeg: cfg.FFmpeg, FLAC: cfg.FLAC, Metaflac: cfg.Metaflac}
}

func cmdImport(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("import", s)
	dry := fs.Bool("dry-run", false, "inventory only: print and save the plan")
	plan := fs.String("plan", "", "apply the plan of this dry-run report")
	yes := fs.Bool("yes", false, "apply the current plan without a reviewed report")
	inboxFlag := fs.String("inbox", "", "inbox directory (default MM_INBOX)")
	mapping := fs.String("mapping", "", "mapping CSV (default <data dir>/import/mapping.csv)")
	report := fs.String("report", "", "also write the report to this new file")
	template := fs.String("mapping-template", "", "write the mapping template to this file instead of import/mapping.todo.csv")
	minAge := fs.Duration("min-age", 2*time.Minute, "files younger than this are skipped as unsettled")
	jobs := fs.Int("jobs", 2, "concurrent ingests")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	refUnmatched := fs.Bool("reference-unmatched", false, "import unmatched FLACs as recipe-less reference tracks")
	refLossy := fs.Bool("reference-lossy", false, "import standalone MP3s as lossy reference tracks")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	switch {
	case fs.NArg() > 0:
		return usageError("import: unexpected argument " + importer.Escape(fs.Arg(0)))
	case *jobs < 1:
		return usageError("import: --jobs must be at least 1")
	case *minAge < 0:
		return usageError("import: --min-age must not be negative")
	case *dry && (*plan != "" || *yes):
		return usageError("import: --dry-run cannot be combined with --plan or --yes")
	case !*dry && *plan == "" && !*yes:
		return usageError("import: applying needs a reviewed plan: run `metamusic import --dry-run`, then `metamusic import --plan <report>` (or pass --yes to apply the current plan unreviewed)")
	}
	var reviewed *importer.Report
	if *plan != "" {
		r, err := readPlan(*plan)
		if err != nil {
			return usageError(err.Error())
		}
		reviewed = r
		if r.Flags.ReferenceUnmatched != *refUnmatched || r.Flags.ReferenceLossy != *refLossy {
			return usageError("import: --plan was reviewed with different reference flags")
		}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return fatal(err)
	}
	dir := cfg.Inbox
	if *inboxFlag != "" {
		dir = *inboxFlag
	}
	ro, err := inbox.Open(dir)
	if err != nil {
		return fatal(fmt.Errorf("opening inbox: %w", err))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fatal(fmt.Errorf("creating data dir: %w", err))
	}
	data, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return fatal(fmt.Errorf("opening data dir: %w", err))
	}
	defer func() { _ = data.Close() }()
	opts := importer.Options{Inbox: ro, InboxLabel: "inbox", MappingPath: *mapping, MinAge: *minAge, Jobs: *jobs, Principal: "cli",
		ReferenceUnmatched: *refUnmatched, ReferenceLossy: *refLossy}
	out := cliOut{s: s, asJSON: *asJSON, dataDir: cfg.DataDir}
	dbPath := filepath.Join(cfg.DataDir, "metamusic.db")

	if *dry {
		st, err := store.OpenReadOnly(ctx, dbPath)
		var lib *library.Service
		switch {
		case errors.Is(err, store.ErrNoDatabase):
			st = nil
		case err != nil:
			return fatal(err)
		default:
			defer func() { _ = st.Close() }()
			lib = library.New(st, data, time.Now)
		}
		im, err := importer.New(st, lib, data, nil, opts)
		if err != nil {
			return fatal(err)
		}
		inv, err := im.Inventory(ctx)
		if err != nil {
			return fatal(err)
		}
		files, err := im.WriteDryRun(inv)
		if err != nil {
			return fatal(err)
		}
		if *template != "" {
			if err := moveTemplate(cfg.DataDir, files.MappingTodo, *template); err != nil {
				return fatal(err)
			}
			files.MappingTodo = ""
		}
		if *report != "" {
			if err := copyNew(filepath.Join(cfg.DataDir, files.Report), *report); err != nil {
				return fatal(err)
			}
		}
		out.dryRun(inv, files)
		return exitCode(inv.Report.Summary.Errors)
	}

	// apply
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fatal(err)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	rt, err := newRuntime(ctx, cfg, st, data)
	if err != nil {
		return fatal(err)
	}
	im, err := importer.New(st, rt.lib, data, rt.pipe, opts)
	if err != nil {
		return fatal(err)
	}
	inv, err := im.Inventory(ctx)
	if err != nil {
		return fatal(err)
	}
	ao := importer.ApplyOptions{Force: *yes}
	if reviewed != nil {
		ao.ExpectDigest = reviewed.PlanDigest
		if *yes && reviewed.PlanDigest != inv.PlanDigest() {
			out.planDiff(reviewed.PlanEntries, inv.PlanEntries())
		}
	}
	res, err := im.Apply(ctx, inv, ao)
	switch {
	case errors.Is(err, importer.ErrPlanChanged):
		var reviewedEntries []string
		if reviewed != nil {
			reviewedEntries = reviewed.PlanEntries
		}
		out.planDiff(reviewedEntries, inv.PlanEntries())
		return exitError{3, errors.New("PLAN_CHANGED: the inbox plan differs from the reviewed dry-run; run `metamusic import --dry-run` again, or pass --yes to apply the current plan")}
	case errors.Is(err, importer.ErrImportInProgress):
		return exitError{4, errors.New("IMPORT_IN_PROGRESS: another import is running (or ran within the last 2 minutes)")}
	case err != nil:
		return fatal(err)
	}
	var spread *float64
	if stats, err := rt.lib.Stats(ctx); err == nil {
		spread = stats.Loudness.SpreadLU
	}
	tierA := tierAAfterIngest(ctx, cfg, st, data, rt.lib)
	out.applied(res, spread, tierA)
	slog.Info("import finished", "run", res.RunID, "state", res.State, "tracks_imported", res.TracksImported,
		"tracks_failed", res.TracksFailed, "errors", res.Errors)
	if res.State != "completed" {
		return exitError{2, fmt.Errorf("import run %s ended %s", res.RunID, res.State)}
	}
	return exitCode(res.Errors + res.TracksFailed)
}

func exitCode(errs int) error {
	if errs > 0 {
		return exitError{1, nil}
	}
	return nil
}

// runtime holds what apply and seeding need around a store.
type runtime struct {
	lib  *library.Service
	pipe *ingest.Pipeline
}

func newRuntime(ctx context.Context, cfg config.Config, st *store.Store, data *os.Root) (*runtime, error) {
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	tools := toolsOf(cfg)
	an, err := ffmpeg.New(ctx, tools)
	if err != nil {
		return nil, fmt.Errorf("media tools: %w", err)
	}
	return &runtime{lib: library.New(st, data, time.Now), pipe: ingest.New(data, abs, tools, an, time.Now)}, nil
}

func readPlan(path string) (*importer.Report, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied report path
	if err != nil {
		return nil, fmt.Errorf("import: reading --plan: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxPlanReport+1))
	if err != nil || len(b) > maxPlanReport {
		return nil, errors.New("import: --plan report unreadable or too large")
	}
	var r importer.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("import: --plan is not a dry-run report")
	}
	if r.Format != importer.ReportFormat || !r.DryRun || r.PlanDigest == "" {
		return nil, errors.New("import: --plan must be a dry-run report (mm.inventory/1) written by `metamusic import --dry-run`")
	}
	return &r, nil
}

func copyNew(src, dst string) error {
	b, err := os.ReadFile(src) // #nosec G304 -- a file this process just wrote
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-supplied path
	if err != nil {
		return fmt.Errorf("import: creating %s: %w", importer.Escape(dst), err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// moveTemplate writes the mapping template to dst (new file) and removes the default copy.
func moveTemplate(dataDir, rel, dst string) error {
	if err := copyNew(filepath.Join(dataDir, rel), dst); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dataDir, rel))
}

// cliOut renders results; every inbox path is escaped by the importer's formatters.
type cliOut struct {
	s       streams
	asJSON  bool
	dataDir string
}

func (o cliOut) json(v any) {
	enc := json.NewEncoder(o.s.out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (o cliOut) dryRun(inv *importer.Inventory, files importer.DryRunFiles) {
	if o.asJSON {
		o.json(importJSON{ReportID: inv.Report.ReportID, DryRun: true, PlanDigest: inv.PlanDigest(), Report: files.Report,
			MappingTodo: files.MappingTodo, Summary: inv.Report.Summary})
		return
	}
	inv.Report.FormatText(o.s.out)
	abs := filepath.Join(o.dataDir, files.Report)
	fmt.Fprintf(o.s.out, "\nreport:  %s\n", importer.Escape(abs))
	if files.MappingTodo != "" && inv.NeedsMapping() {
		fmt.Fprintf(o.s.out, "mapping: %s (fill it in, save it as import/mapping.csv, and dry-run again)\n", importer.Escape(filepath.Join(o.dataDir, files.MappingTodo)))
	}
	fmt.Fprintf(o.s.out, "\nnext:    metamusic import --plan %s\n", shellQuote(abs))
}

func (o cliOut) applied(res *importer.RunResult, spread *float64, tierA string) {
	if o.asJSON {
		o.json(importJSON{ReportID: res.RunID, State: res.State, PlanDigest: res.Report.PlanDigest, Report: res.ReportPath,
			Summary: res.Report.Summary, TracksImported: res.TracksImported, TracksFailed: res.TracksFailed, SpreadLU: spread, TierA: tierA})
		return
	}
	res.Report.FormatText(o.s.out)
	fmt.Fprintf(o.s.out, "\nimport run %s: %s, %d tracks imported, %d failed\n", res.RunID, res.State, res.TracksImported, res.TracksFailed)
	if spread != nil {
		note := ""
		if *spread > 2 {
			note = " (above 2 LU: a loudness-matched listen.flac is recommended)"
		}
		fmt.Fprintf(o.s.out, "loudness spread: %.1f LU%s\n", *spread, note)
	}
	fmt.Fprintln(o.s.out, tierA)
}

func (o cliOut) planDiff(reviewed, current []string) {
	added, removed := importer.DiffPlans(reviewed, current)
	w := o.s.err
	fmt.Fprintf(w, "plan differs from the reviewed dry-run: %d entries added, %d removed\n", len(added), len(removed))
	for _, a := range added {
		fmt.Fprintf(w, "  + %s\n", importer.Escape(a))
	}
	for _, r := range removed {
		fmt.Fprintf(w, "  - %s\n", importer.Escape(r))
	}
}

func shellQuote(p string) string {
	if !strings.ContainsAny(p, " '\"\\$`!*?()<>|&;#~\n\t") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
