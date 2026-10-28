package store

import (
	"fmt"
	"strings"
	"testing"
)

const (
	l2RecordQ = `INSERT INTO records(id,title,cover_seed,created_by,created_at,updated_by,updated_at) VALUES(?,?,1,'human:h',?,'human:h',?)`
	l2SlotQ   = `INSERT INTO record_slots(id,record_id,song_id,pinned_take_id,pinned_by,pinned_at,synced_seq,added_by,added_at) VALUES(?,?,?,?,?,?,0,'human:h',?)`
	l2ChangeQ = `INSERT INTO record_changes(id,record_id,seq,at,kind,slot_id,from_take_id,to_take_id,reason,pick_event_id,actor,actor_kind,actor_name)
		VALUES(?,?,?,?,?,?,?,?,?,?,'human:h','human','h')`
	l2CrateQ = `INSERT INTO crates(id,name,kind,subject,filter_json,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,?,?,'human:h',?,'human:h',?)`
	l2ItemQ  = `INSERT INTO crate_items(id,crate_id,song_id,track_id,added_by,added_at) VALUES(?,?,?,?,'human:h',?)`
	l2TapeQ  = `INSERT INTO tapes(id,record_id,record_revision,key,state,params_json,plan_json,contains_lossy,lossy_sources,error_code,principal_id,created_at)
		VALUES(?,?,1,?,?,'{}','{}',?,?,?,'human:h',?)`
)

func tapeKey(c byte) string { return strings.Repeat(string(c), 64) }

func l2Record(t *testing.T, s *Store, id string) {
	t.Helper()
	mustExec(t, s, l2RecordQ, id, "Synthetic Record "+id, l1ts, l1ts)
}

func l2Slot(t *testing.T, s *Store, id, rec, song string, pin any) error {
	t.Helper()
	var by, at any
	if pin != nil {
		by, at = "human:h", l1ts
	}
	return exec(t, s, l2SlotQ, id, rec, song, pin, by, at, l1ts)
}

func l2Change(t *testing.T, s *Store, id, rec string, seq int, kind string, slot, from, to, reason, event any) error {
	t.Helper()
	return exec(t, s, l2ChangeQ, id, rec, seq, l1ts, kind, slot, from, to, reason, event)
}

func l2Order(t *testing.T, s *Store, rec string, ids ...string) error {
	t.Helper()
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = fmt.Sprintf("%q", id)
	}
	return exec(t, s, `UPDATE records SET order_json = ? WHERE id = ?`, "["+strings.Join(q, ",")+"]", rec)
}

// TestTriggers_L2 is the C119 trigger table over the L1 world (songs s1 with takes t1/t1c and a pick event e1, s2 with t2).
func TestTriggers_L2(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)

	// ---- records ----
	l2Record(t, s, "r1")
	l2Record(t, s, "r2")
	expectErr(t, exec(t, s, `UPDATE records SET created_at = 'x' WHERE id = 'r1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE records SET created_by = 'x' WHERE id = 'r1'`), "frozen column")
	// draft -> published is legal (published_* and first_published_at set together); published -> proposed is not
	mustExec(t, s, `UPDATE records SET status='published', published_by='human:h', published_at=?, first_published_at=? WHERE id='r2'`, l1ts, l1ts)
	expectErr(t, exec(t, s, `UPDATE records SET status='proposed' WHERE id='r2'`), "illegal status transition")
	expectErr(t, exec(t, s, `UPDATE records SET first_published_at='2027-01-01T00:00:00.000Z' WHERE id='r2'`), "first_published_at is set once")
	// published -> draft keeps first_published_at, clears published_*
	mustExec(t, s, `UPDATE records SET status='draft', published_by=NULL, published_at=NULL WHERE id='r2'`)
	// draft -> proposed -> published and proposed -> draft
	mustExec(t, s, `UPDATE records SET status='proposed', proposed_by='agent:a', proposed_at=?, proposal_note='why' WHERE id='r1'`, l1ts)
	mustExec(t, s, `UPDATE records SET status='draft', proposed_by=NULL, proposed_at=NULL, proposal_note=NULL WHERE id='r1'`)
	// CHECKs
	expectErr(t, exec(t, s, `UPDATE records SET status='published' WHERE id='r1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET proposed_by='agent:a', proposed_at=? WHERE id='r1'`, l1ts), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET proposal_note='x' WHERE id='r1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET title='' WHERE id='r1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET cover_seed=9007199254740992 WHERE id='r1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET order_json='{}' WHERE id='r1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET hidden_at=? WHERE id='r1'`, l1ts), "CHECK")
	expectErr(t, exec(t, s, `UPDATE records SET status='published', published_by='human:h', published_at=? WHERE id='r1'`, l1ts), "CHECK") // no first_published_at

	// ---- record_slots ----
	if err := l2Slot(t, s, "sl1", "r1", "s1", nil); err != nil {
		t.Fatal(err)
	}
	expectErr(t, l2Slot(t, s, "sl1b", "r1", "s1", nil), "UNIQUE")
	if err := l2Slot(t, s, "sl2", "r1", "s2", nil); err != nil {
		t.Fatal(err)
	}
	expectErr(t, l2Slot(t, s, "sl3", "r1", "s2", "t1"), "not in the song") // pinned take of another Song
	expectErr(t, exec(t, s, `UPDATE record_slots SET pinned_take_id='t2', pinned_by='human:h', pinned_at=? WHERE id='sl1'`, l1ts), "not in the song")
	mustExec(t, s, `UPDATE record_slots SET pinned_take_id='t1c', pinned_by='human:h', pinned_at=? WHERE id='sl1'`, l1ts)
	expectErr(t, exec(t, s, `UPDATE record_slots SET pinned_take_id=NULL WHERE id='sl1'`), "CHECK") // the pin columns go together
	mustExec(t, s, `UPDATE record_slots SET pinned_take_id=NULL, pinned_by=NULL, pinned_at=NULL WHERE id='sl1'`)
	expectErr(t, exec(t, s, `UPDATE record_slots SET record_id='r2' WHERE id='sl1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE record_slots SET added_at='x' WHERE id='sl1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE record_slots SET side='E' WHERE id='sl1'`), "CHECK")
	mustExec(t, s, `UPDATE record_slots SET side='A' WHERE id='sl1'`)
	mustExec(t, s, `UPDATE record_slots SET synced_seq = 3 WHERE id='sl1'`)
	expectErr(t, exec(t, s, `UPDATE record_slots SET synced_seq = 2 WHERE id='sl1'`), "synced_seq never decreases")
	// order_consistent: exactly the live slots, each once
	expectErr(t, l2Order(t, s, "r1", "sl1"), "order must list exactly the live slots")               // missing
	expectErr(t, l2Order(t, s, "r1", "sl1", "sl2", "slx"), "order must list exactly the live slots") // extra
	expectErr(t, l2Order(t, s, "r1", "sl1", "sl1"), "order must list exactly the live slots")        // duplicate
	expectErr(t, l2Order(t, s, "r1", "sl1", "sl9"), "order must list exactly the live slots")        // unknown
	if err := l2Order(t, s, "r1", "sl2", "sl1"); err != nil {
		t.Fatal(err)
	}
	// remove-then-add: the removed slot frees the live (record, song) pair; the removed row is immutable
	mustExec(t, s, `UPDATE record_slots SET removed_by='human:h', removed_at=? WHERE id='sl2'`, l1ts)
	expectErr(t, l2Order(t, s, "r1", "sl1", "sl2"), "order must list exactly the live slots") // a removed slot cannot be listed
	if err := l2Order(t, s, "r1", "sl1"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE record_slots SET side='B' WHERE id='sl2'`), "immutable")
	if err := l2Slot(t, s, "sl2b", "r1", "s2", nil); err != nil {
		t.Fatalf("remove-then-add must be ok: %v", err)
	}
	if err := l2Order(t, s, "r1", "sl1", "sl2b"); err != nil {
		t.Fatal(err)
	}
	// a retired Song cannot be slotted; a slot follows retired_into only (and a follow may restart synced_seq)
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('dd1','import_backlog','l2_v1','{"id":"l2_v1"}',?,?)`, ts, ts)
	if err := l1Song(s, t, "sd1", nil, "dd1"); err != nil {
		t.Fatal(err)
	}
	l2Record(t, s, "r3")
	if err := l2Slot(t, s, "sl5", "r3", "sd1", nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE record_slots SET synced_seq = 5 WHERE id='sl5'`)
	mustExec(t, s, `UPDATE songs SET retired_at=?, retired_into='s1' WHERE id='sd1'`, l1ts)
	expectErr(t, l2Slot(t, s, "sl6", "r3", "sd1", nil), "song is retired")
	expectErr(t, exec(t, s, `UPDATE record_slots SET song_id='s2' WHERE id='sl5'`), "follows retired_into only")
	expectErr(t, exec(t, s, `UPDATE record_slots SET song_id='s2', synced_seq=0 WHERE id='sl5'`), "follows retired_into only")
	expectErr(t, exec(t, s, `UPDATE record_slots SET synced_seq=1 WHERE id='sl5'`), "synced_seq never decreases")
	mustExec(t, s, `UPDATE record_slots SET song_id='s1', synced_seq=0 WHERE id='sl5'`)
	mustExec(t, s, `UPDATE records SET order_json='["sl5"]' WHERE id='r3'`)

	// ---- record_changes ----
	for _, c := range []struct {
		name                          string
		id                            string
		rec                           string
		seq                           int
		kind                          string
		slot, from, to, reason, event any
		want                          string
	}{
		{"created", "c1", "r1", 1, "created", nil, nil, nil, "initial", nil, ""},
		{"gap in the chain", "c2", "r1", 3, "renamed", nil, nil, nil, nil, nil, "broken chain"},
		{"repeat of seq 1", "c2", "r1", 1, "renamed", nil, nil, nil, nil, nil, "broken chain"},
		{"slot kind without a slot", "c2", "r1", 2, "slot_added", nil, nil, "t1c", nil, nil, "CHECK"},
		{"non-slot kind with a slot", "c2", "r1", 2, "renamed", "sl1", nil, nil, nil, nil, "CHECK"},
		{"slot of another record", "c2", "r2", 1, "slot_added", "sl1", nil, "t1c", nil, nil, "slot is not in the record"},
		{"unknown kind", "c2", "r1", 2, "slot_exploded", "sl1", nil, nil, nil, nil, "CHECK"},
		{"unknown reason", "c2", "r1", 2, "slot_added", "sl1", nil, "t1c", "whim", nil, "CHECK"},
		{"upgrade to the same take", "c2", "r1", 2, "slot_upgraded", "sl1", "t1", "t1", "ladder", "e1", "CHECK"},
		{"upgrade without a from", "c2", "r1", 2, "slot_upgraded", "sl1", nil, "t1", "ladder", "e1", "CHECK"},
		{"filled with a from", "c2", "r1", 2, "slot_filled", "sl1", "t1", "t1c", "newest", "e1", "CHECK"},
		{"emptied with a to", "c2", "r1", 2, "slot_emptied", "sl1", "t1", "t1c", "emptied", "e1", "CHECK"},
		{"syncer kind without an event", "c2", "r1", 2, "slot_upgraded", "sl1", "t1", "t1c", "ladder", nil, "CHECK"},
		{"filled without an event", "c2", "r1", 2, "slot_filled", "sl1", nil, "t1c", "newest", nil, "CHECK"},
		{"emptied without an event", "c2", "r1", 2, "slot_emptied", "sl1", "t1c", nil, "emptied", nil, "CHECK"},
		{"slot_added with no take", "c2", "r1", 2, "slot_added", "sl1", nil, nil, "initial", nil, ""},
		{"filled by an event", "c3", "r1", 3, "slot_filled", "sl1", nil, "t1c", "newest", "e1", ""},
		{"the same event twice", "c4", "r1", 4, "slot_upgraded", "sl1", "t1", "t1c", "newest", "e1", "UNIQUE"},
		{"another slot, the same event", "c4", "r1", 4, "slot_filled", "sl2b", nil, "t2", "newest", "e1", ""},
		{"unknown event", "c5", "r1", 5, "slot_filled", "sl1", nil, "t1c", "newest", "nope", "FOREIGN KEY"},
	} {
		err := l2Change(t, s, c.id, c.rec, c.seq, c.kind, c.slot, c.from, c.to, c.reason, c.event)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
	expectErr(t, exec(t, s, `UPDATE record_changes SET reason='pin' WHERE id='c1'`), "append-only")
	expectErr(t, exec(t, s, `DELETE FROM record_changes WHERE id='c1'`), "append-only")

	// ---- crates and crate_items ----
	for _, c := range []struct {
		name, id, kind, subject string
		filter                  any
		want                    string
	}{
		{"manual", "cm", "manual", "song", nil, ""},
		{"manual take crate", "cmt", "manual", "take", nil, ""},
		{"smart", "cs", "smart", "song", `{"unheard":true}`, ""},
		{"smart without a filter", "x1", "smart", "song", nil, "CHECK"},
		{"manual with a filter", "x2", "manual", "song", `{}`, "CHECK"},
		{"filter that is not an object", "x3", "smart", "song", `[1]`, "CHECK"},
		{"filter over 4096 bytes", "x4", "smart", "song", `{"q":"` + strings.Repeat("a", 4100) + `"}`, "CHECK"},
		{"filter at the limit", "cs2", "smart", "take", `{"q":"` + strings.Repeat("a", 4096-8) + `"}`, ""},
		{"unknown kind", "x5", "dynamic", "song", nil, "CHECK"},
		{"unknown subject", "x6", "manual", "muse", nil, "CHECK"},
	} {
		err := exec(t, s, l2CrateQ, c.id, "Synthetic "+c.id, c.kind, c.subject, c.filter, l1ts, l1ts)
		if c.want == "" {
			if err != nil {
				t.Errorf("crate %s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("crate %s: error %v, want %q", c.name, err, c.want)
		}
	}
	expectErr(t, exec(t, s, `UPDATE crates SET kind='smart' WHERE id='cm'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE crates SET subject='take' WHERE id='cm'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE crates SET created_by='x' WHERE id='cm'`), "frozen column")
	mustExec(t, s, `UPDATE crates SET name='Renamed', revision=2 WHERE id='cm'`)
	expectErr(t, exec(t, s, `UPDATE crates SET filter_json=NULL WHERE id='cs'`), "CHECK")
	mustExec(t, s, `UPDATE crates SET filter_json='{"unheard":false}' WHERE id='cs'`)
	expectErr(t, exec(t, s, l2ItemQ, "i0", "cs", "s1", nil, l1ts), "only manual crates hold items")
	expectErr(t, exec(t, s, l2ItemQ, "i0", "cm", nil, "t1", l1ts), "does not match the crate subject")
	expectErr(t, exec(t, s, l2ItemQ, "i0", "cmt", "s1", nil, l1ts), "does not match the crate subject")
	expectErr(t, exec(t, s, l2ItemQ, "i0", "cm", nil, nil, l1ts), "CHECK")
	expectErr(t, exec(t, s, l2ItemQ, "i0", "cm", "s1", "t1", l1ts), "does not match the crate subject") // both set: the subject trigger fires first
	mustExec(t, s, l2ItemQ, "i1", "cm", "s1", nil, l1ts)
	mustExec(t, s, l2ItemQ, "i2", "cmt", nil, "t1", l1ts)
	expectErr(t, exec(t, s, l2ItemQ, "i3", "cm", "s1", nil, l1ts), "UNIQUE")
	expectErr(t, exec(t, s, l2ItemQ, "i3", "cmt", nil, "t1", l1ts), "UNIQUE")
	expectErr(t, exec(t, s, `UPDATE crate_items SET song_id='s2' WHERE id='i1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE crate_items SET crate_id='cs' WHERE id='i1'`), "frozen column")
	mustExec(t, s, `UPDATE crate_items SET removed_by='human:h', removed_at=? WHERE id='i1'`, l1ts)
	expectErr(t, exec(t, s, `UPDATE crate_items SET removed_at=NULL, removed_by=NULL WHERE id='i1'`), "immutable")
	mustExec(t, s, l2ItemQ, "i3", "cm", "s1", nil, l1ts) // remove-then-add
	// a retired Song may be a member (resolved through retired_into at read time)
	mustExec(t, s, l2ItemQ, "i4", "cm", "sd1", nil, l1ts)

	// ---- tapes ----
	tape := func(id, rec, key, state string, lossy, sources int, code any) error {
		return exec(t, s, l2TapeQ, id, rec, key, state, lossy, sources, code, l1ts)
	}
	if err := tape("tp1", "r1", tapeKey('a'), "queued", 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	expectErr(t, tape("tp2", "r1", tapeKey('a'), "queued", 0, 0, nil), "UNIQUE")
	expectErr(t, tape("tp2", "r1", "short", "queued", 0, 0, nil), "CHECK")
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "ready", 0, 0, nil), "CHECK")             // ready needs a sha256 and a file
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "failed", 0, 0, nil), "CHECK")            // failed needs an error code
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "queued", 0, 0, "TAPE_CLIPPED"), "CHECK") // only failed rows carry one
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "queued", 1, 0, nil), "CHECK")            // contains_lossy implies lossy_sources > 0
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "queued", 0, 2, nil), "CHECK")
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "running", 0, 0, nil), "CHECK") // running needs an owner and a lease
	expectErr(t, tape("tp2", "r1", tapeKey('b'), "paused", 0, 0, nil), "CHECK")
	expectErr(t, exec(t, s, `UPDATE tapes SET key=? WHERE id='tp1'`, tapeKey('z')), "frozen column")
	expectErr(t, exec(t, s, `UPDATE tapes SET record_id='r2' WHERE id='tp1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE tapes SET params_json='{"a":1}' WHERE id='tp1'`), "frozen column")
	const finish = `state='ready', owner=NULL, lease_expires_at=NULL, sha256=?, bytes=100, path='derived/tapes/aa/x.flac', filename='x.flac', total_samples=48000, finished_at=?`
	expectErr(t, exec(t, s, `UPDATE tapes SET `+finish+` WHERE id='tp1'`, tapeKey('c'), l1ts), "illegal state transition") // queued -> ready
	mustExec(t, s, `UPDATE tapes SET state='running', owner='runner-1', lease_expires_at=?, started_at=? WHERE id='tp1'`, l1ts, l1ts)
	mustExec(t, s, `UPDATE tapes SET state='queued', owner=NULL, lease_expires_at=NULL, attempts=attempts+1 WHERE id='tp1'`) // recovery
	mustExec(t, s, `UPDATE tapes SET state='running', owner='runner-1', lease_expires_at=? WHERE id='tp1'`, l1ts)
	mustExec(t, s, `UPDATE tapes SET progress_json='{"phase":"encode"}' WHERE id='tp1'`)
	mustExec(t, s, `UPDATE tapes SET `+finish+`, plan_json='{"chapters":[]}', lossy_sources=1, contains_lossy=1 WHERE id='tp1'`, tapeKey('c'), l1ts)
	expectErr(t, exec(t, s, `UPDATE tapes SET sha256=? WHERE id='tp1'`, tapeKey('d')), "a finished tape is immutable")
	expectErr(t, exec(t, s, `UPDATE tapes SET plan_json='{}' WHERE id='tp1'`), "a finished tape is immutable")
	expectErr(t, exec(t, s, `UPDATE tapes SET state='queued' WHERE id='tp1'`), "illegal state transition")
	mustExec(t, s, `UPDATE tapes SET state='expired' WHERE id='tp1'`)
	// a failed tape frees its key
	if err := tape("tp3", "r1", tapeKey('e'), "queued", 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE tapes SET state='failed' WHERE id='tp3'`), "CHECK")
	mustExec(t, s, `UPDATE tapes SET state='failed', error_code='TAPE_DECODE_FAILED', error_message='item 2', finished_at=? WHERE id='tp3'`, l1ts)
	if err := tape("tp4", "r1", tapeKey('e'), "queued", 0, 0, nil); err != nil {
		t.Fatalf("a failed row must not block its key: %v", err)
	}
	expectErr(t, exec(t, s, `UPDATE tapes SET state='queued', error_code=NULL WHERE id='tp3'`), "illegal state transition")
	if err := tape("tp5", "r1", tapeKey('f'), "queued", 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE tapes SET state='failed', error_code='TAPE_INTERRUPTED', finished_at=? WHERE id='tp5'`, l1ts) // queued -> failed

	// ---- no physical delete, anywhere ----
	for tb, want := range map[string]string{"records": "records: no physical delete", "record_slots": "record_slots: no physical delete",
		"record_changes": "append-only", "crates": "crates: no physical delete", "crate_items": "crate_items: no physical delete", "tapes": "tapes: no physical delete"} {
		expectErr(t, exec(t, s, `DELETE FROM `+tb), want)
	}
}

// TestTriggers_MuseOrdinal pins the frozen Muse ordinal (D-L2-37): assigned by the database, set once, unique.
func TestTriggers_MuseOrdinal(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	const ins = `INSERT INTO muses(id,kind,title,title_fold,brief,key,source,created_by,created_at,updated_by,updated_at,ordinal) VALUES(?,'brief','B','b','text',?,'human','h',?,'h',?,?)`
	ordinal := func(id string) (n int) {
		t.Helper()
		if err := s.R.QueryRow(`SELECT ordinal FROM muses WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	mustExec(t, s, ins, "ma", "brief:ma", l1ts, l1ts, nil)
	mustExec(t, s, ins, "mb", "brief:mb", l1ts, l1ts, nil)
	if a, b := ordinal("ma"), ordinal("mb"); a != 1 || b != 2 {
		t.Fatalf("ordinals %d, %d; want 1, 2", a, b)
	}
	expectErr(t, exec(t, s, `UPDATE muses SET ordinal = 9 WHERE id = 'ma'`), "ordinal is set once")
	expectErr(t, exec(t, s, `UPDATE muses SET ordinal = NULL WHERE id = 'ma'`), "ordinal is set once")
	expectErr(t, exec(t, s, `UPDATE muses SET ordinal = 1 WHERE id = 'mb'`), "ordinal is set once")
	mustExec(t, s, `UPDATE muses SET ordinal = 2 WHERE id = 'mb'`) // the same value is not a change
	expectErr(t, exec(t, s, ins, "mc", "brief:mc", l1ts, l1ts, 1), "UNIQUE")
	expectErr(t, exec(t, s, ins, "mc", "brief:mc", l1ts, l1ts, 0), "CHECK")
	// hiding, renaming and a revision bump never touch it
	mustExec(t, s, `UPDATE muses SET hidden_at = ?, hidden_by = 'h', title = 'B2', revision = revision + 1 WHERE id = 'ma'`, l1ts)
	if got := ordinal("ma"); got != 1 {
		t.Errorf("ordinal after hide = %d", got)
	}
	// an explicit ordinal is kept, and the next row continues after the maximum
	mustExec(t, s, ins, "md", "brief:md", l1ts, l1ts, 50)
	mustExec(t, s, ins, "me", "brief:me", l1ts, l1ts, nil)
	if d, e := ordinal("md"), ordinal("me"); d != 50 || e != 51 {
		t.Errorf("ordinals %d, %d; want 50, 51", d, e)
	}
	expectErr(t, exec(t, s, `DELETE FROM muses WHERE id = 'ma'`), "no physical delete")
}
