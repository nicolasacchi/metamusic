package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/store"
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
	return &booted{cfg, sec, st, auth.NewTokenStore(st, sec.Key), auth.NewLocalAuth(st, sec.Key, cfg.PublicURL)}, nil
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

func serve(ctx context.Context, b *booted, s streams) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(s.err, nil)))
	h, err := httpserver.New(httpserver.Options{
		Cfg: b.cfg, Sec: b.sec, Store: b.st, Tokens: b.tokens, Sessions: b.local,
		SPA: web.Dist(), Version: version, Commit: commit,
		MCPFactory: func(a huma.API, inner http.Handler) (http.Handler, error) {
			srv, err := mcpgen.NewServer(a, inner, version)
			if err != nil {
				return nil, err
			}
			return mcpgen.Handler(srv), nil
		},
	})
	if err != nil {
		return err
	}
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

func cmdDev(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("dev", s)
	dir := fs.String("data-dir", "./data/dev", "dev data directory (MM_DATA_DIR and MM_SECRETS_DIR are ignored)")
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
