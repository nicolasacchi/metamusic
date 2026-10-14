// Package config loads process configuration and secret files.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

	// M2a (C25).
	BackupDir string // MM_BACKUP_DIR, default <parent of DataDir>/backup
	BackupAt  string // MM_BACKUP_AT "HH:MM" or "off", default "03:30"
	NtfyURL   string // MM_NTFY_URL, default "" (notifications off)
	RateScale int    // MM_RATE_SCALE 1..1000, default 1

	// M2b (C43).
	ClipCacheMB int64 // MM_CLIP_CACHE_MB, default 4096, minimum 256

	// M3 (C68).
	Backend         string        // MM_BACKEND: "none" (default) | "fake" | "runpod"
	Runner          bool          // MM_RUNNER, default true; `serve --runner=false` overrides; mcp-stdio forces false
	FakeBackend     string        // MM_FAKE_BACKEND: "" | "fast" | "slow:<duration>"; only with Backend=fake
	JobMaxRuntime   time.Duration // MM_JOB_MAX_RUNTIME, default 15m, 30s..45m (D-M3-16)
	KillSlackS      int           // MM_KILL_SLACK_S, default 15, 0..120
	JobTTL          time.Duration // MM_JOB_TTL, default 2h, 10m..24h
	ColdStartS      int           // MM_COLD_START_S, default 180, 30..900
	PriceMarginPPM  int64         // MM_PRICE_MARGIN_PPM, default 100000, 0..1000000
	BillingLag      time.Duration // MM_BILLING_LAG, default 6h, 1h..72h
	ResidualHold    time.Duration // MM_RESIDUAL_HOLD, default 48h, 1h..720h
	RunPodRecordDir string        // MM_RUNPOD_RECORD_DIR, default "" (fixture recording); existing dir outside DataDir
}

var backupAtRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Load builds a Config from getenv. MM_AUTH must be "proxy" or "local".
func Load(getenv func(string) string) (Config, error) {
	c, err := load(getenv)
	if err != nil {
		return Config{}, err
	}
	if c.AuthMode != "proxy" && c.AuthMode != "local" {
		return Config{}, fmt.Errorf("config: MM_AUTH must be %q or %q (got %q)", "proxy", "local", c.AuthMode)
	}
	return c, nil
}

// LoadOffline is Load without the MM_AUTH requirement, for commands that never
// serve HTTP (mcp-stdio, export, backup, restore, validate-batch, token, user).
// AuthMode stays as given, possibly empty.
func LoadOffline(getenv func(string) string) (Config, error) {
	return load(getenv)
}

func load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:     orDefault(getenv("MM_ADDR"), "127.0.0.1:8080"),
		AuthMode: getenv("MM_AUTH"),
		DataDir:  orDefault(getenv("MM_DATA_DIR"), "./data"),
		E2E:      getenv("MM_E2E") == "1",
		FFmpeg:   orDefault(getenv("MM_FFMPEG"), "ffmpeg"),
		FLAC:     orDefault(getenv("MM_FLAC"), "flac"),
		Metaflac: orDefault(getenv("MM_METAFLAC"), "metaflac"),
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
	if err := loadM2a(&c, getenv); err != nil {
		return Config{}, err
	}
	if err := loadM2b(&c, getenv); err != nil {
		return Config{}, err
	}
	if err := loadM3(&c, getenv); err != nil {
		return Config{}, err
	}
	return c, nil
}

// loadM2b reads the M2b settings (C43).
func loadM2b(c *Config, getenv func(string) string) error {
	c.ClipCacheMB = 4096
	if v := getenv("MM_CLIP_CACHE_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 256 {
			return errors.New("config: MM_CLIP_CACHE_MB must be an integer >= 256")
		}
		c.ClipCacheMB = n
	}
	return nil
}

func loadM2a(c *Config, getenv func(string) string) error {
	c.BackupAt = orDefault(getenv("MM_BACKUP_AT"), "03:30")
	if c.BackupAt != "off" && !backupAtRE.MatchString(c.BackupAt) {
		return errors.New(`config: MM_BACKUP_AT must be "off" or HH:MM (00:00-23:59)`)
	}
	c.BackupDir = orDefault(getenv("MM_BACKUP_DIR"), filepath.Join(filepath.Dir(c.DataDir), "backup"))
	if err := checkDisjoint(c.DataDir, c.BackupDir); err != nil {
		return err
	}
	if u := getenv("MM_NTFY_URL"); u != "" {
		if err := checkNtfyURL(u); err != nil {
			return err
		}
		c.NtfyURL = u
	}
	c.RateScale = 1
	if v := getenv("MM_RATE_SCALE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return errors.New("config: MM_RATE_SCALE must be an integer in 1..1000")
		}
		c.RateScale = n
	}
	return nil
}

// checkDisjoint refuses a backup dir that equals, contains or lies inside the data dir.
func checkDisjoint(dataDir, backupDir string) error {
	d, err1 := filepath.Abs(dataDir)
	b, err2 := filepath.Abs(backupDir)
	if err1 != nil || err2 != nil {
		return errors.New("config: MM_BACKUP_DIR: cannot resolve path")
	}
	inside := func(base, p string) bool {
		rel, err := filepath.Rel(base, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if inside(d, b) || inside(b, d) {
		return errors.New("config: MM_BACKUP_DIR must not equal, contain or lie inside MM_DATA_DIR")
	}
	return nil
}

// checkNtfyURL validates MM_NTFY_URL. Errors never echo the value (it may carry credentials).
func checkNtfyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return errors.New("config: MM_NTFY_URL must be an absolute URL")
	}
	if u.User != nil {
		return errors.New("config: MM_NTFY_URL must not carry credentials (use the ntfy_token secret)")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("config: MM_NTFY_URL must not carry a query string or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip, perr := netip.ParseAddr(u.Hostname()); !(u.Hostname() == "localhost" || (perr == nil && ip.IsLoopback())) {
			return errors.New("config: MM_NTFY_URL must use https unless the host is loopback")
		}
	default:
		return errors.New("config: MM_NTFY_URL must use https unless the host is loopback")
	}
	return nil
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
	NtfyToken  string // optional ntfy publisher token (file ntfy_token)
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
	if b, err := os.ReadFile(filepath.Join(dir, "ntfy_token")); err == nil {
		s.NtfyToken = strings.TrimSpace(string(b))
		if s.NtfyToken == "" {
			return s, errors.New("config: ntfy_token: file is empty")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return s, fmt.Errorf("config: reading ntfy_token: %w", errors.Unwrap(err))
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
