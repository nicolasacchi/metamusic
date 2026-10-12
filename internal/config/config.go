// Package config loads process configuration and secret files.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Config is the process configuration, derived from MM_* environment variables.
type Config struct {
	Addr           string
	PublicURL      string
	AuthMode       string // "proxy" | "local"
	DataDir        string
	SecretsDir     string
	TrustedProxies []netip.Prefix
	TrustedUsers   []string
	E2E            bool
	FFmpeg         string // MM_FFMPEG, default "ffmpeg"
	FLAC           string // MM_FLAC, default "flac"
	Metaflac       string // MM_METAFLAC, default "metaflac"
	Inbox          string // MM_INBOX, default DataDir/inbox
}

// Load builds a Config from getenv. MM_AUTH must be "proxy" or "local".
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:     orDefault(getenv("MM_ADDR"), "127.0.0.1:8080"),
		AuthMode: getenv("MM_AUTH"),
		DataDir:  orDefault(getenv("MM_DATA_DIR"), "./data"),
		E2E:      getenv("MM_E2E") == "1",
		FFmpeg:   orDefault(getenv("MM_FFMPEG"), "ffmpeg"),
		FLAC:     orDefault(getenv("MM_FLAC"), "flac"),
		Metaflac: orDefault(getenv("MM_METAFLAC"), "metaflac"),
	}
	if c.AuthMode != "proxy" && c.AuthMode != "local" {
		return Config{}, fmt.Errorf("config: MM_AUTH must be %q or %q (got %q)", "proxy", "local", c.AuthMode)
	}
	c.Inbox = orDefault(getenv("MM_INBOX"), filepath.Join(c.DataDir, "inbox"))
	c.PublicURL = orDefault(getenv("MM_PUBLIC_URL"), "http://"+c.Addr)
	c.SecretsDir = orDefault(getenv("MM_SECRETS_DIR"), filepath.Join(c.DataDir, "secrets"))
	for _, p := range splitList(getenv("MM_TRUSTED_PROXY")) {
		pfx, err := parsePrefix(p)
		if err != nil {
			return Config{}, fmt.Errorf("config: MM_TRUSTED_PROXY entry %q: %w", p, err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx)
	}
	c.TrustedUsers = splitList(getenv("MM_TRUSTED_USERS"))
	return c, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Secrets holds key material loaded from files in the secrets directory.
type Secrets struct {
	Key        []byte // HMAC pepper for tokens/URLs
	EdgeSecret string // shared secret with the reverse proxy (proxy mode)
}

// LoadSecrets reads secret_key (always) and edge_secret (proxy mode). Errors
// name the file, never its contents.
func LoadSecrets(dir string, mode string) (Secrets, error) {
	var s Secrets
	raw, err := readSecretFile(dir, "secret_key")
	if err != nil {
		return s, err
	}
	switch {
	case len(raw) == 32:
		s.Key = raw
	case len(raw) == 64:
		k, herr := hex.DecodeString(string(raw))
		if herr != nil || len(k) != 32 {
			return s, errors.New("config: secret_key: invalid hex encoding")
		}
		s.Key = k
	default:
		trimmed := strings.TrimSpace(string(raw))
		k, herr := hex.DecodeString(trimmed)
		if len(trimmed) != 64 || herr != nil {
			return s, fmt.Errorf("config: secret_key: must be 32 raw bytes or 64 hex chars (got %d bytes)", len(raw))
		}
		s.Key = k
	}
	if mode == "proxy" {
		e, err := readSecretFile(dir, "edge_secret")
		if err != nil {
			return s, err
		}
		s.EdgeSecret = strings.TrimSpace(string(e))
		if s.EdgeSecret == "" {
			return s, errors.New("config: edge_secret: file is empty")
		}
	}
	return s, nil
}

func readSecretFile(dir, name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", name, errors.Unwrap(err))
	}
	return b, nil
}
