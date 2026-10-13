package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// tierAAfterIngest runs Tier A over every track that lacks it, as the last step of `import` and
// `seed-synthetic` (C51.7), and returns the one-line summary. It never fails the command: a missing
// capability or a failing track is reported in the line and the exit code stays what the import
// decided.
func tierAAfterIngest(ctx context.Context, cfg config.Config, st *store.Store, data *os.Root, lib *library.Service) string {
	media, err := app.NewMedia(ctx, cfg, st, data, lib, nil, time.Now, nil, nil)
	if err != nil {
		return "Tier A: failed (" + redact.Error(err) + ")"
	}
	rep, err := media.Ears.Backfill(ctx, ears.BackfillOptions{Jobs: 2})
	switch {
	case err != nil:
		slog.Error("tier A after ingest failed", "err", redact.Error(err))
		return "Tier A: failed (" + redact.Error(err) + ")"
	case rep.ToolMissing != "":
		return "Tier A: unavailable (ffmpeg lacks " + capabilityName(rep.ToolMissing) + ")"
	}
	return fmt.Sprintf("Tier A: %d tracks, %d analysed, %d failed", rep.Tracks, rep.Analysed, rep.Failed)
}

// capabilityName turns "filter showspectrumpic" into "showspectrumpic" (encoders keep their kind).
func capabilityName(c string) string { return strings.TrimPrefix(c, "filter ") }

// workDirMaxAge is how old a clip or Tier A work directory must be before serve removes it.
const workDirMaxAge = time.Hour

// sweepWorkDirs removes incoming/clip-* and incoming/ears-* directories older than maxAge (a crashed
// process left them behind; M1's importer only sweeps ingest-*). It returns how many it removed.
func sweepWorkDirs(data *os.Root, now time.Time, maxAge time.Duration) int {
	dir, err := data.Open("incoming")
	if err != nil {
		return 0
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !(strings.HasPrefix(name, "clip-") || strings.HasPrefix(name, "ears-")) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if err := data.RemoveAll("incoming/" + name); err != nil {
			slog.Warn("work dir sweep failed", "dir", name, "err", redact.Error(err))
			continue
		}
		n++
	}
	return n
}

// missingCapability extracts "filter showspectrumpic" from an analysis.ErrToolMissing error.
func missingCapability(err error) string {
	msg := err.Error()
	if _, rest, ok := strings.Cut(msg, analysis.ErrToolMissing.Error()+": "); ok {
		return rest
	}
	return msg
}
