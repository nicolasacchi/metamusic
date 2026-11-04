package app

import "github.com/nicolasacchi/metamusic/internal/callbacks"

// buildM5Callbacks constructs the callbacks service (lane M5-C1 owns this file and points in.late.hook.target and
// in.late.backlog.target at it).
func buildM5Callbacks(in m5Inputs, s *m5Services) {
	s.Callbacks = callbacks.New(callbacks.Options{})
}
