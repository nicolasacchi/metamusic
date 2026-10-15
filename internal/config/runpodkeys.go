package config

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultConsoleURL is where the "set max workers to 0 now" push (C82) points.
const DefaultConsoleURL = "https://console.runpod.io/serverless"

// RunPodKeys are the values of <SecretsDir>/runpod.env (C68). It never prints
// a value: String and GoString report only presence.
type RunPodKeys struct {
	Jobs, Read, Write string // RUNPOD_JOBS_KEY (required), RUNPOD_READ_KEY (required), RUNPOD_WRITE_KEY (optional)
	EndpointID        string // RUNPOD_ENDPOINT_ID (required; not secret, but private)
	ConsoleURL        string // RUNPOD_CONSOLE_URL (optional; https only)
}

func set(s string) string {
	if s == "" {
		return "unset"
	}
	return "set"
}

// String lists which fields are present, never their values.
func (k RunPodKeys) String() string {
	console := "default"
	if k.ConsoleURL != "" && k.ConsoleURL != DefaultConsoleURL {
		console = "custom"
	}
	return fmt.Sprintf("RunPodKeys{jobs:%s read:%s write:%s endpoint:%s console:%s}",
		set(k.Jobs), set(k.Read), set(k.Write), set(k.EndpointID), console)
}

// GoString is String, so %#v cannot leak either.
func (k RunPodKeys) GoString() string { return k.String() }

// LoadRunPodKeys reads <dir>/runpod.env. It returns (zero, false, nil) when the
// file is absent. Errors name the file and the variable, never a value.
func LoadRunPodKeys(dir string) (RunPodKeys, bool, error) {
	return loadRunPodKeys(dir, true)
}

// LoadRunPodKeysForProvision is LoadRunPodKeys for `runpod provision`, which finds the endpoint by name:
// a runpod.env without RUNPOD_ENDPOINT_ID is accepted (first-time provisioning, before the id exists). An
// all-access key in the file is still refused.
func LoadRunPodKeysForProvision(dir string) (RunPodKeys, bool, error) {
	return loadRunPodKeys(dir, false)
}

func loadRunPodKeys(dir string, needEndpoint bool) (RunPodKeys, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "runpod.env"))
	if errors.Is(err, os.ErrNotExist) {
		return RunPodKeys{}, false, nil
	}
	if err != nil {
		return RunPodKeys{}, false, fmt.Errorf("config: reading runpod.env: %w", errors.Unwrap(err))
	}
	vars, err := parseDotenv("runpod.env", raw)
	if err != nil {
		return RunPodKeys{}, false, err
	}
	for _, name := range []string{"RUNPOD_API_KEY", "RUNPOD_ADMIN_KEY"} {
		if _, ok := vars[name]; ok {
			return RunPodKeys{}, false, errors.New("config: runpod.env must not contain an all-access key; use runpod-admin.env with `metamusic runpod provision` only")
		}
	}
	k := RunPodKeys{
		Jobs: vars["RUNPOD_JOBS_KEY"], Read: vars["RUNPOD_READ_KEY"], Write: vars["RUNPOD_WRITE_KEY"],
		EndpointID: vars["RUNPOD_ENDPOINT_ID"], ConsoleURL: vars["RUNPOD_CONSOLE_URL"],
	}
	for _, req := range []struct {
		name, val string
		optional  bool
	}{
		{"RUNPOD_JOBS_KEY", k.Jobs, false}, {"RUNPOD_READ_KEY", k.Read, false}, {"RUNPOD_ENDPOINT_ID", k.EndpointID, !needEndpoint},
	} {
		if req.val == "" && !req.optional {
			return RunPodKeys{}, false, fmt.Errorf("config: runpod.env: %s missing", req.name)
		}
	}
	if k.ConsoleURL == "" {
		k.ConsoleURL = DefaultConsoleURL
	} else if u, err := url.Parse(k.ConsoleURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return RunPodKeys{}, false, errors.New("config: runpod.env: RUNPOD_CONSOLE_URL must be an https URL")
	}
	return k, true, nil
}

// AdminKey is the all-access key read only by `runpod provision`.
type AdminKey struct{ key string }

// String and GoString never reveal the key.
func (a AdminKey) String() string   { return "AdminKey{" + set(a.key) + "}" }
func (a AdminKey) GoString() string { return a.String() }

// Value returns the key for the one caller that needs it (the admin client).
func (a AdminKey) Value() string { return a.key }

// LoadAdminKey reads RUNPOD_ADMIN_KEY or RUNPOD_API_KEY (either name) from a
// dotenv file at path. Both present and different is an error.
func LoadAdminKey(path string) (AdminKey, error) {
	base := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return AdminKey{}, fmt.Errorf("config: reading %s: %w", base, errors.Unwrap(err))
	}
	vars, err := parseDotenv(base, raw)
	if err != nil {
		return AdminKey{}, err
	}
	a, b := vars["RUNPOD_ADMIN_KEY"], vars["RUNPOD_API_KEY"]
	switch {
	case a != "" && b != "" && a != b:
		return AdminKey{}, fmt.Errorf("config: %s: RUNPOD_ADMIN_KEY and RUNPOD_API_KEY differ; keep one", base)
	case a != "":
		return AdminKey{key: a}, nil
	case b != "":
		return AdminKey{key: b}, nil
	}
	return AdminKey{}, fmt.Errorf("config: %s: RUNPOD_ADMIN_KEY or RUNPOD_API_KEY missing", base)
}

// parseDotenv implements the C68 dotenv rules: KEY=VALUE lines, # comments,
// blank lines, optional `export `, optional quotes around the whole value, no
// interpolation, CRLF tolerated, BOM stripped. A duplicated variable or a
// malformed line is an error naming the file and variable or line, never a value.
func parseDotenv(file string, raw []byte) (map[string]string, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	vars := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, val, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("config: %s: line %d is not KEY=VALUE", file, i+1)
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("config: %s: %s defined more than once", file, name)
		}
		val = strings.TrimSpace(val)
		if n := len(val); n >= 2 && (val[0] == '"' || val[0] == '\'') && val[n-1] == val[0] {
			val = val[1 : n-1]
		}
		vars[name] = val
	}
	return vars, nil
}

// derive is HMAC-SHA256(Secrets.Key, label) (C68 "Derived keys"; no new secret file).
func (s Secrets) derive(label string) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// UploadKey signs worker upload and start-handshake URLs.
func (s Secrets) UploadKey() []byte { return s.derive("metamusic/upload-url/v1") }

// ConfirmKey keys the hash of spend-confirmation tokens.
func (s Secrets) ConfirmKey() []byte { return s.derive("metamusic/confirmation/v1") }

// MeterKey keys the stored worker_key (raw worker ids are never stored).
func (s Secrets) MeterKey() []byte { return s.derive("metamusic/worker-id/v1") }
