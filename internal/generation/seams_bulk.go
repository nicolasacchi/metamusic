package generation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The pod bulk seam (BULK-P6, C398). generation never imports the pods package: the app hands over the numbers an estimate needs.

// Safety verdicts of PodSafety, in the words get_provider_status uses.
const (
	PodSafetyVerified     = "verified"
	PodSafetyFailed       = "failed"
	PodSafetyInconclusive = "inconclusive"
	PodSafetyUnverified   = "unverified"
)

// ReasonPodSafetyUnverified is the disabled reason of an ordinary pod estimate while the latest self-termination canary for the image
// digest is not verified (D-BULK-12).
const ReasonPodSafetyUnverified = "pod_safety_unverified"

// PodConfig is the pod backend's side of an estimate. A Service without one refuses every pod estimate (503 UNAVAILABLE): money must
// never be priced by the serverless path for a pod batch.
type PodConfig struct {
	// MinTakes and MaxTakes bound an ordinary pod batch (MM_POD_MIN_TAKES, MM_POD_MAX_TAKES).
	MinTakes, MaxTakes int
	// BootS, IdleS and SlackS are MM_POD_BOOT_S, MM_POD_IDLE_S and MM_POD_SLACK_S; MaxRuntimeS is MM_POD_MAX_RUNTIME in seconds.
	BootS, IdleS, SlackS, MaxRuntimeS int
	// MaxRecreate is the number of re-creations the ceiling pays for (one, D-BULK-8).
	MaxRecreate int
	// GPUTypes are the ordered catalog types a pod may be created on (MM_POD_GPU_TYPES); they are frozen into the quote.
	GPUTypes []string
	// RequireCanary makes an ordinary estimate need a verified canary for the image digest (false for the simulated backend).
	RequireCanary bool
}

// UsePods wires the pod estimate rules; the app calls it when the pod backend is built. Without it a pod estimate is refused.
func (s *Service) UsePods(c PodConfig) {
	c.GPUTypes = append([]string(nil), c.GPUTypes...)
	s.pods = &c
}

// Pods returns the pod estimate configuration the service was given, and whether it was given one.
func (s *Service) Pods() (PodConfig, bool) {
	if s.pods == nil {
		return PodConfig{}, false
	}
	c := *s.pods
	c.GPUTypes = append([]string(nil), c.GPUTypes...)
	return c, true
}

// PodSafety is the verdict of the latest canary row of pod_safety_log for an image digest: verified, failed, inconclusive, or
// unverified when there is none (or no digest).
func PodSafety(ctx context.Context, q QueryReader, digest string) (string, error) {
	if digest == "" {
		return PodSafetyUnverified, nil
	}
	var verdict string
	err := q.QueryRowContext(ctx, `SELECT verdict FROM pod_safety_log WHERE kind = 'canary' AND image_digest = ? ORDER BY id DESC LIMIT 1`, digest).Scan(&verdict)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return PodSafetyUnverified, nil
	case err != nil:
		return "", fmt.Errorf("generation: pod safety: %w", err)
	}
	return verdict, nil
}
