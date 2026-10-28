package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/web"
)

type booted struct {
	cfg    config.Config
	sec    config.Secrets
	st     *store.Store
	tokens *auth.TokenStore
	local  *auth.LocalAuth
	l1     app.L1Options // test seams of the Reconciler (BeforeSettle, FaultAfterPhaseA); zero in production
}

func (b *booted) close() { _ = b.st.Close() }

// boot loads config and secrets and opens the database.
func boot(ctx context.Context, getenv func(string) string, ensureSecrets bool) (*booted, error) {
	cfg, err := config.Load(getenv)
	if err != nil {
		return nil, err
	}
	if ensureSecrets {
		if err := writeDevSecrets(cfg.SecretsDir); err != nil {
			return nil, err
		}
	}
	sec, err := config.LoadSecrets(cfg.SecretsDir, cfg.AuthMode)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "metamusic.db"))
	if err != nil {
		return nil, err
	}
	return &booted{cfg: cfg, sec: sec, st: st, tokens: auth.NewTokenStore(st, sec.Key, time.Now), local: auth.NewLocalAuth(st, sec.Key, cfg.PublicURL)}, nil
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// writeDevSecrets creates missing secret files (dev mode only).
func writeDevSecrets(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating secrets dir: %w", err)
	}
	for name, n := range map[string]int{"secret_key": 32, "edge_secret": 16} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		v, err := randHex(n)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

func cmdServe(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("serve", s)
	runner := fs.Bool("runner", true, "run the generation runner and reconciler in this process (MM_RUNNER); --runner=false serves HTTP only")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("serve takes no arguments")
	}
	var off bool
	fs.Visit(func(f *flag.Flag) { off = off || (f.Name == "runner" && !*runner) })
	b, err := boot(ctx, getenv, false)
	if err != nil {
		return err
	}
	defer b.close()
	if off {
		b.cfg.Runner = false
	}
	return serve(ctx, b, s)
}

// pruneIdempotency removes expired idempotency rows now and then every interval until ctx ends.
func pruneIdempotency(ctx context.Context, s *idem.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.Prune(ctx); err != nil && ctx.Err() == nil {
			slog.Error("idempotency prune failed", "err", redact.Error(err))
		} else if n > 0 {
			slog.Info("idempotency prune", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepExports removes export files older than 7 days now and then every interval until ctx ends.
// It has its own ticker, independent of the backup schedule.
func sweepExports(ctx context.Context, e *exports.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := e.Sweep(ctx, 7*24*time.Hour); err != nil && ctx.Err() == nil {
			slog.Error("export sweep failed", "err", redact.Error(err))
		} else if n > 0 {
			slog.Info("export sweep", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweeper is the cache-sweep surface of the cover and download services.
type sweeper interface {
	Sweep(ctx context.Context, olderThan time.Duration) (int, error)
}

// sweepM4aCaches removes cover files older than 30 days and download copies older than 7 days now and then every
// interval until ctx ends (C110.3); a nil service is skipped. Both caches are input-addressed, so a swept file is
// simply rendered again on its next use.
func sweepM4aCaches(ctx context.Context, covers, downloads sweeper, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for _, c := range []struct {
			name string
			s    sweeper
			age  time.Duration
		}{{"cover", covers, 30 * 24 * time.Hour}, {"download", downloads, 7 * 24 * time.Hour}} {
			if c.s == nil { // callers pass a nil interface, never a typed nil
				continue
			}
			if n, err := c.s.Sweep(ctx, c.age); err != nil && ctx.Err() == nil {
				slog.Error(c.name+" sweep failed", "err", redact.Error(err))
			} else if n > 0 {
				slog.Info(c.name+" sweep", "deleted", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pruneSubsonicSalts removes salt sightings past the media window now and then every interval until ctx ends.
func pruneSubsonicSalts(ctx context.Context, s *auth.SubsonicStore, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.PruneSalts(ctx); err != nil && ctx.Err() == nil {
			slog.Error("subsonic salt prune failed", "err", redact.Error(err))
		} else if n > 0 {
			slog.Info("subsonic salt prune", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func serve(ctx context.Context, b *booted, s streams) error {
	root, err := os.OpenRoot(b.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("opening data dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	// Every service, the gate and api.Deps come from one constructor (C39).
	svc, err := app.Build(ctx, b.cfg, b.sec, b.st, root, app.Options{
		Probe: ensureProbe(ctx, b.cfg, root, s), Version: version, Commit: commit,
		// ctx ends when shutdown starts: it is the server's base context, so in-flight long-polls return.
		M3: app.M3Options{Shutdown: ctx, Live: true},
		L1: b.l1,
	})
	if err != nil {
		return err
	}
	if n, err := svc.Lineage.BackfillRecipeKeys(ctx); err != nil {
		slog.Warn("recipe key backfill failed", "err", redact.Error(err))
	} else if n > 0 {
		slog.Info("recipe keys backfilled", "rows", n)
	}
	if n := sweepWorkDirs(root, time.Now(), workDirMaxAge); n > 0 {
		slog.Info("swept stale work directories", "removed", n)
	}
	// Both capability gaps are also logged by app.NewMedia through slog; the operator running the
	// binary in a terminal sees one plain line per gap here.
	if svc.TierAMissing != nil {
		fmt.Fprintf(s.err, "metamusic: warning: Tier A unavailable (ffmpeg lacks %s); analysis answers TOOL_MISSING\n", capabilityName(missingCapability(svc.TierAMissing)))
	}
	if svc.ClipsMissing != nil {
		fmt.Fprintf(s.err, "metamusic: warning: clips unavailable (ffmpeg lacks %s); clip operations answer 503\n", capabilityName(missingCapability(svc.ClipsMissing)))
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if err := svc.Close(cctx); err != nil {
			slog.Error("closing services failed", "err", redact.Error(err))
		}
	}()
	opts := svc.Server
	opts.SPA = web.Dist()
	opts.MCPFactory = func(a huma.API, inner http.Handler) (http.Handler, error) {
		srv, err := mcpgen.NewServer(a, inner, version)
		if err != nil {
			return nil, err
		}
		return mcpgen.Handler(srv), nil
	}
	h, err := httpserver.New(opts)
	if err != nil {
		return err
	}
	pruneCtx, stopPrune := context.WithCancel(ctx)
	var pruned sync.WaitGroup
	pruned.Add(4)
	go func() { defer pruned.Done(); pruneIdempotency(pruneCtx, svc.Idem, time.Hour) }()
	go func() { defer pruned.Done(); sweepExports(pruneCtx, svc.Exports, 24*time.Hour) }()
	go func() { defer pruned.Done(); pruneSubsonicSalts(pruneCtx, svc.Subsonic, time.Hour) }()
	var coverSweep, dlSweep sweeper
	if svc.Covers != nil {
		coverSweep = svc.Covers
	}
	if svc.Downloads != nil {
		dlSweep = svc.Downloads
	}
	go func() { defer pruned.Done(); sweepM4aCaches(pruneCtx, coverSweep, dlSweep, 24*time.Hour) }()
	if svc.OIDC != nil { // C105 eager fetch: never blocks startup, logs error codes only
		go svc.OIDC.Prefetch(ctx)
	}
	if b.cfg.BackupAt != "off" {
		sch := &backup.Scheduler{At: b.cfg.BackupAt, Loc: time.Local, Run: func(c context.Context) {
			if _, err := svc.Backups.Start(c, "scheduled", auth.CLI); err != nil {
				slog.Error("scheduled backup not started", "err", redact.Error(err))
			}
		}}
		pruned.Add(1)
		go func() { defer pruned.Done(); sch.Loop(pruneCtx) }()
	}
	defer func() { stopPrune(); pruned.Wait() }()
	runCtx, stopRun := context.WithCancel(ctx)
	var running sync.WaitGroup
	// stopRunner stops claiming and waits for the ingest pool and the lease release (Runner.Run's own drain).
	stopRunner := func() { stopRun(); running.Wait() }
	defer stopRunner()
	if svc.Runner != nil && svc.Reconciler != nil {
		running.Add(2)
		go func() { defer running.Done(); _ = svc.Runner.Run(runCtx) }()
		go func() { defer running.Done(); _ = svc.Reconciler.Run(runCtx) }()
	} else {
		slog.Info("runner not started", "reason", runnerOffReason(b.cfg, svc))
	}
	// Startup order (C95.3): store opened and migrated (boot) -> app.Build -> taxonomy seed -> the first Settle (waiting while
	// another process holds the lease) -> net.Listen -> Run. There is no readiness flag: the listener simply is not open
	// until the derived model is consistent, inside the image HEALTHCHECK window.
	if err := startupSettle(ctx, svc); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", b.cfg.Addr)
	if err != nil {
		return err
	}
	running.Add(1)
	go func() { defer running.Done(); svc.Provenance.Run(runCtx) }()
	startL2(runCtx, pruneCtx, svc, &running, &pruned) // the Syncer, the tape runner and the tape sweep (C118)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Fprintf(s.out, "metamusic %s listening on http://%s (auth=%s)\n", version, ln.Addr(), b.cfg.AuthMode)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// Shutdown (C79): stop claiming, the base context is already cancelled (long-polls return), drain HTTP for
	// 30 s while the runner drains its ingest pool, then release the lease.
	stopRun()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownDrain)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// shutdownDrain is how long in-flight HTTP requests get at shutdown (compose stop_grace_period is 90 s).
const shutdownDrain = 30 * time.Second

// runnerOffReason names why serve did not start the runner, for the log line.
func runnerOffReason(cfg config.Config, svc *app.Services) string {
	switch {
	case !cfg.Runner:
		return "runner_disabled"
	case cfg.Backend == config.BackendNone:
		return "backend_none"
	case svc.Backend == nil:
		return "keys_absent"
	}
	return "no_runner"
}

// ensureProbe makes sure the device-probe FLAC exists under derived/probe and
// returns the object the media handler serves for it. Without ffmpeg (or when
// generating fails) it returns nil and get_device_probe answers 503.
func ensureProbe(ctx context.Context, cfg config.Config, root *os.Root, s streams) *mediaserve.Object {
	const rel = "derived/probe/probe-v1.flac"
	tools := toolsOf(cfg)
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		fmt.Fprintf(s.err, "metamusic: warning: ffmpeg not found; the device probe is unavailable\n")
		return nil
	}
	if _, err := root.Stat(rel); err != nil {
		if err := root.MkdirAll("derived/probe", 0o750); err != nil {
			slog.Error("probe: creating directory failed", "err", err)
			return nil
		}
		abs, err := filepath.Abs(cfg.DataDir)
		if err == nil {
			err = synth.ProbeFLAC(ctx, tools, filepath.Join(abs, filepath.FromSlash(rel)))
		}
		if err != nil {
			slog.Error("probe: generating failed", "err", err)
			return nil
		}
	}
	f, err := root.Open(rel)
	if err != nil {
		slog.Error("probe: opening failed", "err", err)
		return nil
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		slog.Error("probe: hashing failed", "err", err)
		return nil
	}
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	return &mediaserve.Object{RelPath: rel, ContentType: "audio/flac", ETag: hex.EncodeToString(h.Sum(nil)), ModTime: st.ModTime()}
}

func cmdDev(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("dev", s)
	dir := fs.String("data-dir", "./data/dev", "dev data directory (MM_DATA_DIR and MM_SECRETS_DIR are ignored)")
	fakeProv := fs.Bool("fake-provider", true, "use the in-process fake backend (MM_BACKEND=fake) unless MM_BACKEND is set")
	seed := fs.Bool("seed-synthetic", false, "generate and import a synthetic library when the library has no tracks")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	over := map[string]string{
		"MM_AUTH":        "local",
		"MM_DATA_DIR":    *dir,
		"MM_SECRETS_DIR": filepath.Join(*dir, "secrets"),
	}
	if *fakeProv && getenv("MM_BACKEND") == "" {
		over["MM_BACKEND"] = "fake"
	}
	env := func(k string) string {
		if v, ok := over[k]; ok {
			return v
		}
		return getenv(k)
	}
	b, err := boot(ctx, env, true)
	if err != nil {
		return err
	}
	defer b.close()

	if *seed {
		var tracks int
		if err := b.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks`).Scan(&tracks); err != nil {
			return err
		}
		switch {
		case tracks > 0:
		case missingTools(b.cfg) != nil:
			fmt.Fprintf(s.err, "metamusic: warning: %v; continuing without a library\n", missingTools(b.cfg))
		default:
			fmt.Fprintln(s.out, "seeding a synthetic library (this renders audio and takes a few seconds) ...")
			if err := seedSynthetic(ctx, b.cfg, b.st, seedOpts{profile: synth.ProfileDev, seed: 1, viaDev: true}, s); err != nil {
				return err
			}
		}
	}

	var n int
	if err := b.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_account`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		pw, err := randHex(12)
		if err != nil {
			return err
		}
		if err := b.local.SetPassword(ctx, "dev", pw); err != nil {
			return err
		}
		fmt.Fprintf(s.out, "dev login: username=dev password=%s (generated once; reset with `metamusic user set-password`)\n", pw)
	} else {
		fmt.Fprintln(s.out, "dev login: username/password already set; reset with `MM_AUTH=local metamusic user set-password --username dev`")
	}
	fmt.Fprintf(s.out, "open %s/auth/login\n", b.cfg.PublicURL)
	return serve(ctx, b, s)
}

func cmdHealthcheck(ctx context.Context, getenv func(string) string, s streams) error {
	addr := getenv("MM_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad MM_ADDR: %w", err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/healthz"}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("unhealthy: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	fmt.Fprintln(s.out, "ok")
	return nil
}

func parseScopes(csv string) ([]auth.Scope, error) {
	var out []auth.Scope
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, auth.Scope(p))
		}
	}
	return out, nil
}

// startupSettle seeds the Sound taxonomy (the taxonomy exists before any Sound is computed) and runs the first Settle,
// waiting while another process holds the lease (a restarting server whose predecessor is still draining).
func startupSettle(ctx context.Context, svc *app.Services) error {
	if err := svc.Sounds.EnsureSeed(ctx); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("sound taxonomy seed: %w", err)
	}
	for {
		rep, err := svc.Provenance.Settle(ctx)
		switch {
		case err == nil:
			if rep.KeysProcessed > 0 {
				slog.Info("library model settled", "keys", rep.KeysProcessed, "songs_created", rep.SongsCreated, "muses_created", rep.MusesCreated, "sessions_created", rep.SessionsCreated)
			}
			return nil
		case errors.Is(err, provenance.ErrSettleBusy):
			slog.Info("waiting for the settle lease")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		default:
			return fmt.Errorf("startup settle: %w", err)
		}
	}
}
