package servertest

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TapeRunner drives the tape runner of a rig by hand: the rig builds the runner and leaves it idle unless Options.Sync is set.
type TapeRunner struct{ svc *tape.Service }

// RunOne claims and renders one queued tape and reports whether there was one; it fails the test on an error.
func (r TapeRunner) RunOne(tb testing.TB) bool {
	tb.Helper()
	ran, err := r.svc.RunOne(context.Background())
	if err != nil {
		tb.Fatalf("servertest: tape RunOne: %v", err)
	}
	return ran
}
