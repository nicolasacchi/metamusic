package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
)

// EnvInput holds the identifying fields of an environment fingerprint (D-M3-26). GPU name, CUDA, torch,
// the worker build and python are non-identifying detail and live beside it, not in the id.
type EnvInput struct {
	Engine, PkgVersion, CodeCommit string
	Weights                        map[string]string
	Decoder, Backend, GPUClass     string
	ImageDigest                    string
}

// EnvID is sha256 over the canonical JSON of the identifying fields (D-M3-26, C75.6).
func EnvID(in EnvInput) (string, error) {
	w := in.Weights
	if w == nil {
		w = map[string]string{}
	}
	raw, err := json.Marshal(map[string]any{
		"engine": in.Engine, "pkg_version": in.PkgVersion, "code_commit": in.CodeCommit, "weights": w,
		"decoder": in.Decoder, "backend": in.Backend, "gpu_class": in.GPUClass, "image_digest": in.ImageDigest,
	})
	if err != nil {
		return "", err
	}
	canon, err := canonjson.Canonical(raw)
	if err != nil {
		return "", fmt.Errorf("jobs: env id: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

var rtxA = regexp.MustCompile(`^RTX (A\d+)$`)

// GPUClass normalises a worker-reported GPU name (C75.6): "NVIDIA RTX A5000" is "A5000", the GeForce
// RTX 3090 and 4090 are "RTX 3090" and "RTX 4090", "NVIDIA L4" is "L4"; any other name keeps everything
// after the "NVIDIA " prefix.
func GPUClass(name string) string {
	n := strings.TrimSpace(name)
	n = strings.TrimPrefix(n, "NVIDIA ")
	switch n {
	case "GeForce RTX 3090":
		return "RTX 3090"
	case "GeForce RTX 4090":
		return "RTX 4090"
	}
	if m := rtxA.FindStringSubmatch(n); m != nil {
		return m[1]
	}
	return n
}

var digestRE = regexp.MustCompile(`@(sha256:[0-9a-f]{64})$`)

// ImageDigestFor is the digest of a worker record's image when it carries one, else "build:<worker_build>"
// from the worker's own output (D-M3-26).
func ImageDigestFor(image, workerBuild string) string {
	if m := digestRE.FindStringSubmatch(strings.TrimSpace(image)); m != nil {
		return m[1]
	}
	if workerBuild == "" {
		workerBuild = "unknown"
	}
	return "build:" + workerBuild
}

// imageDigestOf resolves the image digest of an attempt: the digest recorded on the attempt, else the
// image of the worker lifetime that ran it (worker_meter), else the build the worker reported.
func imageDigestOf(ctx context.Context, x Querier, attemptDigest, workerKey, workerBuild string) (string, error) {
	if attemptDigest != "" {
		return attemptDigest, nil
	}
	image := ""
	if workerKey != "" {
		rows, err := x.QueryContext(ctx, `SELECT COALESCE(image, '') FROM worker_meter WHERE worker_key = ? ORDER BY started_at DESC`, workerKey)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var im string
			if err := rows.Scan(&im); err != nil {
				return "", err
			}
			if digestRE.MatchString(strings.TrimSpace(im)) {
				image = im
				break
			}
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
	}
	return ImageDigestFor(image, workerBuild), nil
}
