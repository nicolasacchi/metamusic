package main

import (
	"context"
	"errors"
)

// cmdOIDC dispatches the operator's OIDC commands (C114, D-M4a-21).
func cmdOIDC(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 || args[0] != "probe" {
		return usageError("oidc: expected probe")
	}
	return cmdOIDCProbe(ctx, args[1:], getenv, s)
}

// cmdOIDCProbe parses the final flags of `oidc probe` and, until M4a-C3, answers "not implemented". The flags are
// frozen here so the usage text and the tests do not move when the lane lands. STUB(M4a-C3)
func cmdOIDCProbe(_ context.Context, args []string, _ func(string) string, s streams) error {
	fs := newFlags("oidc probe", s)
	server := fs.String("server", "", "the metamusic base URL (the public URL)")
	clientID := fs.String("client-id", "", "the probe's OAuth client id")
	_ = fs.String("client-secret-file", "", "file holding the client secret (read by the binary; confidential clients only)")
	_ = fs.String("redirect", "http://127.0.0.1:8765/callback", "the probe client's registered redirect URL")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("oidc probe: unexpected argument")
	}
	if *server == "" {
		return usageError("oidc probe: --server is required")
	}
	if *clientID == "" {
		return usageError("oidc probe: --client-id is required")
	}
	return errors.New("oidc probe: not implemented yet (STUB(M4a-C3))")
}
