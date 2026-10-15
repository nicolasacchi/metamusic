package servertest

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// Subsonic creates a Subsonic client through create_subsonic_client as the human and returns a kit client for it
// (C117.1). STUB(M4a-S1): skips the test.
func (r *Rig) Subsonic(tb testing.TB, _ subsonictest.ClientOpts) *subsonictest.Client {
	tb.Helper()
	tb.Skip("M4a-S1: servertest.Rig.Subsonic not implemented yet")
	return nil
}
