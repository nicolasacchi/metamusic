package runpod

import (
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// KeysFor selects the keys of an engine's endpoint from runpod.env: yue2 keeps the keys as loaded; acestep15 uses
// RUNPOD_ACESTEP_ENDPOINT_ID and, when set, RUNPOD_ACESTEP_JOBS_KEY (else the shared jobs key, C147.2). The read
// and write keys are account-wide and shared. The result is empty of an endpoint id when the engine's endpoint
// is not configured; the caller then builds no backend (reason keys_absent).
func KeysFor(k config.RunPodKeys, engine string) config.RunPodKeys {
	if engine != provider.EngineACEStep {
		return k
	}
	k.EndpointID = k.ACEStepEndpointID
	if k.ACEStepJobsKey != "" {
		k.Jobs = k.ACEStepJobsKey
	}
	return k
}

// E2 freeze (C151.1): the Backend methods the Set routes by. Lane E2-PR owns this file from here on (an EngineProfile
// gives the ACE-Step backend its own engine, cold start and runtime).

// ColdStartS is the provisioned cold start allowance (MM_COLD_START_S for the yue2 endpoint).
func (b *Backend) ColdStartS() int { return b.o.ColdStartS }

// Engine is the engine this backend renders.
func (b *Backend) Engine() string {
	if b.o.Engine == "" {
		return provider.EngineYuE2
	}
	return b.o.Engine
}

// MaxRuntime is the provisioned endpoint timeout (MM_JOB_MAX_RUNTIME for the yue2 endpoint).
func (b *Backend) MaxRuntime() time.Duration { return b.o.MaxRuntime }
