package fake

import "time"

// E2 freeze (C151.1): the Backend methods the Set routes by. Lane E2-PF owns this file from here on: it adds the
// fake-acestep instance (its own name, engine and cold start); until then the fake serves yue2 only.

// ColdStartS is the fake model's cold start allowance (the same value the endpoint config advertises).
func (b *Backend) ColdStartS() int { return b.o.ColdStartS }

// Engine is the engine this backend renders.
func (b *Backend) Engine() string { return "yue2" }

// MaxRuntime is the endpoint execution timeout of the simulation.
func (b *Backend) MaxRuntime() time.Duration { return endpointTimeoutMS * time.Millisecond }
