package desk

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// The plan bundle (C361.2): one uncompressed POSIX tar per ok seed of a plan job. It is untrusted worker output. The reader below
// never writes a tar member to disk under a name the tar chose: it reads the allowlisted members into memory, each bounded by its
// own limit, and the service stores them itself under plans/<id>/.
const (
	// maxBundleFiles is the number of members a bundle may have (the allowlist is five names).
	maxBundleFiles = 5
	// maxBundleTextBytes bounds score.abc and plan.json.
	maxBundleTextBytes = 64 << 10
	// maxBundleAuxBytes bounds each optional member.
	maxBundleAuxBytes = 1 << 20

	planJSONName     = "plan.json"
	tokensName       = "abc_tokens.npy" // #nosec G101 -- a file name, not a credential
	prefixName       = "prefix.npy"
	planManifestName = "plan_manifest.json"
)

// bundleLimits is the allowlist of member names with their size limits.
var bundleLimits = map[string]int64{
	scoreName:        maxBundleTextBytes,
	planJSONName:     maxBundleTextBytes,
	tokensName:       maxBundleAuxBytes,
	prefixName:       maxBundleAuxBytes,
	planManifestName: maxBundleAuxBytes,
}

// bundleOrder is the order members are stored in (stable rows and directory listings).
var bundleOrder = []string{scoreName, planJSONName, tokensName, prefixName, planManifestName}

// Rules of a refused bundle (the detail of a plan_bundle_invalid event).
const (
	RuleBundleTar      = "tar"
	RuleBundleType     = "member_type"
	RuleBundleName     = "member_name"
	RuleBundleDup      = "duplicate"
	RuleBundleSize     = "member_size"
	RuleBundleOver     = "bundle_size"
	RuleBundleMissing  = "member_missing"
	RuleBundleUTF8     = "score_utf8"
	RuleBundleEmpty    = "empty_member"
	RuleBundlePlanJSON = "plan_json"
	RuleBundleNPY      = "npy"
	RuleBundleManifest = "manifest"
	RuleBundleMismatch = "plan_mismatch"
	RuleBundleScore    = "score_invalid"
)

// bundleError is a refused bundle: the seed is lost, the other seeds of the job go on.
type bundleError struct{ rule, msg string }

func (e *bundleError) Error() string { return "desk: plan bundle: " + e.rule + ": " + e.msg }

func bad(rule, format string, a ...any) error {
	return &bundleError{rule: rule, msg: fmt.Sprintf(format, a...)}
}

// planMeta is plan.json.
type planMeta struct {
	Seed      int64
	Cot       string
	Truncated bool
	ABCTokens *int
	Seconds   float64
	YuE2      string
}

// bundle is a verified plan bundle: its members by name and the parsed plan.json.
type bundle struct {
	files map[string][]byte
	meta  planMeta
}

func (b *bundle) score() []byte { return b.files[scoreName] }

// readBundle verifies raw, a tar of at most declared bytes: regular files only, names from the allowlist (so no link, directory,
// absolute or .. name, and no other name), none twice, each within its size limit and exactly the size its header declares, the
// two required members present and valid. A violation is a *bundleError.
func readBundle(raw []byte, declared int64) (*bundle, error) {
	if int64(len(raw)) > declared || int64(len(raw)) > 2<<20 {
		return nil, bad(RuleBundleOver, "the tar is %d bytes, declared %d", len(raw), declared)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	b := &bundle{files: map[string][]byte{}}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, bad(RuleBundleTar, "the archive is malformed")
		}
		if len(b.files) >= maxBundleFiles {
			return nil, bad(RuleBundleName, "more members than the allowlist has")
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, bad(RuleBundleType, "member %q is not a regular file", safeName(hdr.Name))
		}
		limit, ok := bundleLimits[hdr.Name]
		if !ok || hdr.Linkname != "" {
			return nil, bad(RuleBundleName, "member %q is not in the allowlist", safeName(hdr.Name))
		}
		if _, dup := b.files[hdr.Name]; dup {
			return nil, bad(RuleBundleDup, "member %q appears twice", hdr.Name)
		}
		if hdr.Size < 0 || hdr.Size > limit {
			return nil, bad(RuleBundleSize, "member %q is %d bytes, at most %d", hdr.Name, hdr.Size, limit)
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil || int64(len(body)) != hdr.Size {
			return nil, bad(RuleBundleTar, "member %q is truncated", hdr.Name)
		}
		if len(body) == 0 {
			return nil, bad(RuleBundleEmpty, "member %q is empty", hdr.Name)
		}
		b.files[hdr.Name] = body
	}
	for _, need := range []string{scoreName, planJSONName} {
		if _, ok := b.files[need]; !ok {
			return nil, bad(RuleBundleMissing, "member %q is missing", need)
		}
	}
	if !utf8.Valid(b.score()) || bytes.IndexByte(b.score(), 0) >= 0 {
		return nil, bad(RuleBundleUTF8, "score.abc is not UTF-8 text")
	}
	meta, err := parsePlanJSON(b.files[planJSONName])
	if err != nil {
		return nil, err
	}
	b.meta = meta
	for _, n := range []string{tokensName, prefixName} {
		if body, ok := b.files[n]; ok && !bytes.HasPrefix(body, []byte("\x93NUMPY")) {
			return nil, bad(RuleBundleNPY, "%s is not a numpy file", n)
		}
	}
	if body, ok := b.files[planManifestName]; ok {
		var obj map[string]json.RawMessage
		if json.Unmarshal(body, &obj) != nil || obj == nil {
			return nil, bad(RuleBundleManifest, "plan_manifest.json is not a JSON object")
		}
	}
	return b, nil
}

// safeName shortens a member name for a message (the name is worker-chosen).
func safeName(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return string(bytes.ToValidUTF8([]byte(s), []byte("?")))
}

var planJSONKeys = []string{"seed", "cot", "truncated", "abc_tokens", "seconds", "yue2"}

// parsePlanJSON reads plan.json strictly: a JSON object with exactly the six keys of C361.2 and their types.
func parsePlanJSON(raw []byte) (planMeta, error) {
	var m planMeta
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return m, bad(RuleBundlePlanJSON, "plan.json is not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return m, bad(RuleBundlePlanJSON, "plan.json has trailing data")
	}
	if len(obj) != len(planJSONKeys) {
		return m, bad(RuleBundlePlanJSON, "plan.json must have exactly the keys %v", planJSONKeys)
	}
	for _, k := range planJSONKeys {
		if _, ok := obj[k]; !ok {
			return m, bad(RuleBundlePlanJSON, "plan.json lacks %q", k)
		}
	}
	num := func(k string) (json.Number, bool) {
		var n json.Number
		d := json.NewDecoder(bytes.NewReader(obj[k]))
		d.UseNumber()
		return n, d.Decode(&n) == nil && n != ""
	}
	seed, ok := num("seed")
	if !ok {
		return m, bad(RuleBundlePlanJSON, "seed is not a number")
	}
	var err error
	if m.Seed, err = seed.Int64(); err != nil || m.Seed < 0 {
		return m, bad(RuleBundlePlanJSON, "seed is not a non-negative integer")
	}
	if json.Unmarshal(obj["cot"], &m.Cot) != nil || (m.Cot != "full" && m.Cot != "melody") {
		return m, bad(RuleBundlePlanJSON, "cot is not full or melody")
	}
	if json.Unmarshal(obj["truncated"], &m.Truncated) != nil || string(obj["truncated"]) != "true" && string(obj["truncated"]) != "false" {
		return m, bad(RuleBundlePlanJSON, "truncated is not a boolean")
	}
	if string(obj["abc_tokens"]) != "null" {
		n, ok := num("abc_tokens")
		v, err := n.Int64()
		if !ok || err != nil || v < 0 || v > math.MaxInt32 {
			return m, bad(RuleBundlePlanJSON, "abc_tokens is not null or a non-negative integer")
		}
		t := int(v)
		m.ABCTokens = &t
	}
	secs, ok := num("seconds")
	if m.Seconds, err = secs.Float64(); !ok || err != nil || m.Seconds < 0 || math.IsInf(m.Seconds, 0) {
		return m, bad(RuleBundlePlanJSON, "seconds is not a non-negative number")
	}
	if json.Unmarshal(obj["yue2"], &m.YuE2) != nil || m.YuE2 == "" || len(m.YuE2) > 64 {
		return m, bad(RuleBundlePlanJSON, "yue2 is not a version string")
	}
	return m, nil
}
