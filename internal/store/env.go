package store

import (
	"log/slog"
	"time"
)

// Env is the small shared type every Reconciler-called function takes (C87): the pass clock, the id source and the
// actor written into created_by/updated_by columns ("system:provenance" for the Reconciler).
type Env struct {
	Now   time.Time
	NewID func() string
	Actor string
	// Log receives ids-only records (never Muse text, C99.4); nil => slog.Default().
	Log *slog.Logger
}

// Logger returns Log, or the default logger when unset.
func (e Env) Logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}
