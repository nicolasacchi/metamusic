package records_test

// The Syncer tests drive real pick_events and song_picks rows written through SQL in the exact shape the Reconciler writes
// them (insert the event, move the Song's pick row): the L2 fixture is SQL-only and so are these. The Reconciler itself is
// proven by the acceptance mirror of lane INT-B.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var eventN atomic.Int64

// setPick writes one Pick event for a Song and moves its song_picks row; take "" means the Song has no Pick (source none).
// It returns the event id.
func (r *rig) setPick(songID, take, source, reason, evidence string) string {
	r.t.Helper()
	var seq int
	var prevTake *string
	prevSource := "none"
	if err := r.st.W.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM pick_events WHERE song_id = ?`, songID).Scan(&seq); err != nil {
		r.t.Fatal(err)
	}
	_ = r.st.W.QueryRow(`SELECT take_id, source FROM song_picks WHERE song_id = ?`, songID).Scan(&prevTake, &prevSource)
	var to any
	if take != "" {
		to = take
	} else {
		source = "none"
	}
	if evidence == "" {
		evidence = fmt.Sprintf(`{"def":"mm.pick/1","rule":%q,"eligible":2}`, source)
	}
	id := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe20000+eventN.Add(1))
	at := store.FormatTime(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).Add(time.Duration(eventN.Load()) * time.Second))
	r.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES(?,?,?,?,?,?,?,?,?,'system:provenance','system','provenance',?)`, id, songID, seq+1, at, prevTake, to, prevSource, source, reason, evidence)
	var have int
	_ = r.st.W.QueryRow(`SELECT COUNT(*) FROM song_picks WHERE song_id = ?`, songID).Scan(&have)
	if have == 0 {
		r.exec(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,?,?,?,?,?)`, songID, to, source, evidence, id, at, at)
	} else {
		r.exec(`UPDATE song_picks SET take_id=?, source=?, evidence_json=?, last_event_id=?, changed_at=?, computed_at=? WHERE song_id=?`, to, source, evidence, id, at, at, songID)
	}
	return id
}

func (r *rig) syncer() *records.Syncer {
	return records.NewSyncer(r.svc, slog.New(slog.DiscardHandler))
}

func (r *rig) settle() records.SyncReport {
	r.t.Helper()
	rep, err := r.syncer().Settle(context.Background())
	if err != nil {
		r.t.Fatalf("Settle: %v", err)
	}
	return rep
}

func (r *rig) wantClean() {
	r.t.Helper()
	an, err := r.svc.Verify(context.Background())
	if err != nil {
		r.t.Fatalf("Verify: %v", err)
	}
	if len(an) != 0 {
		r.t.Fatalf("Verify: %+v", an)
	}
}

func (r *rig) take(label string) string { return r.fx.Takes[label] }

// pendingIn counts the floating slots of one Record that have Pick events beyond their cursor.
func (r *rig) pendingIn(recID string) int {
	r.t.Helper()
	return count(r.t, r, `SELECT COUNT(*) FROM record_slots s WHERE s.record_id = ? AND s.removed_at IS NULL AND s.pinned_take_id IS NULL
		AND EXISTS (SELECT 1 FROM pick_events e WHERE e.song_id = s.song_id AND e.seq > s.synced_seq)`, recID)
}

// tableHash is a digest of the three tables the Syncer writes.
func (r *rig) tableHash() string {
	r.t.Helper()
	h := sha256.New()
	for _, q := range []string{
		`SELECT id, record_id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, pick_event_id, evidence_json, detail_json, actor FROM record_changes ORDER BY record_id, seq`,
		`SELECT id, song_id, synced_seq, removed_at, pinned_take_id FROM record_slots ORDER BY id`,
		`SELECT id, order_json, revision, updated_at FROM records ORDER BY id`,
	} {
		rows, err := r.st.R.Query(q)
		if err != nil {
			r.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				r.t.Fatal(err)
			}
			fmt.Fprintln(h, vals...)
		}
		rows.Close()
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

type wantRow struct {
	kind, from, to, reason string
	undecided              bool
	pos                    int
}

func (r *rig) wantRows(recID string, skip int, want []wantRow) {
	r.t.Helper()
	cs := r.changes(recID)
	if len(cs) < skip {
		r.t.Fatalf("only %d rows", len(cs))
	}
	var got []wantRow
	for _, c := range cs[skip:] {
		w := wantRow{kind: c.Kind}
		if c.FromTakeID != nil {
			w.from = *c.FromTakeID
		}
		if c.ToTakeID != nil {
			w.to = *c.ToTakeID
		}
		if c.Reason != nil {
			w.reason = *c.Reason
		}
		if c.Position != nil {
			w.pos = *c.Position
		}
		w.undecided = strings.Contains(string(c.Detail), `"undecided":true`)
		if c.Actor != records.SyncerActor || c.ActorKind != records.SyncerActorKind || c.ActorName != records.SyncerActorName {
			r.t.Errorf("row %d actor = %s %s %s", c.Seq, c.Actor, c.ActorKind, c.ActorName)
		}
		got = append(got, w)
	}
	if d := cmp.Diff(want, got, cmp.AllowUnexported(wantRow{})); d != "" {
		r.t.Errorf("rows (-want +got):\n%s", d)
	}
}

func TestSyncer_Table(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s3, s4 := r.song("S3"), r.song("S4")
	b1, b2, c1, c2 := r.take("b1"), r.take("b2"), r.take("c1"), r.take("c2")
	rec := r.newRecord(agent, "S3", "S4")
	created := len(r.changes(rec.ID))
	if rep := r.settle(); rep != (records.SyncReport{}) {
		t.Fatalf("nothing pending, report = %+v", rep)
	}

	// S01-S06: one event each, then one Settle consumes them all in order
	e1 := r.setPick(s3, b2, "ladder", "ladder", `{"def":"mm.pick/1","rule":"ladder","duels_n":2}`)
	r.setPick(s3, b1, "stars", "stars", `{"def":"mm.pick/1","rule":"stars","stars":4,"tied":1}`)
	r.setPick(s3, b1, "ladder", "ladder", "") // S03: the same Take under another source: no row
	r.setPick(s3, "", "none", "emptied", "")  // S04
	r.setPick(s3, b2, "newest", "newest", "") // S05
	r.setPick(s4, c2, "newest", "newest", "") // S06
	y := r.syncer()
	// the two slots of the new Record and slot B of R1, which floats on S3 too (S4 is pinned in R1)
	if n, err := y.Pending(context.Background()); err != nil || n != 3 {
		t.Fatalf("Pending = %d, %v; want 3", n, err)
	}
	rep, err := y.Settle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Slots != 3 || rep.Rows != 9 || rep.Followed != 0 || rep.Removed != 0 {
		t.Errorf("report = %+v", rep)
	}
	r.wantRows(rec.ID, created, []wantRow{
		{"slot_upgraded", b1, b2, "ladder", false, 1},
		{"slot_upgraded", b2, b1, "stars", true, 1},
		{"slot_emptied", b1, "", "emptied", false, 1},
		{"slot_filled", "", b2, "newest", true, 1},
		{"slot_upgraded", c1, c2, "newest", true, 2},
	})
	cs := r.changes(rec.ID)
	first := cs[created]
	if first.PickEventID == nil || *first.PickEventID != e1 || first.SlotID == nil || string(first.Evidence) != `{"def":"mm.pick/1","rule":"ladder","duels_n":2}` {
		t.Errorf("first row = %+v", first)
	}
	if got := records.ChangeText(first, func(string) string { return "T" }); got != "Slot 1 upgraded: T → T (won 2 blind duels)" {
		t.Errorf("text = %q", got)
	}
	if got := records.ChangeText(cs[created+3], func(string) string { return "T" }); got != "Slot 1 filled: T (newest take · undecided)" {
		t.Errorf("text = %q", got)
	}
	for _, sl := range r.slots(rec.ID) {
		var maxSeq int
		_ = r.st.R.QueryRow(`SELECT MAX(seq) FROM pick_events WHERE song_id = ?`, sl.SongID).Scan(&maxSeq)
		if sl.SyncedSeq != maxSeq {
			t.Errorf("slot %d cursor = %d, want %d", sl.Position, sl.SyncedSeq, maxSeq)
		}
	}
	if got := r.get(rec.ID); got.Revision != rec.Revision || got.UpdatedAt != rec.UpdatedAt {
		t.Errorf("the Syncer edited the Record: %+v -> %+v", rec, got)
	}
	r.wantClean()
	if rep := r.settle(); rep != (records.SyncReport{}) {
		t.Errorf("second Settle = %+v", rep)
	}
}

func TestSyncer_PinnedRemovedAndHidden(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	b1, b2 := r.take("b1"), r.take("b2")
	s3 := r.song("S3")
	rec := r.newRecord(agent, "S3", "S1", "S6")
	slots := r.slotIDs(rec.ID)
	res := mustEdit(t, r, agent, rec,
		records.SlotOp{Op: "pin", SlotID: slots[0], TakeID: b2},
		records.SlotOp{Op: "remove", SlotID: slots[1]})
	rec = res.Record
	if _, err := r.svc.Update(ctx, agent, rec.ID, records.Patch{Revision: rec.Revision, Hidden: bp(true)}); err != nil {
		t.Fatal(err)
	}
	rows := len(r.changes(rec.ID))
	r.setPick(s3, b2, "ladder", "ladder", "")
	r.setPick(r.song("S1"), "", "none", "emptied", "")
	if n := r.pendingIn(rec.ID); n != 0 {
		t.Fatalf("pinned and removed slots are pending: %d", n)
	}
	r.settle()
	if len(r.changes(rec.ID)) != rows {
		t.Error("pinned or removed slots were logged")
	}
	// S07: a hidden Record is synced (slot 3, S6 is floating)
	r.setPick(r.song("S6"), "", "none", "emptied", "")
	r.settle()
	if got := len(r.changes(rec.ID)); got != rows+1 {
		t.Errorf("hidden record: %d rows, want %d", got, rows+1)
	}
	// the unpin restarts the cursor: the pinned-time event is not logged afterwards, a later one is
	cur := r.get(rec.ID)
	mustEdit(t, r, agent, cur, records.SlotOp{Op: "unpin", SlotID: slots[0]})
	rows = len(r.changes(rec.ID))
	r.settle()
	if got := len(r.changes(rec.ID)); got != rows {
		t.Errorf("events from the pinned period were logged: %d rows, want %d", got, rows)
	}
	r.setPick(s3, b1, "stars", "stars", "")
	r.settle()
	if got := len(r.changes(rec.ID)); got != rows+1 {
		t.Errorf("after unpin: %d rows, want %d", got, rows+1)
	}
	r.wantClean()
}

func TestSyncer_Idempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S3", "S4", "S6")
	cursors := map[string]int{}
	for _, sl := range r.slots(rec.ID) {
		cursors[sl.ID] = sl.SyncedSeq
	}
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	r.setPick(r.song("S6"), "", "none", "emptied", "")
	r.settle()
	h := r.tableHash()
	if rep := r.settle(); rep != (records.SyncReport{}) {
		t.Errorf("second Settle = %+v", rep)
	}
	if r.tableHash() != h {
		t.Error("a second Settle changed the changelog")
	}
	// even with the cursors forced back, no event is logged twice: the unique (slot, event) index drops the rows
	r.exec(`DROP TRIGGER record_slots_synced_monotonic`)
	for id, seq := range cursors {
		r.exec(`UPDATE record_slots SET synced_seq = ? WHERE id = ?`, seq, id)
	}
	before := count(t, r, `SELECT COUNT(*) FROM record_changes`)
	rep := r.settle()
	if rep.Rows != 0 || count(t, r, `SELECT COUNT(*) FROM record_changes`) != before {
		t.Errorf("replayed events were logged again: %+v", rep)
	}
	if n := r.pendingIn(rec.ID); n != 0 {
		t.Errorf("%d cursors did not advance", n)
	}
	if r.tableHash() != h {
		t.Error("the replay changed the changelog")
	}
}

func TestSyncer_TwoSyncers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var path string
	if err := r.st.R.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	n := 0
	svc2 := records.New(st2, nil, func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe30000+n) })
	for i := 0; i < 6; i++ {
		r.newRecord(agent, "S3", "S4", "S6", "S1")
	}
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	r.setPick(r.song("S4"), r.take("c2"), "newest", "newest", "")
	r.setPick(r.song("S6"), "", "none", "emptied", "")
	r.setPick(r.song("S6"), r.take("s6a"), "newest", "newest", "")
	r.setPick(r.song("S1"), "", "none", "emptied", "")
	r.setPick(r.song("S1"), r.take("s1a"), "newest", "newest", "")
	y1, y2 := r.syncer(), records.NewSyncer(svc2, slog.New(slog.DiscardHandler))
	var busy1, busy2 atomic.Int32
	y1.SetFault(func(string) error {
		if busy1.Add(1) == 1 {
			return errors.New("database is locked (SQLITE_BUSY)")
		}
		return nil
	})
	y2.SetFault(func(string) error {
		if busy2.Add(1) == 1 {
			return errors.New("database is locked (SQLITE_BUSY)")
		}
		return nil
	})
	var wg sync.WaitGroup
	for _, y := range []*records.Syncer{y1, y2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := y.Settle(context.Background()); err != nil {
				t.Errorf("Settle: %v", err)
			}
		}()
	}
	wg.Wait()
	if busy1.Load() < 2 || busy2.Load() < 2 {
		t.Errorf("the busy retry was not exercised: %d %d", busy1.Load(), busy2.Load())
	}
	// per new Record: S3 1 row, S4 1, S6 2, S1 2; the fixture's floating slots add S3 (R1) 1, S1 (R2) 2 and S6 (R2) 2
	const want = 6*6 + 5
	if got := count(t, r, `SELECT COUNT(*) FROM record_changes WHERE pick_event_id IS NOT NULL`); got != want {
		t.Errorf("syncer rows = %d, want %d", got, want)
	}
	if got := count(t, r, `SELECT COUNT(*) FROM (SELECT DISTINCT slot_id, pick_event_id FROM record_changes WHERE pick_event_id IS NOT NULL)`); got != want {
		t.Errorf("distinct (slot, event) = %d, want %d", got, want)
	}
	r.wantClean()
}

func TestSyncer_BatchBoundary(t *testing.T) {
	t.Parallel()
	r := newRigWith(t, true)
	var ids []string
	for i := 1; i <= 50; i++ {
		ids = append(ids, r.song("S"+fmt.Sprint(i)))
	}
	for i := 0; i < 5; i++ {
		if _, err := r.svc.Create(context.Background(), agent, records.NewRecord{Title: "Synthetic Batch Record", SongIDs: ids}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		var root string
		if err := r.st.R.QueryRow(`SELECT root_track_id FROM songs WHERE id = ?`, id).Scan(&root); err != nil {
			t.Fatal(err)
		}
		r.setPick(id, root, "ladder", "ladder", "")
	}
	y := r.syncer()
	if n, err := y.Pending(context.Background()); err != nil || n != 250 {
		t.Fatalf("Pending = %d, %v", n, err)
	}
	rep, err := y.Settle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Slots != 250 || rep.Rows != 250 || y.Passes() != 2 {
		t.Errorf("report = %+v, passes = %d; want 250 slots, 250 rows, 2 passes", rep, y.Passes())
	}
	if n, _ := y.Pending(context.Background()); n != 0 {
		t.Errorf("Pending after Settle = %d", n)
	}
}

func TestSyncer_RunPollsOnlyWhenPending(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		y := records.NewSyncer(records.New(nil, nil, nil), slog.New(slog.DiscardHandler))
		var pending, probes, settles atomic.Int32
		y.SetProbe(func(context.Context) (int, error) { probes.Add(1); return int(pending.Load()), nil })
		y.SetSettle(func(context.Context) error { settles.Add(1); pending.Store(0); return nil })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { y.Run(ctx); close(done) }()
		for i := 0; i < 5; i++ {
			time.Sleep(records.SyncPoll)
			synctest.Wait()
		}
		if probes.Load() != 5 || settles.Load() != 0 {
			t.Fatalf("idle: %d probes, %d settles; want 5 and 0", probes.Load(), settles.Load())
		}
		pending.Store(1)
		time.Sleep(records.SyncPoll)
		synctest.Wait()
		if probes.Load() != 6 || settles.Load() != 1 {
			t.Fatalf("one event: %d probes, %d settles; want 6 and 1", probes.Load(), settles.Load())
		}
		time.Sleep(records.SyncPoll)
		synctest.Wait()
		if probes.Load() != 7 || settles.Load() != 1 {
			t.Fatalf("back to idle: %d probes, %d settles; want 7 and 1", probes.Load(), settles.Load())
		}
		cancel()
		<-done
	})
}

func TestEditSlots_CatchesUpFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S3", "S4")
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	r.setPick(r.song("S4"), r.take("c2"), "newest", "newest", "")
	res := mustEdit(t, r, agent, rec, records.SlotOp{Op: "add", SongID: r.song("S1"), Position: records.PositionEnd})
	cs := r.changes(rec.ID)
	var order []string
	for _, c := range cs {
		order = append(order, fmt.Sprintf("%d:%s", c.Seq, c.Kind))
	}
	want := []string{"1:created", "2:slot_added", "3:slot_added", "4:slot_upgraded", "5:slot_upgraded", "6:slot_added"}
	if d := cmp.Diff(want, order); d != "" {
		t.Errorf("changelog order (-want +got):\n%s", d)
	}
	if len(res.Changes) != 1 || res.Record.Revision != rec.Revision+1 {
		t.Errorf("the edit returned %d rows at revision %d", len(res.Changes), res.Record.Revision)
	}
	if n := r.pendingIn(rec.ID); n != 0 {
		t.Errorf("pending slots of the record after the edit = %d", n)
	}
	r.wantClean()
}

func retire(r *rig, from, into string) {
	r.t.Helper()
	r.exec(`UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, "2026-10-07T00:00:00.000Z", into, from)
}

func TestSyncer_FollowRetired(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r1 := r.get(r.fx.Records["R1"])
	slotD, slotC := r.fx.Slots["D"], r.fx.Slots["C"]
	rows := len(r.changes(r1.ID))
	retire(r, r.song("S7"), r.song("S1"))
	y := r.syncer()
	if n, _ := y.Pending(context.Background()); n != 1 {
		t.Fatalf("Pending = %d, want the slot on the retired Song", n)
	}
	if an, _ := r.svc.Verify(context.Background()); len(an) != 1 || an[0].Code != records.CodeRetiredSong || an[0].Severity != "warn" {
		t.Errorf("before the follow: %+v", an)
	}
	rep, err := y.Settle(context.Background())
	if err != nil || rep.Followed != 1 || rep.Rows != 1 || rep.Removed != 0 {
		t.Fatalf("Settle = %+v, %v", rep, err)
	}
	r.wantRows(r1.ID, rows, []wantRow{{"slot_song_followed", "", r.take("s1a"), "retired", false, 4}})
	var song string
	var synced int
	_ = r.st.R.QueryRow(`SELECT song_id, synced_seq FROM record_slots WHERE id = ?`, slotD).Scan(&song, &synced)
	if song != r.song("S1") || synced != 1 {
		t.Errorf("slot D = %s at %d", song, synced)
	}
	if got := r.get(r1.ID); got.Revision != r1.Revision || got.UpdatedAt != r1.UpdatedAt {
		t.Error("following edited the Record")
	}
	var pin string
	_ = r.st.R.QueryRow(`SELECT pinned_take_id FROM record_slots WHERE id = ?`, slotC).Scan(&pin)
	if pin != r.take("c2") {
		t.Errorf("a pin was lost: %q", pin)
	}
	r.wantClean()
	// the followed slot now follows S1's Picks
	r.setPick(r.song("S1"), "", "none", "emptied", "")
	r.settle()
	if got := count(t, r, `SELECT COUNT(*) FROM record_changes WHERE slot_id = ? AND kind = 'slot_emptied'`, slotD); got != 1 {
		t.Errorf("emptied rows of the followed slot = %d", got)
	}
	r.wantClean()
}

func TestSyncer_DuplicateRewritesOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r1 := r.get(r.fx.Records["R1"])
	oldOrder := r.slotIDs(r1.ID)
	slotD := r.fx.Slots["D"]
	retire(r, r.song("S7"), r.song("S2")) // S2 is slot A of the same Record
	rep := r.settle()
	if rep.Removed != 1 || rep.Rows != 1 || rep.Followed != 0 {
		t.Fatalf("report = %+v", rep)
	}
	after := r.get(r1.ID)
	if after.Revision != r1.Revision || after.UpdatedAt != r1.UpdatedAt || after.UpdatedBy != r1.UpdatedBy {
		t.Errorf("the duplicate removal edited the Record: %+v", after)
	}
	var want []string
	for _, id := range oldOrder {
		if id != slotD {
			want = append(want, id)
		}
	}
	if d := cmp.Diff(want, r.slotIDs(r1.ID)); d != "" {
		t.Errorf("order (-want +got):\n%s", d)
	}
	last := r.changes(r1.ID)
	c := last[len(last)-1]
	if c.Kind != "slot_removed" || *c.Reason != "duplicate" || c.Actor != records.SyncerActor || *c.SlotID != slotD || *c.Position != 4 {
		t.Errorf("row = %+v", c)
	}
	r.wantClean()
	// a client holding the old order
	_, err := r.svc.EditSlots(ctx, human, r1.ID, r1.Revision, []records.SlotOp{{Op: "reorder", SlotIDs: oldOrder}})
	pe := wantProblem(t, err, 422, "SLOT_ORDER_MISMATCH")
	same(t, "extra", fmt.Sprint(pe.Details["extra"]), "["+slotD+"]")
	_, err = r.svc.EditSlots(ctx, human, r1.ID, r1.Revision, []records.SlotOp{{Op: "remove", SlotID: slotD}})
	wantProblem(t, err, 422, "UNKNOWN_SLOT")
}

func TestSyncer_NeverBumpsRevision(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	snap := func() map[string]string {
		out := map[string]string{}
		rows, err := r.st.R.Query(`SELECT id, revision || '|' || updated_at || '|' || updated_by FROM records`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, v string
			if err := rows.Scan(&id, &v); err != nil {
				t.Fatal(err)
			}
			out[id] = v
		}
		return out
	}
	before := snap()
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	r.setPick(r.song("S4"), "", "none", "emptied", "")
	r.setPick(r.song("S6"), "", "none", "emptied", "")
	retire(r, r.song("S7"), r.song("S2"))
	if rep := r.settle(); rep.Rows < 3 {
		t.Fatalf("report = %+v", rep)
	}
	if d := cmp.Diff(before, snap()); d != "" {
		t.Errorf("revision, updated_at or updated_by moved (-before +after):\n%s", d)
	}
}

func TestSyncer_FailingSlotDoesNotBlockOthers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S3", "S4")
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	r.setPick(r.song("S4"), r.take("c2"), "newest", "newest", "")
	bad := r.slotIDs(rec.ID)[0]
	y := r.syncer()
	y.SetFault(func(id string) error {
		if id == bad {
			return errors.New("synthetic failure")
		}
		return nil
	})
	rep, err := y.Settle(context.Background())
	if err != nil || rep.Skipped != 1 || rep.Rows != 2 { // the other new slot and R1's slot B
		t.Fatalf("Settle = %+v, %v", rep, err)
	}
	if n := r.pendingIn(rec.ID); n != 1 {
		t.Errorf("the failed slot should stay pending: %d", n)
	}
	// the failed slot is retried by the next Settle
	y.SetFault(nil)
	if rep := r.settle(); rep.Rows != 1 {
		t.Errorf("retry = %+v", rep)
	}
	r.wantClean()
}

func TestChanges_Paging(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r1 := r.fx.Records["R1"]
	all := r.changes(r1)
	page, err := r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Limit: 4})
	if err != nil || len(page.Items) != 4 || page.NextCursor == nil || page.Items[0].Seq != all[len(all)-1].Seq {
		t.Fatalf("first desc page: %+v %v", page, err)
	}
	var seqs []int
	cursor := 0
	for {
		p, err := r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Order: "asc", Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Items {
			seqs = append(seqs, c.Seq)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if len(seqs) != len(all) || seqs[0] != 1 || seqs[len(seqs)-1] != len(all) {
		t.Errorf("asc walk = %v over %d rows", seqs, len(all))
	}
	kinds, err := r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Kinds: []string{"slot_pinned"}})
	if err != nil || len(kinds.Items) != 1 || kinds.NextCursor != nil {
		t.Errorf("kinds filter: %+v %v", kinds, err)
	}
	def, err := r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Limit: 1000})
	if err != nil || len(def.Items) != len(all) {
		t.Errorf("limit above the cap: %d items, %v", len(def.Items), err)
	}
	_, err = r.svc.Changes(ctx, records.ChangeFilter{RecordID: "no-such-record"})
	wantProblem(t, err, 404, "NOT_FOUND")
	_, err = r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Kinds: []string{"bogus"}})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = r.svc.Changes(ctx, records.ChangeFilter{RecordID: r1, Order: "sideways"})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	var raw map[string]any
	b, _ := json.Marshal(all[0])
	if json.Unmarshal(b, &raw) != nil {
		t.Error("a row does not marshal")
	}
}
