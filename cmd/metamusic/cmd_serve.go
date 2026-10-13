package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	return &booted{cfg, sec, st, auth.NewTokenStore(st, sec.Key, time.Now), auth.NewLocalAuth(st, sec.Key, cfg.PublicURL)}, nil
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

func cmdServe(ctx context.Context, getenv func(string) string, s streams) error {
	b, err := boot(ctx, getenv, false)
	if err != nil {
		return err
	}
	defer b.close()
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

func serve(ctx context.Context, b *booted, s streams) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(s.err, nil)))
	root, err := os.OpenRoot(b.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("opening data dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	// Every service, the gate and api.Deps come from one constructor (C39).
	svc, err := app.Build(ctx, b.cfg, b.sec, b.st, root, app.Options{
		Probe: ensureProbe(ctx, b.cfg, root, s), Version: version, Commit: commit,
	})
	if err != nil {
		return err
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
	pruned.Add(2)
	go func() { defer pruned.Done(); pruneIdempotency(pruneCtx, svc.Idem, time.Hour) }()
	go func() { defer pruned.Done(); sweepExports(pruneCtx, svc.Exports, 24*time.Hour) }()
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
	ln, err := net.Listen("tcp", b.cfg.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Fprintf(s.out, "metamusic %s listening on http://%s (auth=%s)\n", version, ln.Addr(), b.cfg.AuthMode)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
	seed := fs.Bool("seed-synthetic", false, "generate and import a synthetic library when the library has no tracks")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	over := map[string]string{
		"MM_AUTH":        "local",
		"MM_DATA_DIR":    *dir,
		"MM_SECRETS_DIR": filepath.Join(*dir, "secrets"),
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
