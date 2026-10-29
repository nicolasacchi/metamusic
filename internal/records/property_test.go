package records_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// propSongs lists, per fixture Song, the Takes it owns (S7 and the drafts have none).
var propSongs = map[string][]string{
	"S1": {"s1a"}, "S2": {"a1"}, "S3": {"b2", "b1"}, "S4": {"c1", "c2"}, "S5": {"e1"}, "S6": {"s6a"},
}

// propWorld is the random walk's state: the Songs, the Records and a seeded source.
type propWorld struct {
	t       *testing.T
	r       *rig
	rnd     *rand.Rand
	songs   []string // Song ids in play (with and without Takes)
	takes   map[string][]string
	recs    []string
	retired map[string]bool
	drafts  int
}

func (w *propWorld) song() string { return w.songs[w.rnd.Intn(len(w.songs))] }

func (w *propWorld) live() string {
	for i := 0; i < 8; i++ {
		if s := w.song(); !w.retired[s] {
			return s
		}
	}
	return w.r.song("S1")
}

func (w *propWorld) rec() (records.Record, bool) {
	if len(w.recs) == 0 {
		return records.Record{}, false
	}
	rec, err := w.r.svc.Get(context.Background(), w.recs[w.rnd.Intn(len(w.recs))])
	if err != nil {
		w.t.Fatal(err)
	}
	return rec, true
}

// draftSong adds a Song that has no Take yet (a retire candidate).
func (w *propWorld) draftSong() string {
	w.drafts++
	n := w.drafts
	did, sid := fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe40000+n), fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe41000+n)
	w.r.exec(`INSERT INTO recipe_drafts(id,origin,group_key,label,request_id,request_json,status,created_at,updated_at)
		VALUES(?,'import_backlog',?,'v1',?,'{}','backlog','2026-10-06T00:00:00.000Z','2026-10-06T00:00:00.000Z')`, did, "prop_"+strconv.Itoa(n), "prop_"+strconv.Itoa(n))
	w.r.exec(`INSERT INTO songs(id,root_track_id,draft_id,title,origin,created_by,created_at,updated_by,updated_at)
		VALUES(?,NULL,?,?,'draft','system:provenance','2026-10-06T00:00:00.000Z','system:provenance','2026-10-06T00:00:00.000Z')`, sid, did, "Synthetic Property Draft "+strconv.Itoa(n))
	w.songs = append(w.songs, sid)
	return sid
}

// expected reports whether err is one of the refusals a random edit may meet (a 4xx problem); anything else fails the run.
func (w *propWorld) expected(err error) {
	w.t.Helper()
	if err == nil {
		return
	}
	var pe *problem.Error
	if errors.As(err, &pe) && pe.Status >= 400 && pe.Status < 500 {
		return
	}
	w.t.Fatalf("unexpected error: %v", err)
}

func (w *propWorld) step(i int) {
	ctx := context.Background()
	r := w.r
	switch op := w.rnd.Intn(16); op {
	case 0, 1: // create a Record
		n := 1 + w.rnd.Intn(5)
		seen := map[string]bool{}
		var ids []string
		for len(ids) < n {
			s := w.live()
			if !seen[s] {
				seen[s] = true
				ids = append(ids, s)
			}
		}
		p := []auth.Principal{agent, human}[w.rnd.Intn(2)]
		rec, err := r.svc.Create(ctx, p, records.NewRecord{Title: "Synthetic Property Record", SongIDs: ids})
		w.expected(err)
		if err == nil {
			w.recs = append(w.recs, rec.ID)
		}
	case 2, 3, 4, 5: // slot edits by a human (who may edit a published Record)
		rec, ok := w.rec()
		if !ok {
			return
		}
		slots, err := r.svc.Slots(ctx, rec.ID)
		w.expected(err)
		var sop records.SlotOp
		pick := func() records.Slot { return slots[w.rnd.Intn(len(slots))] }
		switch k := w.rnd.Intn(6); {
		case k == 0 || len(slots) == 0:
			sop = records.SlotOp{Op: "add", SongID: w.live(), Position: records.PositionEnd}
		case k == 1:
			sop = records.SlotOp{Op: "remove", SlotID: pick().ID}
		case k == 2:
			sop = records.SlotOp{Op: "move", SlotID: pick().ID, Position: 1 + w.rnd.Intn(len(slots))}
		case k == 3:
			sl := pick()
			if ts := w.takes[sl.SongID]; len(ts) > 0 {
				sop = records.SlotOp{Op: "pin", SlotID: sl.ID, TakeID: ts[w.rnd.Intn(len(ts))]}
			} else {
				sop = records.SlotOp{Op: "unpin", SlotID: sl.ID}
			}
		case k == 4:
			sop = records.SlotOp{Op: "unpin", SlotID: pick().ID}
		default:
			sop = []records.SlotOp{{Op: "pin_all"}, {Op: "unpin_all"}}[w.rnd.Intn(2)]
		}
		_, err = r.svc.EditSlots(ctx, human, rec.ID, rec.Revision, []records.SlotOp{sop})
		w.expected(err)
	case 6, 7, 8, 9: // a Pick moves (a rating, a duel verdict, a new Take, an emptied Song)
		s := w.song()
		ts := w.takes[s]
		if len(ts) == 0 {
			return
		}
		cur := ""
		_ = r.st.R.QueryRow(`SELECT COALESCE(take_id, '') FROM song_picks WHERE song_id = ?`, s).Scan(&cur)
		source := []string{"ladder", "stars", "newest"}[w.rnd.Intn(3)]
		to := ts[w.rnd.Intn(len(ts))]
		reason := source
		if w.rnd.Intn(5) == 0 {
			to, source, reason = "", "none", "emptied"
		}
		var prevSource string
		_ = r.st.R.QueryRow(`SELECT COALESCE(source, 'none') FROM song_picks WHERE song_id = ?`, s).Scan(&prevSource)
		if to == cur || (to == "" && cur == "") || (to != "" && cur == to && prevSource == source) {
			return
		}
		r.setPick(s, to, source, reason, "")
	case 10: // discard or restore a Take
		s := w.song()
		if ts := w.takes[s]; len(ts) > 0 {
			t := ts[w.rnd.Intn(len(ts))]
			r.exec(`UPDATE tracks SET status = ? WHERE id = ?`, []string{"discarded", "draft"}[w.rnd.Intn(2)], t)
		}
	case 11: // retire a Song that has no Take into another live Song
		var cands []string
		for _, s := range w.songs {
			if len(w.takes[s]) == 0 && !w.retired[s] {
				cands = append(cands, s)
			}
		}
		if len(cands) == 0 {
			w.draftSong()
			return
		}
		from, into := cands[w.rnd.Intn(len(cands))], w.live()
		if from == into {
			return
		}
		retire(r, from, into)
		w.retired[from] = true
	case 12: // a Song with no Take appears
		w.draftSong()
	case 13: // propose / publish / unpublish
		rec, ok := w.rec()
		if !ok {
			return
		}
		var err error
		switch w.rnd.Intn(3) {
		case 0:
			_, err = r.svc.Propose(ctx, agent, rec.ID, rec.Revision, "synthetic rationale")
		case 1:
			_, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
		default:
			_, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, false)
		}
		w.expected(err)
	case 14: // rename or hide
		rec, ok := w.rec()
		if !ok {
			return
		}
		_, err := r.svc.Update(ctx, human, rec.ID, records.Patch{Revision: rec.Revision, Title: sp("Synthetic Renamed " + strconv.Itoa(i)), Hidden: bp(w.rnd.Intn(2) == 0)})
		w.expected(err)
	default: // settle
		r.settle()
	}
}

// noErrors asserts Verify reports no error. Mid-run (final false) warnings are legal too: a slot on a retired Song waits for the
// next Settle. After the final Settle only published_empty may remain (a published Record whose slots were all removed).
func (w *propWorld) noErrors(final bool) {
	w.t.Helper()
	an, err := w.r.svc.Verify(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	for _, a := range an {
		if a.Severity == "error" || (final && a.Code != records.CodePublishedEmpty) {
			w.t.Fatalf("Verify: %+v", a)
		}
	}
}

// replay folds a Record's changelog per live slot and compares it with the Takes the slots resolve to now.
func (w *propWorld) replay(recID string) {
	w.t.Helper()
	ctx := context.Background()
	res, err := w.r.svc.Resolved(ctx, recID)
	if err != nil {
		w.t.Fatal(err)
	}
	last := map[string]*string{}
	cursor := 0
	for {
		page, err := w.r.svc.Changes(ctx, records.ChangeFilter{RecordID: recID, Order: "asc", Cursor: cursor, Limit: records.MaxChangesPage})
		if err != nil {
			w.t.Fatal(err)
		}
		for _, c := range page.Items {
			switch c.Kind {
			case "slot_added", "slot_pinned", "slot_unpinned", "slot_upgraded", "slot_filled", "slot_emptied", "slot_song_followed":
				last[*c.SlotID] = c.ToTakeID
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	for _, rs := range res {
		got, want := last[rs.Slot.ID], (*string)(nil)
		if rs.Take != nil {
			want = &rs.Take.ID
		}
		if !sameTakePtr(got, want) {
			w.t.Fatalf("record %s slot %d: the changelog replays to %v, the slot resolves to %v", recID, rs.Slot.Position, deref(got), deref(want))
		}
	}
}

func sameTakePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func deref(s *string) string {
	if s == nil {
		return "no take"
	}
	return *s
}

// TestProperty_RecordInvariants runs seeded random sequences of edits, Pick moves and lifecycle changes; after a final Settle
// Verify is empty and the changelog replays to the derived state. Verify is also clean mid-run (unconsumed events are
// excluded from R5). MM_PROPERTY_SEQS overrides the sequence count; a failure names its seed.
func TestProperty_RecordInvariants(t *testing.T) {
	t.Parallel()
	seqs := 60
	if v, err := strconv.Atoi(os.Getenv("MM_PROPERTY_SEQS")); err == nil && v > 0 {
		seqs = v
	}
	if testing.Short() {
		seqs = min(seqs, 12)
	}
	for seed := 1; seed <= seqs; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			w := &propWorld{t: t, r: r, rnd: rand.New(rand.NewSource(int64(seed))), takes: map[string][]string{}, retired: map[string]bool{}}
			for label, ts := range propSongs {
				id := r.song(label)
				w.songs = append(w.songs, id)
				for _, tl := range ts {
					w.takes[id] = append(w.takes[id], r.take(tl))
				}
			}
			w.songs = append(w.songs, r.song("S7"))
			w.draftSong()
			for i := 0; i < 60; i++ {
				w.step(i)
				if i%7 == 6 {
					w.noErrors(false)
				}
			}
			r.settle()
			w.noErrors(true)
			for _, id := range append(w.recs, r.fx.Records["R1"], r.fx.Records["R2"]) {
				w.replay(id)
			}
		})
	}
}
