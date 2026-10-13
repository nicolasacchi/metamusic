package librarytest

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Fixture builds the standard test library: FixtureV2 (the M1 rows) plus the
// M2a rows of fixture_m2a.go (ratings, tags, comments, a playlist, drafts of
// the new origins, listen events, a paired device and a backup run). It is a
// valid schema-version-3/4 library; the M2c rows are in FixtureM2c.
func Fixture(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	fx := FixtureV2(tb, st, now)
	return addM2a(tb, st, now, fx)
}

// FixtureM2c is Fixture plus the two M2c rows of fixture_m2c.go (schema version
// 5): a reference FLAC (TrackRefFlac) and a lossy reference (TrackLossy). It is
// separate so the many suites that count Fixture's rows stay valid; API,
// trackctx, export and playlist tests of M2c use it.
func FixtureM2c(tb testing.TB, st *store.Store, now time.Time) Fixtures {
	tb.Helper()
	return addM2c(tb, st, now, Fixture(tb, st, now))
}
