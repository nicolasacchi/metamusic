package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// cmdMCPStdio serves MCP over the process's stdin/stdout as the agent that
// owns MM_TOKEN (C30). It needs no MM_AUTH, never binds a port and logs to
// stderr only.
func cmdMCPStdio(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) > 0 {
		return usageError("mcp-stdio takes no arguments (set MM_TOKEN)")
	}
	return mcpStdio(ctx, getenv, s, &mcp.StdioTransport{})
}

// mcpStdio is cmdMCPStdio over an arbitrary transport (in-memory in tests).
func mcpStdio(ctx context.Context, getenv func(string) string, s streams, tr mcp.Transport) error {
	token := getenv("MM_TOKEN")
	if token == "" {
		return usageError("MM_TOKEN is required")
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	cfg.Runner = false // never builds a runner, never takes the lease (C79); Wake stays nil-safe
	sec, err := config.LoadSecrets(cfg.SecretsDir, "local")
	if err != nil {
		return fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fatal(fmt.Errorf("creating data dir: %w", err))
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "metamusic.db"))
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return fatal(fmt.Errorf("opening data dir: %w", err))
	}
	defer func() { _ = root.Close() }()

	svc, err := app.Build(ctx, cfg, sec, st, root, app.Options{Version: version, Commit: commit})
	if err != nil {
		return fatal(err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if err := svc.Close(cctx); err != nil {
			slog.Error("closing services failed", "err", redact.Error(err))
		}
	}()

	// Verify once up front; the message never echoes the value.
	if _, err := svc.Tokens.Verify(ctx, token); err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			return fatal(errors.New("MM_TOKEN rejected"))
		}
		return fatal(fmt.Errorf("verifying MM_TOKEN: %s", redact.Error(err)))
	}
	a, gated := svc.Gated()
	srv, err := mcpgen.NewStdioServer(a, gated, mcpgen.StdioOptions{
		// Re-verified before every tool call: a token revoked mid-session
		// is refused from the next call on.
		Principal: func(ctx context.Context) (auth.Principal, error) { return svc.Tokens.Verify(ctx, token) },
		Version:   version,
	})
	if err != nil {
		return fatal(err)
	}
	if err := srv.Run(ctx, tr); err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		return fatal(fmt.Errorf("mcp-stdio: %s", redact.Error(err)))
	}
	return nil
}
