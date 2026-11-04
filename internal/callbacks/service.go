// Package callbacks holds Callback Listening (C255, D-M5-4..6): a callback per addressed comment of a Song PR child, the
// human and critic verdicts on them, the comment transitions they cause, and the backlog gate that paces agent spend.
// M5-F2 froze the service type and its seams (jobs.TakeHook, generation.BacklogGate); lane M5-C1 fills it.
package callbacks

// Options builds the service (store, library timing, judgments, notifier, clock, ids); lane M5-C1 fills it.
type Options struct{}

// Service owns the callbacks and callback_verdicts tables.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }
