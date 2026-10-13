package lineage

import (
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrUnconfigured means the service was built without a store (registry tests); operations answer 503.
var ErrUnconfigured = errors.New("lineage: no store configured")

// ErrNotFound means the track does not exist.
var ErrNotFound = errors.New("lineage: not found")

// Options builds a Service.
type Options struct {
	Store *store.Store
	Now   func() time.Time // nil => time.Now
}

// Service reads the Operator Ledger (lineage trees) and keeps the recipe keys current (C71.6, C71.7).
type Service struct {
	st        *store.Store
	now       func() time.Time
	pageBytes int // the page cap; tests lower it per service (export_test.go)
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{st: o.Store, now: o.Now, pageBytes: maxPageBytes}
}
