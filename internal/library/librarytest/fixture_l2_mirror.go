package librarytest

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// L2MirrorParams shape the acceptance mirror of L2: values deliberately different from the real library (C101.1), 4
// originals and 5 lossy groups, so the Songs a Settle derives from them (4 + 5 + the index Muses' drafts) are plenty for Records.
var L2MirrorParams = L1Params{Muses: 120, Angles: 3, DemoDrafts: 5, Originals: 4, Groups: 5, GroupSize: 3}

// FixtureL2Mirror is FixtureL1Mirror at L2MirrorParams (rows only: no Song exists until a Settle, no L2 row exists until an
// operation writes one). The L2 acceptance mirror settles it, builds Records over the Songs it derives and checks that every
// view agrees.
func FixtureL2Mirror(tb testing.TB, st *store.Store, now time.Time) L1Info {
	tb.Helper()
	return FixtureL1Mirror(tb, st, now, L2MirrorParams)
}

// ApplyL2Mirror adds the L2 mirror to a library that already holds FixtureM3's rows (servertest uses it in place of ApplyL1).
func ApplyL2Mirror(tb testing.TB, st *store.Store, now time.Time) L1Info {
	tb.Helper()
	return buildL1(tb, st, now, L2MirrorParams, Fixtures{})
}
