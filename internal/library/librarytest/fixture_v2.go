package librarytest

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixture builds the standard test library. From M2a-B4b on it also seeds the
// M2a tables; until then it is exactly FixtureV2.
func Fixture(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	return FixtureV2(tb, st, now)
}
