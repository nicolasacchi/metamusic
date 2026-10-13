package librarytest

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixture builds the standard test library: FixtureV2 (the M1 rows) plus the
// M2a rows of fixture_m2a.go (ratings, tags, comments, a playlist, drafts of
// the new origins, listen events, a paired device and a backup run).
func Fixture(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	fx := FixtureV2(tb, st, now)
	return addM2a(tb, st, now, fx)
}
