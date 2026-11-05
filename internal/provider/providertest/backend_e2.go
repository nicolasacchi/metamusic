package providertest

import "time"

// E2 freeze (C151.1): the Backend methods the Set routes by. The scripted backend serves yue2 with fixed values.

// ColdStartS is a fixed 180 s (MM_COLD_START_S default).
func (b *Backend) ColdStartS() int { return 180 }

// Engine is the engine this backend renders.
func (b *Backend) Engine() string { return "yue2" }

// MaxRuntime is a fixed 15 minutes (MM_JOB_MAX_RUNTIME default).
func (b *Backend) MaxRuntime() time.Duration { return 15 * time.Minute }
