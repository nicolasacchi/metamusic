package curation_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestStats_Fixture: the counts of FixtureL2 (R1 published, R2 draft; four Crates; no tapes) and the review queue of C130.5:
// R1's floating slots on a newest Pick (A and B) are undecided; the pinned, the Song-pinned and the empty slot are not.
func TestStats_Fixture(t *testing.T) {
	t.Parallel()
	st := open(t)
	info := librarytest.FixtureL2(t, st, librarytest.FixedNow)
	s := curation.New(st, nil)
	ctx := context.Background()

	got, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := curation.Stats{
		Records: curation.RecordStats{Total: 2, Draft: 1, Published: 1},
		Crates:  curation.CrateStats{Manual: 2, Smart: 2},
		Review:  curation.ReviewStats{Undecided: 2},
	}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("Stats (-want +got):\n%s", d)
	}

	// the opt-ins are repeated, so the operator sees what is on
	if _, err := st.W.Exec(`INSERT INTO settings(key, value) VALUES (?, 'true')`, curation.KeySubsonic); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Stats(ctx)
	if !got.OptIns.SubsonicMuseNames || got.OptIns.ExportMuseNames {
		t.Errorf("opt_ins = %+v", got.OptIns)
	}

	// a discarded pinned Take is a pin_ineligible slot of a published Record; a draft Record's slots never count
	if _, err := st.W.Exec(`UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id = ?`, info.Takes["c2"]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Stats(ctx)
	if got.Review.PinIneligible != 1 || got.Review.Undecided != 2 {
		t.Errorf("review = %+v", got.Review)
	}

	// tapes by state, and a hidden Record stays in the totals
	for i, state := range []string{"queued", "failed", "failed"} {
		insertTape(t, st, info.Records["R1"], state, i)
	}
	if _, err := st.W.Exec(`UPDATE records SET hidden_at = ?, hidden_by = 'human:x' WHERE id = ?`, "2026-10-28T10:00:00.000Z", info.Records["R2"]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Stats(ctx)
	if got.Tapes != (curation.TapeStats{Queued: 1, Failed: 2}) || got.Records.Total != 2 {
		t.Errorf("tapes %+v records %+v", got.Tapes, got.Records)
	}
}

// TestStats_Pending: slots whose Song has Pick events the slot has not consumed; a pinned slot never counts (the Syncer leaves
// its synced_seq alone) and neither does a removed one.
func TestStats_Pending(t *testing.T) {
	t.Parallel()
	st := open(t)
	info := librarytest.FixtureL2(t, st, librarytest.FixedNow)
	s := curation.New(st, nil)
	ctx := context.Background()
	base, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// make slot A (floating, S2) behind by one event (the monotonic guard is dropped: the Syncer never moves backwards)
	if _, err := st.W.Exec(`DROP TRIGGER record_slots_synced_monotonic`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE record_slots SET synced_seq = 0 WHERE id = ?`, info.Slots["A"]); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Stats(ctx)
	if got.Pending != base.Pending+1 {
		t.Errorf("pending %d -> %d, want +1", base.Pending, got.Pending)
	}
	// the same on the pinned slot C changes nothing
	if _, err := st.W.Exec(`UPDATE record_slots SET synced_seq = 0 WHERE id = ?`, info.Slots["C"]); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Stats(ctx); again.Pending != got.Pending {
		t.Errorf("a pinned slot counted: %d -> %d", got.Pending, again.Pending)
	}
}

// insertTape adds a tape row in the given state; failed rows carry a code, keys are unique per n.
func insertTape(t *testing.T, st *store.Store, recordID, state string, n int) {
	t.Helper()
	key := fmt.Sprintf("%064x", n+1)
	code := any(nil)
	if state == "failed" {
		code = "TAPE_FAILED"
	}
	_, err := st.W.Exec(`INSERT INTO tapes(id, record_id, record_revision, key, state, params_json, plan_json, contains_lossy, lossy_sources, error_code, principal_id, created_at)
		VALUES(?, ?, 1, ?, 'queued', '{}', '{}', 0, 0, NULL, 'human:x', '2026-10-28T10:00:00.000Z')`, fmt.Sprintf("tape-%d", n), recordID, key)
	if err != nil {
		t.Fatal(err)
	}
	if state == "failed" {
		if _, err := st.W.Exec(`UPDATE tapes SET state = 'failed', error_code = ?, finished_at = '2026-10-28T10:01:00.000Z' WHERE id = ?`, code, fmt.Sprintf("tape-%d", n)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStats_BrokenSchemaSurfaces: each of the three reads fails loudly when its table is missing.
func TestStats_BrokenSchemaSurfaces(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"tapes", "song_picks", "settings"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			st := open(t)
			if _, err := st.W.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.W.Exec(`DROP TABLE ` + table); err != nil {
				t.Skipf("cannot drop %s here: %v", table, err)
			}
			if _, err := curation.New(st, nil).Stats(context.Background()); err == nil {
				t.Errorf("Stats without %s must fail", table)
			}
		})
	}
}
