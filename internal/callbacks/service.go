// Package callbacks holds Callback Listening (C255, D-M5-4..6): a callback per addressed comment of a Song PR child, the
// human and critic verdicts on them, the comment transitions they cause, and the backlog gate that paces agent spend.
// The service implements jobs.TakeHook (the take transaction creates the callbacks) and generation.BacklogGate.
package callbacks

import (
	"errors"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options builds the service. A zero Options gives a service whose reads and writes answer errors.ErrUnsupported (501 at the
// API) and whose hook does nothing.
type Options struct {
	Store      *store.Store
	Now        func() time.Time
	NewID      func() string // UUIDv7 ids of callbacks, verdicts and regression comments
	Notify     notify.Notifier
	PublicURL  string // the base of the click target of the "callbacks ready" notification
	BacklogMax int    // MM_CALLBACK_BACKLOG_MAX: the agent backlog gate; 0 switches it off
	Log        *slog.Logger
}

// Service owns the callbacks and callback_verdicts tables. Comment rows and states are written through judgments' exported
// ...Tx functions inside the service's own transactions.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{o: o}
}

func (s *Service) configured() bool { return s != nil && s.o.Store != nil && s.o.NewID != nil }

func (s *Service) unconfigured() error {
	if s.configured() {
		return nil
	}
	return errors.ErrUnsupported
}
