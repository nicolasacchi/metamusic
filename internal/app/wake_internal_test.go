package app

import "testing"

// TestWake_NilSafeUntilRunnerExists pins that the injected Wake is callable before (and without) a runner,
// as in mcp-stdio, and forwards once one is set.
func TestWake_NilSafeUntilRunnerExists(t *testing.T) {
	t.Parallel()
	var w wakeRef
	w.call() // no runner: nothing happens
	n := 0
	w.set(func() { n++ })
	w.call()
	if n != 1 {
		t.Errorf("wake forwarded %d times, want 1", n)
	}
}
