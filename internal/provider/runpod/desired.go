package runpod

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Provisioned constants (C77.6).
const (
	DefaultEndpointName = "mm-yue2"
	provisionIdleS      = 20
	provisionCUDA       = "12.8"
	// CachedModelDir is where RunPod mounts the endpoint's cached models. RunPod allows ONE cached model per
	// endpoint (the 3B); a second cached model (the VAE, --vae-source cached) is not offered.
	CachedModelDir = "/runpod-volume/huggingface-cache/hub"
	// ImageVAEDir is the Hugging Face hub cache baked into the worker image (worker/Dockerfile), holding
	// m-a-p/YuE2-Vae at the pinned revision. It is the default VAE source.
	ImageVAEDir = "/opt/mm/hf-cache/hub"
	// excludedGPUName is the catalog name of the type left out of the pools (C77.6).
	excludedGPUName = "L4"
)

// Errors of the desired spec.
var (
	// ErrTagImage: images are pinned by digest; a tag could move under a running endpoint.
	ErrTagImage = errors.New("runpod: provision: the image must be pinned by digest (name@sha256:<64 hex>), not a tag")
	// ErrImageRequired: creating an endpoint needs an image.
	ErrImageRequired = errors.New("runpod: provision: --image name@sha256:<digest> is required to create the endpoint")
)

var (
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	poolRe  = regexp.MustCompile(`^[A-Z0-9_]{2,40}$`)
	pathRe  = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)
)

// DesiredConfig is the part of the desired endpoint that does not come from the weights or the catalog.
type DesiredConfig struct {
	Name       string        // default mm-yue2
	ColdStartS int           // MM_COLD_START_S: queueDelay = ColdStartS + 60
	MaxRuntime time.Duration // MM_JOB_MAX_RUNTIME: the endpoint timeout (sent in milliseconds)
	Pools      []string      // default AMPERE_24, ADA_24
	VAESource  string        // "image" (default) | "cached" | "volume:<absolute path>"
	// TimeoutCase adds MM_WORKER_DEADLINE=provider for the canary's timeout case only.
	TimeoutCase bool
}

// DefaultPools are the GPU pools of C77.6, in preference order.
func DefaultPools() []string { return []string{"AMPERE_24", "ADA_24"} }

// CheckImage accepts only an image pinned by digest.
func CheckImage(image string) error {
	if !imageRe.MatchString(image) {
		return ErrTagImage
	}
	return nil
}

// DesiredSpec is the complete create body of C77.6, every field pinned. weights maps the Hugging Face
// repository to its pinned revision (yue2.Weights); image is name@sha256:<digest> (empty leaves the image
// out, for diffs against an existing endpoint); maxWorkers is 0 (inactive), 1 or 2.
func DesiredSpec(c DesiredConfig, weights map[string]string, image string, maxWorkers int, catalog []GPUType) (EndpointSpec, error) {
	if c.Name == "" {
		c.Name = DefaultEndpointName
	}
	if c.ColdStartS <= 0 || c.MaxRuntime <= 0 {
		return EndpointSpec{}, errors.New("runpod: provision: cold start and max runtime must be positive")
	}
	if maxWorkers < 0 || maxWorkers > 2 {
		return EndpointSpec{}, errors.New("runpod: provision: workers.max must be 0, 1 or 2")
	}
	if image != "" {
		if err := CheckImage(image); err != nil {
			return EndpointSpec{}, err
		}
	}
	pools := c.Pools
	if len(pools) == 0 {
		pools = DefaultPools()
	}
	for _, p := range pools {
		if !poolRe.MatchString(p) {
			return EndpointSpec{}, fmt.Errorf("runpod: provision: invalid GPU pool %q", p)
		}
	}
	vae, err := vaeDir(c.VAESource)
	if err != nil {
		return EndpointSpec{}, err
	}
	rev3, revVAE := weights[yue2.WeightsRepo3B], weights[yue2.WeightsRepoVAE]
	if rev3 == "" || revVAE == "" {
		return EndpointSpec{}, errors.New("runpod: provision: the weights revisions are not pinned")
	}
	env := map[string]string{
		"MODE":              "serverless",
		"HF_HUB_OFFLINE":    "1",
		yue2.Env3BRevision:  rev3,
		yue2.EnvVAERevision: revVAE,
		"MM_YUE2_VAE_DIR":   vae,
	}
	if c.TimeoutCase {
		env["MM_WORKER_DEADLINE"] = "provider"
	}
	idle := provisionIdleS
	excluded := []string{}
	for _, g := range catalog {
		if strings.EqualFold(g.Name, excludedGPUName) && containsStr(pools, g.Pool) {
			excluded = append(excluded, g.ID) // only inside a listed pool: otherwise RunPod answers 400
		}
	}
	return EndpointSpec{
		Name:    c.Name,
		Type:    "QUEUE",
		Scaling: ScalingSpec{Type: "QUEUE_DELAY", QueueDelay: c.ColdStartS + 60},
		Workers: WorkersSpec{Min: 0, Max: maxWorkers, IdleTimeout: &idle},
		GPU: GPUSpec{Count: 1, Pools: append([]string(nil), pools...), ExcludedTypes: excluded,
			MinCudaVersion: provisionCUDA},
		Flashboot: "FLASHBOOT",
		Timeout:   c.MaxRuntime.Milliseconds(),
		Image:     image,
		Env:       env,
	}, nil
}

func vaeDir(src string) (string, error) {
	switch {
	case src == "" || src == "image":
		return ImageVAEDir, nil
	case src == "cached":
		return CachedModelDir, nil
	case strings.HasPrefix(src, "volume:"):
		p := strings.TrimPrefix(src, "volume:")
		if !pathRe.MatchString(p) || strings.Contains(p, "..") {
			return "", errors.New("runpod: provision: --vae-source volume:PATH needs a plain absolute path")
		}
		return p, nil
	}
	return "", errors.New("runpod: provision: --vae-source must be image, cached or volume:PATH")
}
