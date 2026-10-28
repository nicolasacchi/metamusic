package main

import (
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// cmdConfig dispatches `metamusic config <sub>`.
func cmdConfig(args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 || args[0] != "check" {
		return usageError("config: expected check")
	}
	fs := newFlags("config check", s)
	if err := fs.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("config check takes no arguments")
	}
	return configCheck(getenv, s)
}

// configCheck validates the environment through config.LoadAll and config.LoadSecrets, the
// exact calls boot makes for serve, and nothing else: it never opens the data directory or
// database, binds, starts the runner or touches the network. Output carries no secret value;
// the errors come from config, which never echoes secrets.
func configCheck(getenv func(string) string, s streams) error {
	cfg, errs := config.LoadAll(getenv)
	var sec config.Secrets
	if cfg.AuthMode == "proxy" || cfg.AuthMode == "local" {
		var err error
		if sec, err = config.LoadSecrets(cfg.SecretsDir, cfg.AuthMode); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(s.err, e)
		}
		return exitError{code: 1}
	}
	fmt.Fprintln(s.out, "config ok")
	fmt.Fprintf(s.out, "auth: %s\n", cfg.AuthMode)
	fmt.Fprintf(s.out, "backend: %s\n", cfg.Backend)
	fmt.Fprintf(s.out, "runner: %s\n", enabled(cfg.Runner))
	if cfg.OIDCIssuer != "" {
		n := len(cfg.OIDCClients)
		fmt.Fprintf(s.out, "oidc: enabled (%d client%s)\n", n, plural(n))
	} else {
		fmt.Fprintln(s.out, "oidc: disabled")
	}
	if cfg.NtfyURL != "" {
		fmt.Fprintf(s.out, "ntfy: enabled (token %s)\n", map[bool]string{true: "set", false: "not set"}[sec.NtfyToken != ""])
	} else {
		fmt.Fprintln(s.out, "ntfy: disabled")
	}
	fmt.Fprintf(s.out, "backup: %s\n", map[bool]string{true: "disabled", false: "daily at " + cfg.BackupAt}[cfg.BackupAt == "off"])
	fmt.Fprintf(s.out, "trusted proxies: %d\n", len(cfg.TrustedProxies))
	return nil
}

func enabled(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
