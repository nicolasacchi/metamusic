package servertest_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestRig_L2Options: L2 adds the FixtureL2 rows on top of L1; the Syncer and the tape runner are built and idle; Rig.Settle drains
// the provenance queue and then the Syncer (a no-op stub until lane R-b) without failing; Rig.Tapes.RunOne finds nothing queued;
// the pipeline behind the rig is the fake and the encoder version a constant, so no rig spawns a process.
func TestRig_L2Options(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	if r.Syncer == nil || r.Services.Syncer == nil || r.Services.Tapes == nil || r.Services.RecordView == nil || r.Services.Crates == nil ||
		r.Services.Curation == nil || r.Services.Albums == nil || r.Services.Records == nil {
		t.Fatal("every L2 service must be built")
	}
	if r.Pipeline == nil {
		t.Fatal("the default rig uses the fake tape pipeline")
	}
	if n := l1Count(t, r, `SELECT COUNT(*) FROM records`); n != 2 {
		t.Errorf("records = %d, want 2 (R1 and R2 of FixtureL2)", n)
	}
	if n := l1Count(t, r, `SELECT COUNT(*) FROM crates`); n != 4 {
		t.Errorf("crates = %d, want 4", n)
	}
	if n, err := r.Syncer.Pending(context.Background()); err != nil || n != 0 {
		t.Errorf("Pending = %d, %v: the idle Syncer has nothing to do before Settle", n, err)
	}
	if r.Tapes.RunOne(t) {
		t.Error("nothing is queued, so RunOne must find nothing")
	}
	r.Settle(t) // provenance first, then the Syncer
	if an, err := r.Services.Records.Verify(context.Background()); err != nil || len(an) != 0 {
		t.Errorf("Verify after Settle: %v %v", an, err)
	}
}

// TestRig_L2OffByDefault: a rig without L2 has no L2 rows but still builds the services (the operations answer 501 rather
// than 503).
func TestRig_L2OffByDefault(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	if r.Services.Records == nil || r.Services.Tapes == nil || r.Services.Curation == nil {
		t.Fatal("the L2 services are always built")
	}
	if n := l1Count(t, r, `SELECT COUNT(*) FROM records`); n != 0 {
		t.Errorf("records = %d, want 0", n)
	}
}
