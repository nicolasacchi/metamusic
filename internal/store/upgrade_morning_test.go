package store_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// morningMigration is the position of 0026_morning_crate.sql (M6N-C3, C382); renumber here and rename the file if main moved.
const morningMigration = 26

var morningTables = []string{"morning_crate_settings", "morning_crates", "morning_crate_items", "morning_crate_sides", "morning_crate_events"}

// TestMigration_MorningCrate: a database one step before the migration upgrades additively (five new tables, the seeded settings row,
// nothing else touched) and every index and trigger exists.
func TestMigration_MorningCrate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, morningMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	for _, tb := range morningTables {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tb); n != "0" {
			t.Fatalf("the previous schema already has %s", tb)
		}
	}
	if err := store.MigrateTo(ctx, prev.W, morningMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != morningMigration {
		t.Fatalf("user_version = %d", v)
	}
	for _, name := range append([]string{
		"morning_crates_created", "morning_crate_items_track", "morning_crate_sides_sha", "morning_crate_events_crate", "morning_crate_events_unmapped",
		"morning_crate_settings_no_delete", "morning_crates_no_delete", "morning_crates_frozen", "morning_crates_transition", "morning_crates_summary_final",
		"morning_crates_reveal_once", "morning_crates_playlist_once", "morning_crate_items_immutable", "morning_crate_items_no_delete",
		"morning_crate_sides_frozen", "morning_crate_sides_removed_once", "morning_crate_sides_no_delete", "morning_crate_events_frozen",
		"morning_crate_events_map_once", "morning_crate_events_no_delete"}, morningTables...) {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n != "1" {
			t.Errorf("%s missing", name)
		}
	}
	if got := scalar(t, prev.R, `SELECT enabled||'|'||at||'|'||minutes||'|'||side_minutes||'|'||max_takes||'|'||anchors||'|'||include_truncated||'|'||keep_days||'|'||wait_minutes||'|'||updated_by
		FROM morning_crate_settings`); got != "1|07:00|40|15|14|1|0|7|45|system:migration" {
		t.Errorf("seed = %s", got)
	}
	after := dbSnapshot(t, prev.R)
	for _, tb := range morningTables {
		delete(after, tb)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
}

// TestMigration_MorningCrateConstraints pins the CHECKs and triggers: bounds, states, set-once reveal and playlist, immutable items,
// one mapping per event, and no physical delete.
func TestMigration_MorningCrateConstraints(t *testing.T) {
	t.Parallel()
	st, _ := openAt(t, morningMigration)
	librarytest.FixtureM5(t, st, librarytest.FixedNow)
	var track string
	if err := st.R.QueryRow(`SELECT id FROM tracks LIMIT 1`).Scan(&track); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error { _, err := st.W.Exec(q, args...); return err }
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	refuse := func(name, want, q string, args ...any) {
		t.Helper()
		err := exec(q, args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, want)
		}
	}
	const crate = `INSERT INTO morning_crates(id,schedule_key,trigger,state,seed,options_json,created_by,created_at,built_at,error_code) VALUES(?,?,?,?,1,'{}','human:alice','2026-11-22T07:00:00.000Z',?,?)`

	// settings: bounds, one row, no delete
	for name, set := range map[string]string{"minutes 4": "minutes = 4", "minutes 121": "minutes = 121", "side 16": "side_minutes = 16", "takes 0": "max_takes = 0",
		"takes 31": "max_takes = 31", "anchors 3": "anchors = 3", "keep 0": "keep_days = 0", "keep 31": "keep_days = 31", "wait 121": "wait_minutes = 121",
		"at junk": "at = 'seven'", "enabled 2": "enabled = 2"} {
		refuse("settings "+name, "constraint", `UPDATE morning_crate_settings SET `+set)
	}
	refuse("second settings row", "constraint", `INSERT INTO morning_crate_settings(id,enabled,at,minutes,side_minutes,max_takes,anchors,include_truncated,keep_days,wait_minutes,updated_by,updated_at) VALUES(2,1,'07:00',40,15,14,1,0,7,45,'x','2026-11-22T00:00:00.000Z')`)
	refuse("settings delete", "no physical delete", `DELETE FROM morning_crate_settings`)

	// crates
	refuse("bad schedule key", "constraint", crate, "c0", "morning:today", "scheduled", "building", nil, nil)
	refuse("bad trigger", "constraint", crate, "c0", nil, "robot", "building", nil, nil)
	refuse("building with built_at", "constraint", crate, "c0", nil, "manual", "building", "2026-11-22T07:01:00.000Z", nil)
	refuse("failed without code", "constraint", crate, "c0", nil, "manual", "failed", "2026-11-22T07:01:00.000Z", nil)
	refuse("ready with code", "constraint", crate, "c0", nil, "manual", "ready", "2026-11-22T07:01:00.000Z", "X")
	must(crate, "c1", "morning:2026-11-22", "scheduled", "building", nil, nil)
	refuse("second crate for the day", "UNIQUE", crate, "c2", "morning:2026-11-22", "scheduled", "building", nil, nil)
	must(crate, "m1", nil, "manual", "building", nil, nil)
	must(crate, "m2", nil, "manual", "building", nil, nil) // manual crates carry no key and may repeat
	refuse("frozen seed", "frozen column", `UPDATE morning_crates SET seed = 2 WHERE id = 'c1'`)
	refuse("frozen key", "frozen column", `UPDATE morning_crates SET schedule_key = 'morning:2026-11-23' WHERE id = 'c1'`)
	refuse("revealed while building", "constraint", `UPDATE morning_crates SET revealed_at = '2026-11-22T08:00:00.000Z', revealed_by = 'human:alice' WHERE id = 'c1'`)
	refuse("playlist before reveal", "constraint", `UPDATE morning_crates SET playlist_id = 'p' WHERE id = 'c1'`)
	must(`UPDATE morning_crates SET state = 'ready', built_at = '2026-11-22T07:05:00.000Z', takes = 1 WHERE id = 'c1'`)
	refuse("state moves again", "keeps its state", `UPDATE morning_crates SET state = 'empty' WHERE id = 'c1'`)
	refuse("summary changes", "keeps its summary", `UPDATE morning_crates SET takes = 5 WHERE id = 'c1'`)
	refuse("revealed_by without at", "constraint", `UPDATE morning_crates SET revealed_by = 'human:alice' WHERE id = 'c1'`)
	must(`UPDATE morning_crates SET state = 'empty', built_at = '2026-11-22T07:05:00.000Z' WHERE id = 'm1'`)
	refuse("empty crate revealed", "constraint", `UPDATE morning_crates SET revealed_at = '2026-11-22T08:00:00.000Z', revealed_by = 'human:alice' WHERE id = 'm1'`)
	must(`UPDATE morning_crates SET state = 'failed', built_at = '2026-11-22T07:05:00.000Z', error_code = 'TOOL_FAILED' WHERE id = 'm2'`)

	// reveal once, playlist once
	must(`UPDATE morning_crates SET revealed_at = '2026-11-22T08:00:00.000Z', revealed_by = 'human:alice' WHERE id = 'c1'`)
	refuse("revealed twice", "already revealed", `UPDATE morning_crates SET revealed_at = '2026-11-22T09:00:00.000Z', revealed_by = 'human:bob' WHERE id = 'c1'`)
	must(`INSERT INTO playlists(id,name,created_by,created_at,updated_by,updated_at) VALUES('pl1','Morning crate 2026-11-22','system:morning','2026-11-22T08:00:00.000Z','system:morning','2026-11-22T08:00:00.000Z')`)
	must(`UPDATE morning_crates SET playlist_id = 'pl1' WHERE id = 'c1'`)
	refuse("playlist twice", "playlist already set", `UPDATE morning_crates SET playlist_id = NULL WHERE id = 'c1'`)
	refuse("crate delete", "no physical delete", `DELETE FROM morning_crates WHERE id = 'c1'`)

	// items
	item := `INSERT INTO morning_crate_items(crate_id,n,side_index,track_id,anchor,source,start_frame,audio_start_frame,end_frame,gain_cdb,limited) VALUES(?,?,0,?,0,?,?,?,?,0,0)`
	refuse("item of no crate", "FOREIGN KEY", item, "nope", 1, track, "job", 0, 10, 100)
	refuse("item bad source", "constraint", item, "c1", 1, track, "robot", 0, 10, 100)
	refuse("item empty audio", "constraint", item, "c1", 1, track, "job", 0, 100, 100)
	must(item, "c1", 1, track, "job", 0, 10, 100)
	refuse("same take twice in a crate", "UNIQUE", item, "c1", 2, track, "job", 100, 110, 200)
	refuse("item update", "immutable", `UPDATE morning_crate_items SET gain_cdb = 5`)
	refuse("item delete", "no physical delete", `DELETE FROM morning_crate_items`)

	// sides
	side := `INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES('c1',?,?,?,10,100)`
	sha := strings.Repeat("a", 64)
	refuse("side outside derived/morning", "constraint", side, 0, sha, "masters/x.flac")
	refuse("short sha", "constraint", side, 0, "abc", "derived/morning/ab/abc.flac")
	must(side, 0, sha, "derived/morning/aa/"+sha+".flac")
	refuse("side frozen", "frozen column", `UPDATE morning_crate_sides SET bytes = 1`)
	must(`UPDATE morning_crate_sides SET removed_at = '2026-11-30T00:00:00.000Z'`)
	refuse("removal final", "removal is final", `UPDATE morning_crate_sides SET removed_at = NULL`)
	refuse("side delete", "no physical delete", `DELETE FROM morning_crate_sides`)

	// events
	ev := `INSERT INTO morning_crate_events(id,crate_id,side_index,chapter,kind,stars,side_ms,latency_ms,client_at,received_at,received_by) VALUES(?,'c1',0,1,?,?,1000,?,'2026-11-22T08:00:00.000Z','2026-11-22T08:00:01.000Z','human:alice')`
	refuse("bad kind", "constraint", ev, "e0", "tap", nil, 0)
	refuse("stars on a marker", "constraint", ev, "e0", "marker", 3, 0)
	refuse("stars 6", "constraint", ev, "e0", "rate", 6, 0)
	refuse("latency 5001", "constraint", ev, "e0", "marker", nil, 5001)
	must(ev, "e1", "marker", nil, 1200)
	must(ev, "e2", "rate", 4, 0)
	must(ev, "e3", "rate", nil, 0) // clears the rating
	must(ev, "e4", "skip", nil, 0)
	refuse("duplicate id", "UNIQUE", ev, "e1", "marker", nil, 0)
	refuse("target before mapping", "constraint", `UPDATE morning_crate_events SET comment_id = 'cm' WHERE id = 'e1'`)
	refuse("superseded before mapping", "constraint", `UPDATE morning_crate_events SET superseded = 1 WHERE id = 'e2'`)
	refuse("rating target on a marker", "constraint", `UPDATE morning_crate_events SET rating_track_id = 't', mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e1'`)
	refuse("comment target on a rate", "constraint", `UPDATE morning_crate_events SET comment_id = 'cm', mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e2'`)
	refuse("listen target on a marker", "constraint", `UPDATE morning_crate_events SET listen_event_id = 'l', mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e1'`)
	refuse("superseded with a target", "constraint", `UPDATE morning_crate_events SET rating_track_id = 't', superseded = 1, mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e2'`)
	refuse("event frozen", "frozen column", `UPDATE morning_crate_events SET side_ms = 5 WHERE id = 'e1'`)
	must(`UPDATE morning_crate_events SET comment_id = 'cm1', mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e1'`)
	refuse("a second mapping aborts", "already mapped", `UPDATE morning_crate_events SET comment_id = 'cm2' WHERE id = 'e1'`)
	must(`UPDATE morning_crate_events SET superseded = 1, mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e2'`)
	refuse("superseded undone", "already mapped", `UPDATE morning_crate_events SET superseded = 0 WHERE id = 'e2'`)
	must(`UPDATE morning_crate_events SET listen_event_id = 'e4', mapped_at = '2026-11-22T09:00:00.000Z' WHERE id = 'e4'`)
	refuse("event delete", "no physical delete", `DELETE FROM morning_crate_events`)
}
