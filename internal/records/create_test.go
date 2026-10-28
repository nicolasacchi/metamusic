package records_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestRecords_Create_Rules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	long := func(n int) string { return strings.Repeat("x", n) }
	for _, c := range []struct {
		name string
		in   records.NewRecord
		code string
	}{
		{"title empty", records.NewRecord{Title: "  "}, "VALIDATION_FAILED"},
		{"title 121", records.NewRecord{Title: long(121)}, "VALIDATION_FAILED"},
		{"title control", records.NewRecord{Title: "a\x07b"}, "VALIDATION_FAILED"},
		{"title tab", records.NewRecord{Title: "a\tb"}, "VALIDATION_FAILED"},
		{"subtitle 201", records.NewRecord{Title: "t", Subtitle: long(201)}, "VALIDATION_FAILED"},
		{"note 4001", records.NewRecord{Title: "t", LinerNote: long(4001)}, "VALIDATION_FAILED"},
		{"note control", records.NewRecord{Title: "t", LinerNote: "a\x00b"}, "VALIDATION_FAILED"},
		{"duplicate songs", records.NewRecord{Title: "t", SongIDs: []string{r.song("S1"), r.song("S1")}}, "VALIDATION_FAILED"},
		{"unknown song", records.NewRecord{Title: "t", SongIDs: []string{"no-such-song"}}, "UNKNOWN_SONG"},
	} {
		_, err := r.svc.Create(ctx, agent, c.in)
		wantProblem(t, err, 422, c.code)
		_ = c.name
	}
	if n := count(t, r, `SELECT COUNT(*) FROM records WHERE title = 't' OR title = ''`); n != 0 {
		t.Errorf("%d rows written by refused creates", n)
	}

	// a retired Song: 409 with retired_into, nothing written
	r.exec(`UPDATE songs SET retired_at = '2026-10-06T00:00:00.000Z', retired_into = ? WHERE id = ?`, r.song("S1"), r.song("S7"))
	_, err := r.svc.Create(ctx, agent, records.NewRecord{Title: "t", SongIDs: []string{r.song("S2"), r.song("S7")}})
	pe := wantProblem(t, err, 409, "SONG_RETIRED")
	same(t, "details", pe.Details, map[string]any{"song_id": r.song("S7"), "retired_into": r.song("S1")})
	if n := count(t, r, `SELECT COUNT(*) FROM records WHERE title = 't'`); n != 0 {
		t.Errorf("a refused create left %d rows", n)
	}

	// 61 ids
	sc := newRigWith(t, true)
	var ids []string
	for i := 1; i <= 61; i++ {
		ids = append(ids, sc.song("S"+strconv.Itoa(i)))
	}
	_, err = sc.svc.Create(ctx, agent, records.NewRecord{Title: "t", SongIDs: ids})
	wantProblem(t, err, 422, "TOO_MANY_SLOTS")
	if rec, err := sc.svc.Create(ctx, agent, records.NewRecord{Title: "t", SongIDs: ids[:60]}); err != nil || len(sc.slots(rec.ID)) != 60 {
		t.Fatalf("60 slots: %v", err)
	}

	// the happy path, by an agent and by a human: collapsed title, slots in order, rows created then slot_added
	for _, p := range []struct {
		who  string
		kind string
	}{{"agent", "agent"}, {"human", "human"}} {
		pr := agent
		if p.who == "human" {
			pr = human
		}
		rec, err := r.svc.Create(ctx, pr, records.NewRecord{Title: "  Synthetic   Title \n", Subtitle: " sub ", LinerNote: " line one\nline two ", SongIDs: []string{r.song("S3"), r.song("S1"), r.song("S2")}})
		if err != nil {
			t.Fatal(err)
		}
		if rec.Title != "Synthetic Title" || rec.Subtitle != "sub" || rec.LinerNote != "line one\nline two" {
			t.Errorf("texts = %q %q %q", rec.Title, rec.Subtitle, rec.LinerNote)
		}
		if rec.Status != records.StatusDraft || rec.Revision != 1 || rec.CoverRerolls != 0 || rec.CoverSeed != records.CoverSeed(rec.ID, 0) {
			t.Errorf("record = %+v", rec)
		}
		if rec.CreatedBy != pr.Key() || rec.UpdatedBy != pr.Key() {
			t.Errorf("created_by %q updated_by %q", rec.CreatedBy, rec.UpdatedBy)
		}
		sl := r.slots(rec.ID)
		same(t, "song order", []string{sl[0].SongID, sl[1].SongID, sl[2].SongID}, []string{r.song("S3"), r.song("S1"), r.song("S2")})
		for i, s := range sl {
			if s.Position != i+1 || s.PinnedTakeID != nil {
				t.Errorf("slot %d = %+v", i, s)
			}
		}
		// a floating slot starts at the Song's current max pick event
		if want := count(t, r, `SELECT COALESCE(MAX(seq),0) FROM pick_events WHERE song_id = ?`, sl[0].SongID); sl[0].SyncedSeq != want {
			t.Errorf("synced_seq = %d, want %d", sl[0].SyncedSeq, want)
		}
		cs := r.changes(rec.ID)
		same(t, "kinds", kinds(cs), []string{"created", "slot_added", "slot_added", "slot_added"})
		for i, c := range cs {
			if c.Seq != i+1 || c.Actor != pr.Key() || c.ActorKind != p.kind {
				t.Errorf("row %d = %+v", i, c)
			}
		}
		// slot_added carries the Pick Take of the Song (S3's is b1)
		if cs[1].ToTakeID == nil || *cs[1].ToTakeID != r.fx.Takes["b1"] || *cs[1].Position != 1 {
			t.Errorf("first slot_added = %+v", cs[1])
		}
	}
	// a Record without Songs
	rec, err := r.svc.Create(ctx, agent, records.NewRecord{Title: "Empty"})
	if err != nil || len(r.slots(rec.ID)) != 0 || len(r.changes(rec.ID)) != 1 {
		t.Fatalf("empty create: %v", err)
	}
}
