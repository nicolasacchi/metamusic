package records_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

func dropTrigger(t *testing.T, r *rig, names ...string) {
	t.Helper()
	for _, n := range names {
		r.exec(`DROP TRIGGER ` + n)
	}
}

func hasAnomaly(an []records.Anomaly, code, severity string) bool {
	for _, a := range an {
		if a.Code == code && a.Severity == severity {
			return true
		}
	}
	return false
}

func TestVerify_FixtureIsClean(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.wantClean()
	h := r.tableHash()
	if _, err := r.svc.Verify(context.Background()); err != nil || r.tableHash() != h {
		t.Error("Verify wrote")
	}
}

func TestVerify_Constants(t *testing.T) {
	t.Parallel()
	if records.TapeLeaseTTLSeconds != int(tape.LeaseTTL.Seconds()) {
		t.Errorf("lease TTL %d s differs from tape.LeaseTTL", records.TapeLeaseTTLSeconds)
	}
	got, want := records.CurationKeys(), curation.Keys
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("curation keys %v differ from curation.Keys %v", got, want)
	}
}

// TestVerify_L2_DetectsEachAnomaly plants one fault per invariant in a scratch database; faults the triggers forbid are
// planted after dropping the trigger.
func TestVerify_L2_DetectsEachAnomaly(t *testing.T) {
	t.Parallel()
	plantTape := func(r *rig, id, key, state string, lease any) {
		owner := any(nil)
		if state == "running" {
			owner = "runner"
		}
		r.exec(`INSERT INTO tapes(id,record_id,record_revision,key,state,params_json,plan_json,contains_lossy,lossy_sources,owner,lease_expires_at,principal_id,created_at)
			VALUES(?,?,1,?,?,'{}','{}',0,0,?,?,'human:op','2026-10-06T00:00:00.000Z')`, id, r.fx.Records["R1"], key, state, owner, lease)
	}
	key := func(c byte) string { return string(make([]byte, 0)) + fmt.Sprintf("%064x", int(c)) }
	for _, c := range []struct {
		name     string
		plant    func(r *rig)
		code     string
		severity string
	}{
		{"R1 order", func(r *rig) {
			dropTrigger(t, r, "records_order_consistent")
			r.exec(`UPDATE records SET order_json = '[]' WHERE id = ?`, r.fx.Records["R2"])
		}, records.CodeOrderMismatch, "error"},
		{"R1 twice", func(r *rig) {
			r.exec(`DROP INDEX record_slots_live_song`)
			r.exec(`INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES('dup-slot',?,?,'human:op','2026-10-06T00:00:00.000Z')`, r.fx.Records["R2"], r.song("S1"))
		}, records.CodeDuplicateSongSlot, "error"},
		{"R2 missing song", func(r *rig) {
			r.exec(`PRAGMA foreign_keys = OFF`)
			r.exec(`INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES('ghost-slot',?,'no-such-song','human:op','2026-10-06T00:00:00.000Z')`, r.fx.Records["R2"])
		}, records.CodeSlotSongMissing, "error"},
		{"R2 retired", func(r *rig) { retire(r, r.song("S7"), r.song("S1")) }, records.CodeRetiredSong, "warn"},
		{"R3 pin", func(r *rig) {
			dropTrigger(t, r, "record_slots_pin_in_song_upd")
			r.exec(`UPDATE record_slots SET pinned_take_id = ?, pinned_by = 'human:op', pinned_at = '2026-10-06T00:00:00.000Z' WHERE id = ?`, r.take("a1"), r.fx.Slots["R2.1"])
		}, records.CodePinNotInSong, "error"},
		{"R4 chain", func(r *rig) {
			dropTrigger(t, r, "record_changes_no_delete")
			r.exec(`DELETE FROM record_changes WHERE record_id = ? AND seq = 2`, r.fx.Records["R2"])
		}, records.CodeChangesChain, "error"},
		{"R4 first row", func(r *rig) {
			dropTrigger(t, r, "record_changes_immutable")
			r.exec(`UPDATE record_changes SET kind = 'renamed' WHERE record_id = ? AND seq = 1`, r.fx.Records["R2"])
		}, records.CodeChangesNoCreated, "error"},
		{"R5 chain", func(r *rig) {
			dropTrigger(t, r, "record_changes_immutable")
			r.exec(`UPDATE record_changes SET from_take_id = ? WHERE slot_id = ? AND kind = 'slot_pinned'`, r.take("a1"), r.fx.Slots["C"])
		}, records.CodeReplayMismatch, "error"},
		{"R5 state", func(r *rig) {
			r.exec(`UPDATE record_slots SET synced_seq = (SELECT MAX(seq) FROM pick_events WHERE song_id = ?) WHERE id = ?`, r.song("S3"), r.fx.Slots["B"])
			r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
			r.exec(`UPDATE record_slots SET synced_seq = (SELECT MAX(seq) FROM pick_events WHERE song_id = ?) WHERE id = ?`, r.song("S3"), r.fx.Slots["B"])
		}, records.CodeReplayMismatch, "error"},
		{"R6 seed", func(r *rig) {
			r.exec(`UPDATE records SET cover_seed = cover_seed + 1 WHERE id = ?`, r.fx.Records["R2"])
		}, records.CodeCoverSeed, "error"},
		{"R6 first published", func(r *rig) {
			r.exec(`INSERT INTO record_changes(id,record_id,seq,at,kind,actor,actor_kind,actor_name) SELECT 'pub-row', ?, MAX(seq) + 1, '2026-10-06T00:00:00.000Z', 'published', 'human:op', 'human', 'op' FROM record_changes WHERE record_id = ?`, r.fx.Records["R2"], r.fx.Records["R2"])
		}, records.CodeFirstPublished, "error"},
		{"R6 empty", func(r *rig) {
			rec, err := r.svc.Create(context.Background(), human, records.NewRecord{Title: "Synthetic Empty"})
			if err != nil {
				t.Fatal(err)
			}
			r.exec(`UPDATE records SET status = 'published', published_by = 'human:op', published_at = '2026-10-06T00:00:00.000Z', first_published_at = '2026-10-06T00:00:00.000Z' WHERE id = ?`, rec.ID)
		}, records.CodePublishedEmpty, "warn"},
		{"R7 item", func(r *rig) {
			dropTrigger(t, r, "crate_items_subject")
			r.exec(`INSERT INTO crate_items(id,crate_id,song_id,added_by,added_at) VALUES('bad-item',?,?,'human:op','2026-10-06T00:00:00.000Z')`, r.fx.Crates["manual_take"], r.song("S1"))
		}, records.CodeCrateItem, "error"},
		{"R8 key", func(r *rig) {
			r.exec(`DROP INDEX tapes_live_key`)
			plantTape(r, "t1", key(1), "queued", nil)
			plantTape(r, "t2", key(1), "queued", nil)
		}, records.CodeTapeKey, "error"},
		{"R8 lease", func(r *rig) { plantTape(r, "t3", key(2), "running", "2020-01-01T00:00:00.000Z") }, records.CodeTapeLease, "error"},
		{"R9 key", func(r *rig) { r.exec(`INSERT INTO settings(key,value) VALUES('curation.bogus','true')`) }, records.CodeSettingKey, "error"},
		{"R9 value", func(r *rig) { r.exec(`INSERT INTO settings(key,value) VALUES('curation.export_muse_names','maybe')`) }, records.CodeSettingValue, "error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			c.plant(r)
			an, err := r.svc.Verify(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !hasAnomaly(an, c.code, c.severity) {
				t.Errorf("no %s %s in %+v", c.severity, c.code, an)
			}
			for _, a := range an {
				if a.Subject == "" || a.Detail == "" {
					t.Errorf("anomaly without subject or detail: %+v", a)
				}
			}
		})
	}
}

// TestVerify_CrateFilterInvalid is not parallel: the validator is process-wide (the composition root installs it once).
func TestVerify_CrateFilterInvalid(t *testing.T) {
	r := newRig(t)
	records.SetCrateFilterCheck(func(subject string, _ json.RawMessage) error { return errors.New("synthetic: unknown key") })
	t.Cleanup(func() { records.SetCrateFilterCheck(nil) })
	an, err := r.svc.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasAnomaly(an, records.CodeCrateFilterInvalid, "warn") {
		t.Errorf("no crate_filter_invalid in %+v", an)
	}
	for _, a := range an {
		if a.Severity == "error" {
			t.Errorf("an invalid stored filter must warn only: %+v", a)
		}
	}
}
