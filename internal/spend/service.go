// Package spend is the money core of generation: quote math, confirmation tokens, estimates, caps,
// the append-only ledger and approvals (M3 C72).
package spend

import (
	"context"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options builds a Service.
type Options struct {
	Store      *store.Store
	Now        func() time.Time // nil => time.Now
	NewID      func() string    // nil => UUIDv7
	Rand       io.Reader        // confirmation tokens; nil => crypto/rand
	ConfirmKey []byte           // HMAC key of the stored token hash (derived, C68)
	Location   *time.Location   // calendar boundaries of the caps; nil => time.Local
	Observer   Observer         // test hook: called after every committed ledger transaction
}

// Service owns every spend SQL statement.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
	rand  io.Reader
	key   []byte
	loc   *time.Location

	ledger *Ledger
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	s := &Service{st: o.Store, now: o.Now, newID: o.NewID, rand: o.Rand, key: o.ConfirmKey, loc: o.Location}
	s.ledger = newLedger(o.Now, o.Observer, func(ctx context.Context, who string) (Usage, error) {
		return s.Usage(ctx, s.st.R, who, s.now())
	})
	return s
}
