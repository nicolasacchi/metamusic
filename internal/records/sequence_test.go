package records_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// shuffled is the input order of the T-set Record: t4, t6, t1, t5, t3, t2 (C137.6).
var shuffled = []string{"S4", "S6", "S1", "S5", "S3", "S2"}

func (r *rig) tRecord(p auth.Principal) records.Record {
	r.t.Helper()
	r.seedTSet()
	return r.newRecord(p, shuffled...)
}

// seqRow is one record_sequences row as the tests read it.
type seqRow struct {
	ID, Strategy, Before, After, Digest string
	Revision                            int
	CostIn, CostOut                     int64
	Exact                               bool
	Doc                                 string
}

func (r *rig) sequences(recordID string) []seqRow {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT id, strategy, order_before_json, order_after_json, digest, record_revision, cost_input, cost_output, exact, explanation_json
		FROM record_sequences WHERE record_id = ? ORDER BY at, id`, recordID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []seqRow
	for rows.Next() {
		var s seqRow
		if err := rows.Scan(&s.ID, &s.Strategy, &s.Before, &s.After, &s.Digest, &s.Revision, &s.CostIn, &s.CostOut, &s.Exact, &s.Doc); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// songsOf maps slot ids to their Song labels in the order given.
func (r *rig) labelsOf(recordID string) []string {
	r.t.Helper()
	byID := map[string]string{}
	for l, id := range r.fx.Songs {
		byID[id] = l
	}
	var out []string
	for _, s := range r.slots(recordID) {
		out = append(out, byID[s.SongID])
	}
	return out
}

func seqOp(strategy string) records.SlotOp { return records.SlotOp{Op: "sequence", Strategy: strategy} }

func editWith(t *testing.T, r *rig, p auth.Principal, rec records.Record, ops ...records.SlotOp) (records.EditResult, error) {
	t.Helper()
	return r.svc.EditSlots(context.Background(), p, rec.ID, rec.Revision, ops)
}

func TestEditSlots_Sequence_Arc(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	before := r.slotIDs(rec.ID)
	res, err := editWith(t, r, agent, rec, seqOp("arc"))
	if err != nil {
		t.Fatal(err)
	}
	same(t, "order", r.labelsOf(rec.ID), []string{"S1", "S2", "S3", "S4", "S5", "S6"})
	// shuffled t4 t6 t1 t5 t3 t2 -> t1..t6: t3 and t4 swap neighbours and so on; only moved slots get a row
	moved := 0
	for _, c := range res.Changes {
		if c.Kind != "slot_moved" {
			t.Errorf("unexpected row %s", c.Kind)
			continue
		}
		moved++
		var d map[string]any
		if err := json.Unmarshal(c.Detail, &d); err != nil {
			t.Fatal(err)
		}
		if d["by"] != "sequencer" || d["strategy"] != "arc" || d["sequence_id"] == nil || d["from"] == nil || len(d) != 4 {
			t.Errorf("detail = %s", c.Detail)
		}
		if got := string(c.Detail); !strings.HasPrefix(got, `{"from":`) {
			t.Errorf("detail key order = %s", got)
		}
	}
	// positions before: S4=1 S6=2 S1=3 S5=4 S3=5 S2=6; after S1..S6 = 1..6: every slot moves except none
	if moved != 6 {
		t.Errorf("%d slot_moved rows, want 6", moved)
	}
	seqs := r.sequences(rec.ID)
	if len(seqs) != 1 {
		t.Fatalf("%d record_sequences rows", len(seqs))
	}
	s := seqs[0]
	if s.CostIn != 89847130000 || s.CostOut != 55198370000 || !s.Exact || s.Revision != 1 || s.Strategy != "arc" {
		t.Errorf("row = %+v", s)
	}
	var b, a []string
	_ = json.Unmarshal([]byte(s.Before), &b)
	_ = json.Unmarshal([]byte(s.After), &a)
	same(t, "order_before", b, before)
	same(t, "order_after", a, r.slotIDs(rec.ID))
	if res.Record.Revision != 2 || len(res.Sequences) != 1 || res.Sequences[0].ID != s.ID || !res.Sequences[0].Changed {
		t.Errorf("result = rev %d %+v", res.Record.Revision, res.Sequences)
	}
	for _, c := range res.Changes {
		var d struct {
			SequenceID string `json:"sequence_id"`
		}
		_ = json.Unmarshal(c.Detail, &d)
		if d.SequenceID != s.ID {
			t.Errorf("sequence_id = %s, want %s", d.SequenceID, s.ID)
		}
	}
	// the second identical call: a run is stored, no row, no revision bump
	rev := res.Record.Revision
	res2, err := r.svc.EditSlots(context.Background(), agent, rec.ID, rev, []records.SlotOp{seqOp("")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Changes) != 0 || res2.Record.Revision != rev || len(r.sequences(rec.ID)) != 2 || res2.Sequences[0].Changed {
		t.Errorf("second call: %d rows, rev %d, %d runs", len(res2.Changes), res2.Record.Revision, len(r.sequences(rec.ID)))
	}
	if s2 := r.sequences(rec.ID)[1]; s2.Revision != rev || s2.CostIn != s.CostOut || s2.CostOut != s.CostOut {
		t.Errorf("second run = %+v", s2)
	}
}

func TestEditSlots_Sequence_AfterReorderUsesWorkingList(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	ids := r.slotIDs(rec.ID)
	rev := []string{ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}
	res, err := editWith(t, r, agent, rec, records.SlotOp{Op: "reorder", SlotIDs: rev}, seqOp("arc"))
	if err != nil {
		t.Fatal(err)
	}
	same(t, "order", r.labelsOf(rec.ID), []string{"S1", "S2", "S3", "S4", "S5", "S6"})
	var b []string
	_ = json.Unmarshal([]byte(r.sequences(rec.ID)[0].Before), &b)
	same(t, "the sequence started from the reordered list", b, rev)
	// the from of every sequencer row is relative to the list after the reorder
	for _, c := range res.Changes {
		var d struct {
			From int    `json:"from"`
			By   string `json:"by"`
		}
		_ = json.Unmarshal(c.Detail, &d)
		if d.By != "sequencer" {
			continue
		}
		if want := indexOf(rev, *c.SlotID) + 1; d.From != want {
			t.Errorf("slot %s from = %d, want %d", *c.SlotID, d.From, want)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestEditSlots_Sequence_Endpoints(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	if _, err := editWith(t, r, agent, rec, records.SlotOp{Op: "sequence", Strategy: "arc", FirstSongID: r.song("S5"), LastSongID: r.song("S1")}); err != nil {
		t.Fatal(err)
	}
	got := r.labelsOf(rec.ID)
	if got[0] != "S5" || got[5] != "S1" {
		t.Errorf("order = %v", got)
	}
	var first, last *string
	if err := r.st.R.QueryRow(`SELECT first_song_id, last_song_id FROM record_sequences WHERE record_id = ?`, rec.ID).Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	if first == nil || *first != r.song("S5") || last == nil || *last != r.song("S1") {
		t.Errorf("stored endpoints = %v %v", first, last)
	}
}

func TestEditSlots_Sequence_Errors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		setup  func(r *rig) records.Record
		op     func(r *rig) records.SlotOp
		status int
		code   string
	}{
		{"no features", func(r *rig) records.Record { return r.newRecord(agent, "S1", "S2", "S3") },
			func(*rig) records.SlotOp { return seqOp("arc") }, 422, "SEQUENCE_NO_FEATURES"},
		{"too small", func(r *rig) records.Record { r.seedTSet(); return r.newRecord(agent, "S1") },
			func(*rig) records.SlotOp { return seqOp("arc") }, 422, "SEQUENCE_TOO_SMALL"},
		{"unknown strategy", func(r *rig) records.Record { return r.tRecord(agent) },
			func(*rig) records.SlotOp { return seqOp("shuffle") }, 422, "UNKNOWN_STRATEGY"},
		{"endpoint not in the set", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp { return records.SlotOp{Op: "sequence", FirstSongID: r.song("S7")} }, 422, "SEQUENCE_ENDPOINT_NOT_IN_SET"},
		{"endpoints conflict", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp {
				return records.SlotOp{Op: "sequence", FirstSongID: r.song("S1"), LastSongID: r.song("S1")}
			}, 422, "SEQUENCE_ENDPOINTS_CONFLICT"},
		{"slot id is forbidden", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp { return records.SlotOp{Op: "sequence", SlotID: "x"} }, 422, "VALIDATION_FAILED"},
		{"position is forbidden", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp { return records.SlotOp{Op: "sequence", Position: 2} }, 422, "VALIDATION_FAILED"},
		{"side is forbidden", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp {
				return records.SlotOp{Op: "sequence", Side: records.OptString{Set: true, Value: "A"}}
			}, 422, "VALIDATION_FAILED"},
		{"strategy on another op is forbidden", func(r *rig) records.Record { return r.tRecord(agent) },
			func(r *rig) records.SlotOp { return records.SlotOp{Op: "pin_all", Strategy: "arc"} }, 422, "VALIDATION_FAILED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			rec := c.setup(r)
			order, rows := r.slotIDs(rec.ID), len(r.changes(rec.ID))
			_, err := editWith(t, r, agent, rec, c.op(r))
			pe := wantProblem(t, err, c.status, c.code)
			if pe.Details["op"] != 0 {
				t.Errorf("details.op = %v", pe.Details["op"])
			}
			same(t, "order", r.slotIDs(rec.ID), order)
			if len(r.changes(rec.ID)) != rows || len(r.sequences(rec.ID)) != 0 || r.get(rec.ID).Revision != rec.Revision {
				t.Error("a refused sequence wrote something")
			}
		})
	}
}

// TestEditSlots_Sequence_PinnedSlot: a pinned slot is sequenced by its pinned Take's features and keeps its pin and side.
func TestEditSlots_Sequence_PinnedSlot(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	s4 := r.fx.Takes["c2"]
	if r.pickOf(r.song("S4")) == s4 {
		t.Skip("c2 is the Pick of S4")
	}
	// the pinned take of S4 sounds like the quietest opener: the plan must put S4 first
	r.seedTake(s4, feat{9000, 500, "8A"})
	slotS4 := r.slots(rec.ID)[0].ID
	if _, err := editWith(t, r, agent, rec, records.SlotOp{Op: "pin", SlotID: slotS4, TakeID: s4},
		records.SlotOp{Op: "set_side", SlotID: slotS4, Side: records.OptString{Set: true, Value: "B"}}); err != nil {
		t.Fatal(err)
	}
	rec = r.get(rec.ID)
	if _, err := editWith(t, r, agent, rec, seqOp("arc")); err != nil {
		t.Fatal(err)
	}
	sl := r.slots(rec.ID)
	if sl[0].ID != slotS4 || sl[0].PinnedTakeID == nil || *sl[0].PinnedTakeID != s4 || sl[0].Side == nil || *sl[0].Side != "B" {
		t.Errorf("first slot = %+v", sl[0])
	}
	var doc struct {
		Steps []struct {
			SongID string  `json:"song_id"`
			TakeID *string `json:"take_id"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(r.sequences(rec.ID)[0].Doc), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Steps[0].SongID != r.song("S4") || doc.Steps[0].TakeID == nil || *doc.Steps[0].TakeID != s4 {
		t.Errorf("first step = %+v", doc.Steps[0])
	}
}

// TestEditSlots_Sequence_Principals: a published Record is sequenced by a human or a device and refused for an agent; a
// proposed one an agent sequences returns to draft.
func TestEditSlots_Sequence_Principals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(human)
	ctx := context.Background()
	pub, err := r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.EditSlots(ctx, agent, rec.ID, pub.Revision, []records.SlotOp{seqOp("arc")})
	wantProblem(t, err, 403, "HUMAN_ONLY")
	if len(r.sequences(rec.ID)) != 0 {
		t.Error("a refused sequence stored a run")
	}
	if _, err := r.svc.EditSlots(ctx, device, rec.ID, pub.Revision, []records.SlotOp{seqOp("arc")}); err != nil {
		t.Errorf("device: %v", err)
	}

	r2 := newRig(t)
	rec2 := r2.tRecord(agent)
	prop, err := r2.svc.Propose(ctx, agent, rec2.ID, rec2.Revision, "ready")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r2.svc.EditSlots(ctx, agent, rec2.ID, prop.Revision, []records.SlotOp{seqOp("arc")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Status != records.StatusDraft {
		t.Errorf("status = %s, want draft", res.Record.Status)
	}
	if k := kinds(res.Changes); k[len(k)-1] != "proposal_dismissed" {
		t.Errorf("rows = %v", k)
	}
}

// TestEditSlots_SequenceCatchesUpFirst: a pending Pick event is logged before the slot_moved rows of the sequence.
func TestEditSlots_SequenceCatchesUpFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	other := r.take("b2")
	if r.pickOf(r.song("S3")) == other {
		other = r.take("b1")
	}
	r.seedTake(other, tSetFeat["S3"])
	r.setPick(r.song("S3"), other, "ladder", "ladder", "")
	if _, err := editWith(t, r, agent, rec, seqOp("arc")); err != nil {
		t.Fatal(err)
	}
	k := kinds(r.changes(rec.ID))
	k = k[1:] // the "created" row
	for len(k) > 0 && k[0] == "slot_added" {
		k = k[1:]
	}
	if len(k) < 2 || k[0] != "slot_upgraded" {
		t.Fatalf("rows = %v, want the catch-up row before the sequence rows", k)
	}
	for _, kind := range k[1:] {
		if kind != "slot_moved" {
			t.Errorf("rows = %v", k)
		}
	}
}

// TestSequence_NoMuseText: no Muse text reaches the stored runs or the changelog rows.
func TestSequence_NoMuseText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	res, err := editWith(t, r, agent, rec, seqOp("arc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.ProposeWith(context.Background(), agent, rec.ID, res.Record.Revision, "ready", &records.SeqSpec{Strategy: "steady"}); err != nil {
		t.Fatal(err)
	}
	var blob strings.Builder
	for _, s := range r.sequences(rec.ID) {
		blob.WriteString(s.Doc + s.Before + s.After)
	}
	for _, c := range r.changes(rec.ID) {
		blob.Write(c.Detail)
	}
	for _, canary := range []string{"Synthetic Curation Muse", "Synthetic Curation Artist"} {
		if strings.Contains(blob.String(), canary) {
			t.Errorf("Muse text %q in stored rows", canary)
		}
	}
}
