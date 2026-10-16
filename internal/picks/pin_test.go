package picks_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/picks"
)

func TestPin_Rules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(2)
	other, otk := w.song(1)

	res, err := w.svc.Pin(ctx, human, song, &tk[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Pick.Source != picks.SourcePinned || *res.Pick.TakeID != tk[0] || *res.Pick.PinnedBy != "human:op" || *res.Pick.PinnedByKind != "human" || *res.Pick.PinnedByName != "op" {
		t.Fatalf("pin %+v", res)
	}
	if e := w.events(song); e[len(e)-1].Reason != "pin" || e[len(e)-1].Actor != "human:op" || e[len(e)-1].ActorKind != "human" {
		t.Fatalf("event %+v", e[len(e)-1])
	}
	if ev := evidenceOf(t, res.Pick.Evidence); ev["rule"] != "pinned" || ev["pinned_by"] != "human:op" || ev["pinned_at"] == nil {
		t.Errorf("evidence %v", ev)
	}
	// the same take again: unchanged
	res, err = w.svc.Pin(ctx, human, song, &tk[0], nil)
	if err != nil || res.Changed {
		t.Fatalf("repeat pin: %v %+v", err, res)
	}
	n := len(w.events(song))
	// a take of another Song
	_, err = w.svc.Pin(ctx, human, song, &otk[0], nil)
	wantProblem(t, err, 422, "TAKE_NOT_IN_SONG")
	// a discarded take
	w.sc.Status(tk[1], "discarded")
	_, err = w.svc.Pin(ctx, human, song, &tk[1], nil)
	wantProblem(t, err, 422, "TAKE_NOT_ELIGIBLE")
	w.sc.Status(tk[1], "draft")
	if len(w.events(song)) != n {
		t.Errorf("failed pins wrote events")
	}
	// unpin resumes the derivation
	res, err = w.svc.Pin(ctx, human, song, nil, nil)
	if err != nil || !res.Changed || res.Pick.Source != picks.SourceNewest || res.Pick.PinnedTakeID != nil || res.Pick.PinnedBy != nil {
		t.Fatalf("unpin %v %+v", err, res)
	}
	if e := w.events(song); e[len(e)-1].Reason != "unpin" {
		t.Errorf("event %+v", e[len(e)-1])
	}
	// unpin with no pin
	n = len(w.events(song))
	res, err = w.svc.Pin(ctx, human, song, nil, nil)
	if err != nil || res.Changed || len(w.events(song)) != n {
		t.Fatalf("second unpin %v %+v", err, res)
	}
	// a device may pin
	if res, err = w.svc.Pin(ctx, device, song, &tk[0], nil); err != nil || *res.Pick.PinnedByKind != "device" {
		t.Fatalf("device pin %v %+v", err, res)
	}
	// unknown and retired Songs
	_, err = w.svc.Pin(ctx, human, "no-such-song", &tk[0], nil)
	wantProblem(t, err, 404, "NOT_FOUND")
	_ = other
}

func TestPin_NoRowCreatesThroughPinEvent(t *testing.T) {
	t.Parallel()
	w := newWorld(t, withoutPicksStep)
	song, tk := w.song(2)
	if w.count(`SELECT COUNT(*) FROM song_picks WHERE song_id = ?`, song) != 0 {
		t.Fatal("the Reconciler wrote a pick without the step")
	}
	if v := w.pick(song); v.Source != picks.SourceNone || v.TakeID != nil || v.Seq != 0 {
		t.Fatalf("empty view %+v", v)
	}
	res, err := w.svc.Pin(context.Background(), human, song, &tk[0], nil)
	if err != nil || !res.Changed || res.Pick.Source != picks.SourcePinned || res.Pick.Seq != 1 {
		t.Fatalf("pin %v %+v", err, res)
	}
	if e := w.events(song); len(e) != 1 || e[0].Reason != "pin" || e[0].FromSource != "none" {
		t.Fatalf("events %+v", e)
	}
}

func TestPin_AgentRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	before := len(w.events(song))
	_, err := w.svc.Pin(context.Background(), agentA, song, &tk[0], nil)
	wantProblem(t, err, 403, "HUMAN_ONLY")
	_, err = w.svc.Dismiss(context.Background(), agentA, "x")
	wantProblem(t, err, 403, "HUMAN_ONLY")
	if len(w.events(song)) != before || w.pick(song).PinnedTakeID != nil {
		t.Errorf("state changed")
	}
}

func TestPick_PinIneligibleThenRestored(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(2)
	if _, err := w.svc.Pin(ctx, human, song, &tk[0], nil); err != nil {
		t.Fatal(err)
	}
	w.sc.Status(tk[0], "discarded")
	w.settle()
	v := w.pick(song)
	if v.Source != picks.SourceNewest || *v.TakeID != tk[1] || !v.PinIneligible || *v.PinnedTakeID != tk[0] || v.PinnedBy == nil {
		t.Fatalf("while ineligible %+v", v)
	}
	if e := evidenceOf(t, v.Evidence); e["pin_ineligible"].(map[string]any)["take_id"] != tk[0] {
		t.Errorf("evidence %v", e)
	}
	w.sc.Status(tk[0], "draft")
	w.settle()
	v = w.pick(song)
	if v.Source != picks.SourcePinned || *v.TakeID != tk[0] || v.PinIneligible {
		t.Fatalf("restored %+v", v)
	}
	ev := w.events(song)
	var reasons []string
	for _, e := range ev {
		reasons = append(reasons, e.Reason)
	}
	if !slices.Equal(reasons, []string{"initial", "pin", "pin_ineligible", "pin_restored"}) {
		t.Fatalf("reasons %v", reasons)
	}
	if ev[2].Actor != "system:provenance" || ev[3].Actor != "system:provenance" {
		t.Errorf("system events carry %s / %s", ev[2].Actor, ev[3].Actor)
	}
	if _, err := w.svc.Pin(ctx, human, song, nil, nil); err != nil {
		t.Fatal(err)
	}
	if e := w.events(song); e[len(e)-1].Reason != "unpin" {
		t.Errorf("last %+v", e[len(e)-1])
	}
}

func TestPropose_Rules(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(2) // pick = tk[1]
	other, otk := w.song(1)
	_ = other
	_, err := w.svc.Propose(ctx, human, song, tk[0], "why")
	wantProblem(t, err, 403, "HUMAN_ONLY")
	_, err = w.svc.Propose(ctx, agentA, song, tk[1], "already")
	wantProblem(t, err, 409, "ALREADY_PICK")
	_, err = w.svc.Propose(ctx, agentA, song, otk[0], "elsewhere")
	wantProblem(t, err, 422, "TAKE_NOT_IN_SONG")
	_, err = w.svc.Propose(ctx, agentA, song, tk[0], "   ")
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	p1, err := w.svc.Propose(ctx, agentA, song, tk[0], " older ")
	if err != nil || p1.State != "open" || p1.Rationale != "older" || p1.PrincipalID != "agent:ag1" || p1.DecidedAt != nil {
		t.Fatalf("propose %v %+v", err, p1)
	}
	// a re-proposal supersedes
	p2, err := w.svc.Propose(ctx, agentA, song, tk[0], "again")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT state FROM pick_proposals WHERE id = ?`, p1.ID); got != "superseded" || w.str(`SELECT decided_by FROM pick_proposals WHERE id = ?`, p1.ID) != "agent:ag1" {
		t.Errorf("p1 %s", got)
	}
	if v := w.pick(song); v.OpenProposals != 1 {
		t.Errorf("open_proposals %d", v.OpenProposals)
	}
	_ = p2
	// the 21st open proposal
	for i := range 19 {
		ag := agentA
		ag.ID = fmt.Sprintf("f%d", i)
		if _, err := w.svc.Propose(ctx, ag, song, tk[0], "x"); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	_, err = w.svc.Propose(ctx, agentB, song, tk[0], "too many")
	wantProblem(t, err, 409, "TOO_MANY_PROPOSALS")
	// replacing an existing open proposal at the cap is fine
	if _, err := w.svc.Propose(ctx, agentA, song, tk[0], "replace"); err != nil {
		t.Fatalf("replace at the cap: %v", err)
	}
}

func TestProposals_Lifecycle(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(3)
	p, err := w.svc.Propose(ctx, agentA, song, tk[0], "pick the oldest")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Pin(ctx, human, song, &tk[1], &p.ID)
	wantProblem(t, err, 409, "PROPOSAL_MISMATCH")
	res, err := w.svc.Pin(ctx, human, song, &tk[0], &p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal == nil || res.Proposal.State != "accepted" || *res.Proposal.DecidedBy != "human:op" || res.Proposal.DecidedAt == nil {
		t.Fatalf("proposal %+v", res.Proposal)
	}
	if e := evidenceOf(t, res.Pick.Evidence); e["proposal_id"] != p.ID {
		t.Errorf("evidence %v", e)
	}
	// the proposal id survives an evidence refresh
	w.exec(`UPDATE tracks SET status = status`)
	w.settle()
	if e := evidenceOf(t, w.pick(song).Evidence); e["proposal_id"] != p.ID {
		t.Errorf("evidence after refresh %v", e)
	}
	_, err = w.svc.Pin(ctx, human, song, &tk[0], &p.ID)
	wantProblem(t, err, 409, "PROPOSAL_CLOSED")
	// dismiss
	p2, err := w.svc.Propose(ctx, agentB, song, tk[1], "middle")
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.svc.Dismiss(ctx, human, p2.ID)
	if err != nil || d.State != "dismissed" || *d.DecidedBy != "human:op" {
		t.Fatalf("dismiss %v %+v", err, d)
	}
	_, err = w.svc.Dismiss(ctx, human, p2.ID)
	wantProblem(t, err, 409, "PROPOSAL_CLOSED")
	_, err = w.svc.Dismiss(ctx, human, "nope")
	wantProblem(t, err, 404, "NOT_FOUND")
}

func TestDismiss_HumanOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(2)
	p, err := w.svc.Propose(ctx, agentA, song, tk[0], "x")
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Dismiss(ctx, agentB, p.ID)
	wantProblem(t, err, 403, "HUMAN_ONLY")
	if got := w.str(`SELECT state FROM pick_proposals WHERE id = ?`, p.ID); got != "open" {
		t.Errorf("state %s", got)
	}
	if _, err := w.svc.Dismiss(ctx, device, p.ID); err != nil {
		t.Errorf("device dismiss: %v", err)
	}
}

func TestEvents_Feed(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	s1, t1 := w.song(2)
	s2, t2 := w.song(2)
	_, _ = w.svc.Pin(ctx, human, s1, &t1[0], nil)
	_, _ = w.svc.Pin(ctx, human, s2, &t2[0], nil)
	all, err := w.svc.Events(ctx, picks.EventFilter{})
	if err != nil || len(all.Items) != 4 || all.NextCursor != nil {
		t.Fatalf("all %v %+v", err, all)
	}
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i].Cursor <= all.Items[i-1].Cursor {
			t.Fatalf("not ascending: %+v", all.Items)
		}
	}
	p1, _ := w.svc.Events(ctx, picks.EventFilter{Limit: 3})
	if len(p1.Items) != 3 || p1.NextCursor == nil || *p1.NextCursor != p1.Items[2].Cursor {
		t.Fatalf("page 1 %+v", p1)
	}
	p2, _ := w.svc.Events(ctx, picks.EventFilter{Limit: 3, After: *p1.NextCursor})
	if len(p2.Items) != 1 || p2.NextCursor != nil || p2.Items[0].ID != all.Items[3].ID {
		t.Fatalf("page 2 %+v", p2)
	}
	one, _ := w.svc.Events(ctx, picks.EventFilter{SongID: s1})
	if len(one.Items) != 2 {
		t.Errorf("song_id filter: %d", len(one.Items))
	}
	both, _ := w.svc.Events(ctx, picks.EventFilter{SongIDs: []string{s1, s2}})
	if len(both.Items) != 4 {
		t.Errorf("song_ids filter: %d", len(both.Items))
	}
	union, _ := w.svc.Events(ctx, picks.EventFilter{SongID: s1, SongIDs: []string{s2}})
	if len(union.Items) != 4 {
		t.Errorf("unioned filter: %d", len(union.Items))
	}
	big := make([]string, 201)
	for i := range big {
		big[i] = fmt.Sprint(i)
	}
	_, err = w.svc.Events(ctx, picks.EventFilter{SongIDs: big})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	if one.Items[1].Evidence == nil || one.Items[0].ActorName != "provenance" {
		t.Errorf("items %+v", one.Items)
	}
}

// TestEvents_CursorMonotonic: ids are identity only; the integer cursor pages strictly in insertion order even when
// the UUIDv7 ids of later events sort before earlier ones.
func TestEvents_CursorMonotonic(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.descending.Store(true)
	for range 7 {
		w.song(1)
	}
	ctx := context.Background()
	var ids []string
	var seen []int64
	cursor := int64(0)
	for {
		page, err := w.svc.Events(ctx, picks.EventFilter{Limit: 2, After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			seen = append(seen, e.Cursor)
			ids = append(ids, e.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if total := w.count(`SELECT COUNT(*) FROM pick_events`); len(seen) != total || total != 7 {
		t.Fatalf("paged %d of %d", len(seen), total)
	}
	if !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != len(seen) {
		t.Fatalf("cursors %v", seen)
	}
	if slices.IsSorted(ids) {
		t.Fatalf("ids were not out of order: %v", ids)
	}
}
