package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
)

// UploadFallback is <SecretsDir>/r2.env (C390.4): a private S3-compatible bucket the worker can put artifacts into when
// the app is unreachable. The file keeps the name r2.env whichever S3 provider is used. It never prints a value.
type UploadFallback struct {
	Endpoint        string // R2_ENDPOINT: https, host only, no path
	Bucket          string // R2_BUCKET: S3 bucket-name rules
	AccessKeyID     string // R2_ACCESS_KEY_ID
	SecretAccessKey string // R2_SECRET_ACCESS_KEY
	Region          string // R2_REGION, default "auto"
	Prefix          string // R2_PREFIX, default "mm/"
}

// String reports presence only.
func (u UploadFallback) String() string {
	return fmt.Sprintf("UploadFallback{endpoint:%s bucket:%s keys:%s}", set(u.Endpoint), set(u.Bucket), set(u.AccessKeyID+u.SecretAccessKey))
}

// GoString is String, so %#v cannot leak either.
func (u UploadFallback) GoString() string { return u.String() }

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRE = regexp.MustCompile(`^[a-z0-9-]{1,16}/$`)
	regionRE = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
)

var r2Vars = map[string]bool{
	"R2_ENDPOINT": true, "R2_BUCKET": true, "R2_ACCESS_KEY_ID": true, "R2_SECRET_ACCESS_KEY": true, "R2_REGION": true, "R2_PREFIX": true,
}

// LoadUploadFallback reads <dir>/r2.env once (the caller does it at startup; rotation is a restart). It returns
// (zero, false, nil) when the file is absent. The file mode must have no group or other bit. Errors name the file and
// the variable, never a value.
func LoadUploadFallback(dir string) (UploadFallback, bool, error) {
	path := filepath.Join(dir, "r2.env")
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return UploadFallback{}, false, nil
	}
	if err != nil {
		return UploadFallback{}, false, fmt.Errorf("config: reading r2.env: %w", errors.Unwrap(err))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return UploadFallback{}, false, errors.New("config: r2.env must not be readable by group or others (chmod 600)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return UploadFallback{}, false, fmt.Errorf("config: reading r2.env: %w", errors.Unwrap(err))
	}
	vars, err := parseDotenv("r2.env", raw)
	if err != nil {
		return UploadFallback{}, false, err
	}
	for name := range vars {
		if !r2Vars[name] {
			return UploadFallback{}, false, fmt.Errorf("config: r2.env: %s is not a known variable", name)
		}
	}
	u := UploadFallback{
		Endpoint: vars["R2_ENDPOINT"], Bucket: vars["R2_BUCKET"], AccessKeyID: vars["R2_ACCESS_KEY_ID"],
		SecretAccessKey: vars["R2_SECRET_ACCESS_KEY"], Region: vars["R2_REGION"], Prefix: vars["R2_PREFIX"],
	}
	for _, req := range []struct{ name, val string }{
		{"R2_ENDPOINT", u.Endpoint}, {"R2_BUCKET", u.Bucket}, {"R2_ACCESS_KEY_ID", u.AccessKeyID}, {"R2_SECRET_ACCESS_KEY", u.SecretAccessKey},
	} {
		if req.val == "" {
			return UploadFallback{}, false, fmt.Errorf("config: r2.env: %s missing", req.name)
		}
	}
	if p, err := url.Parse(u.Endpoint); err != nil || p.Scheme != "https" || p.Host == "" || p.User != nil ||
		(p.Path != "" && p.Path != "/") || p.RawQuery != "" || p.Fragment != "" || p.Opaque != "" {
		return UploadFallback{}, false, errors.New("config: r2.env: R2_ENDPOINT must be an https URL with a host and no path")
	}
	if !bucketRE.MatchString(u.Bucket) {
		return UploadFallback{}, false, errors.New("config: r2.env: R2_BUCKET is not a valid bucket name")
	}
	if u.Region == "" {
		u.Region = "auto"
	} else if !regionRE.MatchString(u.Region) {
		return UploadFallback{}, false, errors.New("config: r2.env: R2_REGION is not a valid region")
	}
	if u.Prefix == "" {
		u.Prefix = "mm/"
	} else if !prefixRE.MatchString(u.Prefix) {
		return UploadFallback{}, false, errors.New("config: r2.env: R2_PREFIX must match ^[a-z0-9-]{1,16}/$")
	}
	return u, true, nil
}
