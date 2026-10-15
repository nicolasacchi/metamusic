package store

import "time"

// Env is the small shared type every Reconciler-called function takes (C87): the pass clock, the id source and the
// actor written into created_by/updated_by columns ("system:provenance" for the Reconciler).
type Env struct {
	Now   time.Time
	NewID func() string
	Actor string
}
