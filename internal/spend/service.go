// Package spend is the money core of generation: quote math, confirmation tokens, estimates, caps,
// the append-only ledger and approvals (M3 C72). M3-B2 lands the constructor and its options so the
// wiring can be frozen; M3-B3 replaces the stubs.
package spend

import (
	"errors"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a STUB(M3-B3) body.
var ErrNotImplemented = errors.New("spend: not implemented (STUB(M3-B3))")

// Options builds a Service.
type Options struct {
	Store      *store.Store
	Now        func() time.Time // nil => time.Now
	NewID      func() string    // nil => UUIDv7
	Rand       io.Reader        // confirmation tokens; nil => crypto/rand
	ConfirmKey []byte           // HMAC key of the stored token hash (derived, C68)
	Location   *time.Location   // calendar boundaries of the caps; nil => time.Local
}

// Service owns every spend SQL statement.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
	rand  io.Reader
	key   []byte
	loc   *time.Location
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	return &Service{st: o.Store, now: o.Now, newID: o.NewID, rand: o.Rand, key: o.ConfirmKey, loc: o.Location}
}
