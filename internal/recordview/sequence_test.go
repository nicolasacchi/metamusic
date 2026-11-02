package recordview_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func newL3Rig(t *testing.T) *servertest.Rig {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true})
	r.Settle(t)
	return r
}

// tRecord is a draft Record of the T Songs in the shuffled input order t4 t6 t1 t5 t3 t2.
func tRecord(t *testing.T, r *servertest.Rig) records.Record {
	t.Helper()
	var ids []string
	for _, l := range []string{"T4", "T6", "T1", "T5", "T3", "T2"} {
		ids = append(ids, idOf(t, r, "songs", "Synthetic Song "+l))
	}
	rec, err := r.Services.Records.Create(context.Background(), human, records.NewRecord{Title: "Synthetic Sequenced Record", SongIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

type seqDoc struct {
	Steps []struct {
		SongID    string `json:"song_id"`
		Title     string `json:"title"`
		TakeTitle string `json:"take_title"`
		Text      string `json:"text"`
	} `json:"steps"`
	Cost struct{ Input, Output string } `json:"cost"`
}

// TestRecordView_IncludeSequence: the latest run with titles injected at read, stale after a manual reorder or a moved Pick,
// absent without the include and for a Record never sequenced.
func TestRecordView_IncludeSequence(t *testing.T) {
	t.Parallel()
	r := newL3Rig(t)
	ctx := context.Background()
	svc := r.Services
	rec := tRecord(t, r)
	get := func(inc ...string) recordview.View {
		t.Helper()
		v, err := svc.RecordView.Get(ctx, human, rec.ID, recordview.GetOptions{Include: inc, ChangesLimit: 20})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := get(recordview.IncludeSequence); v.Sequence != nil {
		t.Fatalf("a Record never sequenced has a sequence: %+v", v.Sequence)
	}
	res, err := svc.Records.EditSlots(ctx, human, rec.ID, rec.Revision, []records.SlotOp{{Op: "sequence", Strategy: "arc"}})
	if err != nil {
		t.Fatal(err)
	}
	if v := get(); v.Sequence != nil {
		t.Error("Sequence is only returned on request")
	}
	v := get(recordview.IncludeSequence, recordview.IncludeChanges)
	s := v.Sequence
	if s == nil || s.ID != res.Sequences[0].ID || s.Strategy != "arc" || s.Stale || !s.Exact || s.Actor != human.Key() || s.ActorKind != "human" {
		t.Fatalf("sequence = %+v", s)
	}
	if s.CostInput != "8.9847" || s.CostOutput != "5.5198" {
		t.Errorf("costs = %s %s", s.CostInput, s.CostOutput)
	}
	var doc seqDoc
	if err := json.Unmarshal(s.Explanation, &doc); err != nil {
		t.Fatal(err)
	}
	for i, st := range doc.Steps {
		want := "Synthetic Song T" + string(rune('1'+i))
		if st.Title != want || st.TakeTitle == "" || st.Text == "" || v.Slots[i].Song.ID != st.SongID {
			t.Errorf("step %d = %+v", i+1, st)
		}
	}
	// the rows of the call name the run
	var sequenced int
	for _, c := range v.Changes {
		if c.Sequence != nil {
			sequenced++
			if c.Sequence.ID != s.ID || c.Sequence.Strategy != "arc" || !strings.Contains(c.Text, "(sequenced: arc)") {
				t.Errorf("change = %+v", c)
			}
		}
	}
	if sequenced == 0 {
		t.Error("no change row names the run")
	}
	for _, canary := range []string{canary1, canary2} {
		if strings.Contains(string(s.Explanation), canary) {
			t.Error("Muse text in the explanation")
		}
	}

	// a manual reorder makes it stale; reordering back makes it fresh again
	slots := v.Slots
	swap := []string{slots[1].ID, slots[0].ID}
	for _, sl := range slots[2:] {
		swap = append(swap, sl.ID)
	}
	cur := get().Record
	if _, err := svc.Records.EditSlots(ctx, human, rec.ID, cur.Revision, []records.SlotOp{{Op: "reorder", SlotIDs: swap}}); err != nil {
		t.Fatal(err)
	}
	if !get(recordview.IncludeSequence).Sequence.Stale {
		t.Error("a manual reorder must make the run stale")
	}
	cur = get().Record
	orig := make([]string, len(slots))
	for i, sl := range slots {
		orig[i] = sl.ID
	}
	if _, err := svc.Records.EditSlots(ctx, human, rec.ID, cur.Revision, []records.SlotOp{{Op: "reorder", SlotIDs: orig}}); err != nil {
		t.Fatal(err)
	}
	if get(recordview.IncludeSequence).Sequence.Stale {
		t.Error("the order is back: the run is fresh")
	}
	// the latest run wins
	cur = get().Record
	if _, err := svc.Records.EditSlots(ctx, human, rec.ID, cur.Revision, []records.SlotOp{{Op: "sequence", Strategy: "steady"}}); err != nil {
		t.Fatal(err)
	}
	if got := get(recordview.IncludeSequence).Sequence; got.Strategy != "steady" || got.ID == s.ID {
		t.Errorf("latest = %+v", got)
	}
}

// TestRecordView_IncludeSequence_PickMovedIsStale: a floating slot whose Pick moved after the run makes it stale though the
// order is the same.
func TestRecordView_IncludeSequence_PickMovedIsStale(t *testing.T) {
	t.Parallel()
	r := newL3Rig(t)
	ctx := context.Background()
	rec := tRecord(t, r)
	if _, err := r.Services.Records.EditSlots(ctx, human, rec.ID, rec.Revision, []records.SlotOp{{Op: "sequence"}}); err != nil {
		t.Fatal(err)
	}
	// the Song loses its Pick (every take discarded): the slot now resolves to no take
	song := idOf(t, r, "songs", "Synthetic Song T1")
	var seq int
	var prev string
	if err := r.Store.R.QueryRow(`SELECT (SELECT MAX(seq) FROM pick_events WHERE song_id = ?), take_id FROM song_picks WHERE song_id = ?`, song, song).Scan(&seq, &prev); err != nil {
		t.Fatal(err)
	}
	const ev, at, id = `{"def":"mm.pick/1","rule":"none","eligible":0}`, "2026-10-20T00:00:00.000Z", "01900000-0000-7000-8000-00000000f001"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES(?,?,?,?,?,NULL,(SELECT source FROM song_picks WHERE song_id = ?),'none','emptied','system:provenance','system','provenance',?)`, []any{id, song, seq + 1, at, prev, song, ev}},
		{`UPDATE song_picks SET take_id = NULL, source = 'none', evidence_json = ?, last_event_id = ?, changed_at = ?, computed_at = ? WHERE song_id = ?`, []any{ev, id, at, at, song}},
	} {
		if _, err := r.Store.W.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	v, err := r.Services.RecordView.Get(ctx, human, rec.ID, recordview.GetOptions{Include: []string{recordview.IncludeSequence}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Sequence == nil || !v.Sequence.Stale {
		t.Errorf("a moved Pick must make the run stale: %+v", v.Sequence)
	}
}
