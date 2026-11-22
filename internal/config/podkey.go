package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PodKeyFile is the name of the pod key file inside the secrets directory.
const PodKeyFile = "runpod-pod.env"

// PodKey is the RunPod key of the pod backend (C390.3), scoped to pods, catalog and billing. It never prints a value: String
// and GoString report presence only, and Value is for the pods client alone.
type PodKey struct{ key string }

// String never reveals the key.
func (k PodKey) String() string { return "PodKey{" + set(k.key) + "}" }

// GoString is String, so %#v cannot leak either.
func (k PodKey) GoString() string { return k.String() }

// Value returns the key for the one caller that needs it (the pods client).
func (k PodKey) Value() string { return k.key }

// LoadPodKey reads <dir>/runpod-pod.env (C390.3). The caller reads it at every call that needs the key and never caches it,
// so deleting the file closes the backend. It returns (zero, false, nil) when the file is absent. It refuses any variable
// other than RUNPOD_POD_KEY (an all-access RUNPOD_API_KEY or RUNPOD_ADMIN_KEY gets its own message), a file with any group or
// other permission bit, an empty key and a malformed file. Errors name the file and the variable, never a value.
func LoadPodKey(dir string) (PodKey, bool, error) {
	path := filepath.Join(dir, PodKeyFile)
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PodKey{}, false, nil
	}
	if err != nil {
		return PodKey{}, false, fmt.Errorf("config: reading %s: %w", PodKeyFile, errors.Unwrap(err))
	}
	if !fi.Mode().IsRegular() {
		return PodKey{}, false, fmt.Errorf("config: %s must be a regular file", PodKeyFile)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return PodKey{}, false, fmt.Errorf("config: %s must not be readable by group or others (chmod 600)", PodKeyFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PodKey{}, false, fmt.Errorf("config: reading %s: %w", PodKeyFile, errors.Unwrap(err))
	}
	vars, err := parseDotenv(PodKeyFile, raw)
	if err != nil {
		return PodKey{}, false, err
	}
	for _, name := range []string{"RUNPOD_API_KEY", "RUNPOD_ADMIN_KEY"} {
		if _, ok := vars[name]; ok {
			return PodKey{}, false, fmt.Errorf("config: %s must not contain an all-access key (%s); use a key restricted to pods, catalog and billing", PodKeyFile, name)
		}
	}
	for name := range vars {
		if name != "RUNPOD_POD_KEY" {
			return PodKey{}, false, fmt.Errorf("config: %s: %s is not a known variable", PodKeyFile, name)
		}
	}
	if vars["RUNPOD_POD_KEY"] == "" {
		return PodKey{}, false, fmt.Errorf("config: %s: RUNPOD_POD_KEY missing", PodKeyFile)
	}
	return PodKey{key: vars["RUNPOD_POD_KEY"]}, true, nil
}

// PodPollKey is the HMAC key of the pod poll capability URLs (C390.6): HMAC-SHA256(Secrets.Key, "metamusic/pod-poll/v1"), no
// new secret file.
func (s Secrets) PodPollKey() []byte { return s.derive("metamusic/pod-poll/v1") }
