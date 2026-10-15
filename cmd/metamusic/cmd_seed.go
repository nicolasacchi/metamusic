package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/importer"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

const seedPrincipal = "seed-synthetic"

type seedOpts struct {
	profile synth.Profile
	seed    int64
	force   bool // --force
	viaDev  bool // invoked by `dev --seed-synthetic`
	refs    bool // --references: the reference profile, imported with both reference flags (C66)
}

func cmdSeedSynthetic(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("seed-synthetic", s)
	profile := fs.String("profile", "dev", "dev or e2e")
	seed := fs.Int64("seed", 1, "audio variant seed")
	force := fs.Bool("force", false, "seed a data directory that is not marked MM_E2E=1")
	references := fs.Bool("references", false, "also seed recipe-less reference tracks (unmatched FLACs and lossy MP3s)")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("seed-synthetic: unexpected argument " + importer.Escape(fs.Arg(0)))
	}
	if *profile != "dev" && *profile != "e2e" {
		return usageError("seed-synthetic: --profile must be dev or e2e")
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return fatal(err)
	}
	o := seedOpts{profile: synth.Profile(*profile), seed: *seed, force: *force, refs: *references}
	if o.refs {
		o.profile = synth.ProfileReference
	}
	if !cfg.E2E && !o.force {
		return fatal(errors.New("seed-synthetic: refusing to seed a data directory that is not marked as an e2e/dev one: set MM_E2E=1, use `metamusic dev --seed-synthetic`, or pass --force"))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "metamusic.db"))
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := missingTools(cfg); err != nil {
		return fatal(err)
	}
	if err := seedSynthetic(ctx, cfg, st, o, s); err != nil {
		return fatal(err)
	}
	return nil
}

// missingTools reports the first media tool that cannot be found.
func missingTools(cfg config.Config) error {
	for _, t := range []string{cfg.FFmpeg, cfg.FLAC, cfg.Metaflac} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("media tool %q not found: ffmpeg, flac and metaflac are required to generate and import audio", filepath.Base(t))
		}
	}
	return nil
}

// seedSynthetic generates <DataDir>/inbox-synthetic (only when absent) and
// imports it as principal seed-synthetic. Synthetic rows are permanent, so it
// refuses a library that has any other import run (always, even with --force).
func seedSynthetic(ctx context.Context, cfg config.Config, st *store.Store, o seedOpts, s streams) error {
	if err := seedLibrary(ctx, cfg, st, o, s); err != nil {
		return err
	}
	if o.profile == synth.ProfileE2E {
		return seedE2EM4a(ctx, cfg, st)
	}
	return nil
}

// seedE2EM4a adds the M4a rows the e2e profile needs (C114), idempotently and after both the fresh import and the
// "already seeded" early return: the 12 s timed track (syn-song-03, seed 1003) first and the 6 s take
// (syn-song-01, seed 1001) second in the offline set, and one connector grant through connectors.Store.Ensure so
// the /human/connectors page has a row without an OAuth flow. An offline set that already holds tracks is left alone.
func seedE2EM4a(ctx context.Context, cfg config.Config, st *store.Store) error {
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	data, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	lib := library.New(st, data, time.Now)
	pls := playlists.New(st, lib, time.Now, nil)
	set, err := pls.Get(ctx, offlineSetID)
	if err != nil {
		return fmt.Errorf("seed-synthetic: offline set: %w", err)
	}
	if len(set.Items) == 0 {
		var ids []string
		for _, r := range []struct {
			request string
			seed    int64
		}{{"syn-song-03", 1003}, {"syn-song-01", 1001}} {
			var id string
			err := st.R.QueryRowContext(ctx, `SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id
				WHERE r.request_id = ? AND r.seed = ? AND t.status <> 'discarded' ORDER BY t.created_at, t.id LIMIT 1`, r.request, r.seed).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("seed-synthetic: no track for request %s seed %d", r.request, r.seed)
			}
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if _, err := pls.Update(ctx, auth.CLI, offlineSetID, playlists.PlaylistPatch{Revision: set.Revision, TrackIDs: ids}); err != nil {
			return fmt.Errorf("seed-synthetic: offline set: %w", err)
		}
	}
	if _, _, err := connectors.NewStore(st, time.Now, nil).Ensure(ctx, "e2e-connector", "e2e-subject", "e2e", "E2E Connector"); err != nil {
		return fmt.Errorf("seed-synthetic: connector grant: %w", err)
	}
	return nil
}

// offlineSetID is the fixed id of the offline-set playlist created by the migration (C103).
const offlineSetID = "00000000-0000-7000-8000-0000000000f1"

func seedLibrary(ctx context.Context, cfg config.Config, st *store.Store, o seedOpts, s streams) error {
	var other, tracks int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_runs WHERE principal <> ?`, seedPrincipal).Scan(&other); err != nil {
		return err
	}
	if other > 0 {
		return errors.New("seed-synthetic: refusing: this library has real import runs, and synthetic rows are permanent")
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks`).Scan(&tracks); err != nil {
		return err
	}
	if tracks > 0 {
		fmt.Fprintf(s.out, "seed-synthetic: already seeded (%d tracks)\n", tracks)
		return nil
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	archive := filepath.Join(abs, "inbox-synthetic")
	if _, err := os.Lstat(archive); errors.Is(err, os.ErrNotExist) {
		tmp := archive + ".partial"
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
		if err := os.MkdirAll(tmp, 0o750); err != nil {
			return err
		}
		if _, err := synth.Archive(ctx, toolsOf(cfg), tmp, o.profile, o.seed); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, archive); err != nil {
			return err
		}
	}
	data, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	rt, err := newRuntime(ctx, cfg, st, data)
	if err != nil {
		return err
	}
	ro, err := inbox.Open(archive)
	if err != nil {
		return err
	}
	im, err := importer.New(st, rt.lib, data, rt.pipe, importer.Options{
		Inbox: ro, InboxLabel: "synthetic", MinAge: 0, Jobs: 2, Principal: seedPrincipal, Now: time.Now,
		ReferenceUnmatched: o.refs, ReferenceLossy: o.refs,
	})
	if err != nil {
		return err
	}
	inv, err := im.Inventory(ctx)
	if err != nil {
		return err
	}
	res, err := im.Apply(ctx, inv, importer.ApplyOptions{Force: true})
	if err != nil {
		return err
	}
	if res.State != "completed" || res.Errors > 0 {
		return fmt.Errorf("seed-synthetic: import run %s ended %s with %d errors", res.RunID, res.State, res.Errors)
	}
	fmt.Fprintf(s.out, "seed-synthetic: imported %d tracks, %d recipes (%d drafts), profile %s\n",
		res.TracksImported, inv.Report.Summary.RecipesUnique, inv.Report.Summary.DraftsNew, o.profile)
	fmt.Fprintln(s.out, tierAAfterIngest(ctx, cfg, st, data, rt.lib))
	return nil
}
