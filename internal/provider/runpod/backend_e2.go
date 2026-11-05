package runpod

import "time"

// E2 freeze (C151.1): the Backend methods the Set routes by. Lane E2-PR owns this file from here on (an EngineProfile
// gives the ACE-Step backend its own engine, cold start and runtime).

// ColdStartS is the provisioned cold start allowance (MM_COLD_START_S for the yue2 endpoint).
func (b *Backend) ColdStartS() int { return b.o.ColdStartS }

// Engine is the engine this backend renders.
func (b *Backend) Engine() string { return "yue2" }

// MaxRuntime is the provisioned endpoint timeout (MM_JOB_MAX_RUNTIME for the yue2 endpoint).
func (b *Backend) MaxRuntime() time.Duration { return b.o.MaxRuntime }
