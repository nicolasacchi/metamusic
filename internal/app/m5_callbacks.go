package app

import "github.com/nicolasacchi/metamusic/internal/callbacks"

// buildM5Callbacks constructs the callbacks service and points the late take hook (the ingest bridge's TakeHook) and the late
// backlog gate (generation's BacklogGate) at it (lane M5-C1 owns this file).
func buildM5Callbacks(in m5Inputs, s *m5Services) {
	svc := callbacks.New(callbacks.Options{Store: in.st, Now: in.now, NewID: in.newID, Notify: in.notifier,
		PublicURL: in.cfg.PublicURL, BacklogMax: in.cfg.CallbackBacklogMax})
	s.Callbacks = svc
	in.late.hook.target = svc
	in.late.backlog.target = svc
}
