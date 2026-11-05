package runpod

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Endpoint names and image digest files of the two engines (C155.1).
const (
	ACEStepEndpointName = "mm-acestep"
	// hubURL prefixes a Hugging Face repository in a ModelRef.
	hubURL = "https://huggingface.co/"
	// Yue2ImageDigestFile and ACEStepImageDigestFile name the committed digest of each worker image.
	Yue2ImageDigestFile    = "deploy/worker-image.digest"
	ACEStepImageDigestFile = "deploy/worker-acestep-image.digest"
	// defaultLoadDeadlineS is the worker's model-load watchdog written to the ACE-Step endpoint (C154.5).
	defaultLoadDeadlineS = 420
)

// ModelRef is one cached model to attach in the RunPod console: the Hugging Face repository URL and the pinned
// revision (never ":main": a moving reference could mount a snapshot the worker refuses, C154.2).
type ModelRef struct {
	URL      string
	Revision string
}

// Repo is the "org/name" form of the reference.
func (m ModelRef) Repo() string { return strings.TrimPrefix(m.URL, hubURL) }

// EngineProfile is what differs between the endpoints of the engines (C155.1). ModelRefs holds ONE entry on
// RunPod today: the documented limit is one cached model per endpoint (E2-P0a), and the ACE-Step base DiT
// is baked into the worker image instead.
type EngineProfile struct {
	Engine, EndpointName string
	Weights              map[string]string // repo -> pinned revision
	ModelRefs            []ModelRef
	Env                  map[string]string // beyond MODE/HF_HUB_OFFLINE
	ColdStartS           int
	MaxRuntime           time.Duration
	ImageDigestFile      string

	// envFn derives the pool-dependent part of the environment (nil: Env only).
	envFn func(pools []string) (map[string]string, error)
	err   error
}

// env is the complete endpoint environment for the pools.
func (p EngineProfile) env(pools []string) (map[string]string, error) {
	out := map[string]string{"MODE": "serverless", "HF_HUB_OFFLINE": "1"}
	for k, v := range p.Env {
		out[k] = v
	}
	if p.envFn != nil {
		more, err := p.envFn(pools)
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			out[k] = v
		}
	}
	return out, nil
}

// Yue2Profile reproduces main's DesiredSpec input exactly (TestDesiredSpec_Yue2Unchanged).
func Yue2Profile(cfg DesiredConfig) EngineProfile { return yue2ProfileWith(cfg, yue2.Weights) }

func yue2ProfileWith(cfg DesiredConfig, weights map[string]string) EngineProfile {
	p := EngineProfile{Engine: "yue2", EndpointName: DefaultEndpointName, Weights: weights, ColdStartS: cfg.ColdStartS,
		MaxRuntime: cfg.MaxRuntime, ImageDigestFile: Yue2ImageDigestFile}
	vae, err := vaeDir(cfg.VAESource)
	if err != nil {
		p.err = err
		return p
	}
	rev3, revVAE := weights[yue2.WeightsRepo3B], weights[yue2.WeightsRepoVAE]
	if rev3 == "" || revVAE == "" {
		p.err = errors.New("runpod: provision: the weights revisions are not pinned")
		return p
	}
	p.ModelRefs = []ModelRef{{URL: hubURL + yue2.WeightsRepo3B, Revision: rev3}}
	p.Env = map[string]string{
		yue2.Env3BRevision:  rev3,
		yue2.EnvVAERevision: revVAE,
		"MM_YUE2_VAE_DIR":   vae,
		yue2.Env3BDir:       CachedModelDir,
	}
	if cfg.TimeoutCase {
		p.Env["MM_WORKER_DEADLINE"] = "provider"
	}
	return p
}

// ErrBadTasks: --tasks / MM_ACESTEP_TASKS is not one of the task lists an endpoint can be provisioned for.
var ErrBadTasks = errors.New(`runpod: provision: tasks must be "turbo" or "turbo,base"`)

var vramRe = regexp.MustCompile(`(?:^|_)(\d{2,3})(?:_|$)`)

// PoolVRAMGB reads the GPU memory class from a RunPod pool id (AMPERE_24 -> 24, ADA_48_PRO -> 48).
func PoolVRAMGB(pool string) (int, bool) {
	m := vramRe.FindStringSubmatch(pool)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

// ACEStepProfile is the ACE-Step 1.5 endpoint (C155.1, C147.3). tasks is the endpoint's task list ("turbo" or
// "turbo,base"; the base DiT is then baked into the image, the one cached model stays the main bundle). An
// invalid list makes DesiredSpecFor fail with ErrBadTasks.
func ACEStepProfile(cfg DesiredConfig, tasks string) EngineProfile {
	p := EngineProfile{Engine: acestep.Name, EndpointName: ACEStepEndpointName, ColdStartS: cfg.ColdStartS,
		MaxRuntime: cfg.MaxRuntime, ImageDigestFile: ACEStepImageDigestFile}
	norm := acestep.NormalizeTasks(tasks)
	if norm == "" {
		p.err = ErrBadTasks
		return p
	}
	p.Weights = acestep.WeightsFor(norm)
	p.ModelRefs = []ModelRef{{URL: hubURL + acestep.WeightsRepoMain, Revision: acestep.Weights[acestep.WeightsRepoMain]}}
	p.Env = acestep.WeightsEnv(norm)
	p.Env["ENGINE"] = acestep.Name
	p.Env["MM_ACESTEP_MODELS_DIR"] = CachedModelDir
	p.Env["MM_LOAD_DEADLINE_S"] = strconv.Itoa(defaultLoadDeadlineS)
	if cfg.TimeoutCase {
		p.Env["MM_WORKER_DEADLINE"] = "provider"
	}
	feat := cfg.Features
	p.envFn = func(pools []string) (map[string]string, error) {
		f := acestep.EmbeddedFeatures()
		if feat != nil {
			f = *feat
		}
		vram := 0
		for _, pl := range pools {
			g, ok := PoolVRAMGB(pl)
			if !ok {
				return nil, fmt.Errorf("runpod: provision: pool %q does not name its GPU memory (expected an id such as AMPERE_24)", pl)
			}
			if vram == 0 || g < vram {
				vram = g
			}
		}
		return map[string]string{
			"MM_ACESTEP_MAX_SECONDS":  strconv.Itoa(f.MaxSFor(pools)),
			"MM_ACESTEP_POOL_VRAM_GB": strconv.Itoa(vram),
		}, nil
	}
	return p
}

// ProfileFor selects the profile of an engine name ("yue2" or "acestep15"; "" is yue2).
func ProfileFor(engine string, cfg DesiredConfig, tasks string) (EngineProfile, error) {
	switch engine {
	case "", "yue2":
		return Yue2Profile(cfg), nil
	case acestep.Name:
		if tasks == "" {
			tasks = acestep.TasksTurbo
		}
		return ACEStepProfile(cfg, tasks), nil
	}
	return EngineProfile{}, fmt.Errorf("runpod: unknown engine %q (want yue2 or acestep15)", engine)
}
