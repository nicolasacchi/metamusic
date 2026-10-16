package picks_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/picks"
)

const sdUnit = 0.7071067811865476 // sqrt(1/2): two equal sds combine to 1, so z equals the strength difference

func TestPicks_RecomputeWritesEventOnlyOnChange(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2) // tk[1] is the newest
	ev := w.events(song)
	if len(ev) != 1 || ev[0].Reason != "initial" || ev[0].Seq != 1 || ev[0].ToSource != "newest" || *ev[0].ToTakeID != tk[1] {
		t.Fatalf("first events %+v", ev)
	}
	if ev[0].Actor != "system:provenance" || ev[0].ActorKind != "system" {
		t.Errorf("actor %+v", ev[0])
	}
	computed1 := w.str(`SELECT computed_at FROM song_picks WHERE song_id = ?`, song)

	// unchanged inputs: no event, computed_at refreshed
	w.min.Add(5)
	w.exec(`UPDATE tracks SET status = status`)
	w.settle()
	if len(w.events(song)) != 1 {
		t.Fatalf("an unchanged recompute wrote an event")
	}
	if c2 := w.str(`SELECT computed_at FROM song_picks WHERE song_id = ?`, song); c2 == computed1 {
		t.Errorf("computed_at not refreshed")
	}

	// a human 5 star on the older take
	w.sc.Rate(tk[0], 5)
	w.settle()
	ev = w.events(song)
	if len(ev) != 2 || ev[1].Reason != "stars" || ev[1].Seq != 2 || *ev[1].FromTakeID != tk[1] || *ev[1].ToTakeID != tk[0] || ev[1].FromSource != "newest" || ev[1].ToSource != "stars" {
		t.Fatalf("stars event %+v", ev)
	}

	// discard the picked take: the next one, by newest
	w.sc.Status(tk[0], "discarded")
	w.settle()
	ev = w.events(song)
	if len(ev) != 3 || ev[2].Reason != "newest" || *ev[2].ToTakeID != tk[1] {
		t.Fatalf("after discard %+v", ev)
	}
	// discard every take
	w.sc.Status(tk[1], "discarded")
	w.settle()
	ev = w.events(song)
	if len(ev) != 4 || ev[3].Reason != "emptied" || ev[3].ToSource != "none" || ev[3].ToTakeID != nil {
		t.Fatalf("emptied %+v", ev)
	}
	if v := w.pick(song); v.Source != picks.SourceNone || v.TakeID != nil {
		t.Errorf("view %+v", v)
	}
	// un-discard: an event from none, reason initial (C91.2)
	w.sc.Status(tk[1], "draft")
	w.settle()
	ev = w.events(song)
	if len(ev) != 5 || ev[4].Reason != "initial" || ev[4].FromSource != "none" {
		t.Fatalf("restored %+v", ev)
	}
}

func TestPickEvents_ReasonTable(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(3)
	if _, err := w.svc.Pin(context.Background(), human, song, &tk[0], nil); err != nil {
		t.Fatal(err)
	}
	w.sc.Status(tk[0], "discarded") // pin kept, skipped
	w.settle()
	w.sc.Status(tk[0], "draft") // restored
	w.settle()
	if _, err := w.svc.Pin(context.Background(), human, song, nil, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range w.events(song) {
		got = append(got, e.Reason)
	}
	want := []string{"initial", "pin", "pin_ineligible", "pin_restored", "unpin"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reasons %v, want %v", got, want)
	}
}

func TestPicks_RecomputeAfterVerdict(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2) // B = tk[1] is the newest and the first pick
	// critic verdicts never count
	for range 3 {
		w.agentVerdict(w.sc.Duel(tk[0], tk[1]))
	}
	w.lad.set(tk[0], 1.2, 0.5)
	w.lad.set(tk[1], 0.3, 0.6)
	w.settle()
	w.sweep()
	if v := w.pick(song); v.Source != picks.SourceNewest || *v.TakeID != tk[1] {
		t.Fatalf("critic verdicts changed the pick: %+v", v)
	}
	// three blind human verdicts between A and B
	w.verdicts(tk[0], tk[1], 3)
	w.settle()
	v := w.pick(song)
	if v.Source != picks.SourceLadder || *v.TakeID != tk[0] {
		t.Fatalf("pick %+v", v)
	}
	ev := w.events(song)
	last := ev[len(ev)-1]
	if last.Reason != "ladder" {
		t.Fatalf("reason %s", last.Reason)
	}
	e := evidenceOf(t, v.Evidence)
	leader := e["leader"].(map[string]any)
	if e["rule"] != "ladder" || e["duels_n"] != 3.0 || leader["id"] != tk[0] || leader["u"] != 1.2 || leader["sd"] != 0.5 || e["min_z"] != 0.5 || e["min_duels"] != 3.0 {
		t.Errorf("evidence %v", e)
	}
	if z := e["z"].(float64); z < 1.1 || z > 1.2 {
		t.Errorf("z %v", z)
	}
}

func TestPicks_SweepCatchesLadderDrift(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	w.verdicts(tk[0], tk[1], 3)
	w.lad.set(tk[0], 0.2, sdUnit)
	w.lad.set(tk[1], 0, sdUnit)
	w.settle()
	if v := w.pick(song); v.Source != picks.SourceNewest {
		t.Fatalf("margin 0.2 decided: %+v", v)
	}
	// the fit shifts with no queue key for this Song
	w.lad.set(tk[0], 0.6, sdUnit)
	if n := w.count(`SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Fatalf("queue not empty: %d", n)
	}
	ch := w.sweep()
	if ch.Events != 1 || len(ch.Songs) != 1 {
		t.Fatalf("sweep %+v", ch)
	}
	if v := w.pick(song); v.Source != picks.SourceLadder || *v.TakeID != tk[0] {
		t.Fatalf("pick %+v", v)
	}
}

func TestPicks_NoFlipFlopUnderDrift(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	w.verdicts(tk[0], tk[1], 3)
	w.lad.set(tk[1], 0, sdUnit)
	for _, z := range []float64{0.5, 0.4, 0.3, 0.5, 0.2} {
		w.lad.set(tk[0], z, sdUnit)
		w.sweep()
	}
	var got []string
	for _, e := range w.events(song) {
		got = append(got, e.Reason)
	}
	want := []string{"initial", "ladder", "newest"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v, want %v (one ladder event at 0.5, held through 0.3, one fallback at 0.2)", got, want)
	}
}

func TestPick_LadderStickiness(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	w.verdicts(tk[0], tk[1], 3)
	set := func(a, b float64) {
		w.lad.set(tk[0], a, sdUnit)
		w.lad.set(tk[1], b, sdUnit)
		w.sweep()
	}
	set(0, 1.0) // B leads clearly: B becomes the incumbent
	if v := w.pick(song); v.Source != picks.SourceLadder || *v.TakeID != tk[1] {
		t.Fatalf("incumbent %+v", v)
	}
	set(1.3, 1.0) // A leads but is only 0.3 clear of the incumbent: B holds
	v := w.pick(song)
	if *v.TakeID != tk[1] || v.Source != picks.SourceLadder {
		t.Fatalf("challenger displaced the incumbent: %+v", v)
	}
	if e := evidenceOf(t, v.Evidence); e["incumbent"].(map[string]any)["held"] != true {
		t.Errorf("evidence %v", e)
	}
	set(1.6, 1.0) // 0.6 clear: A displaces B
	if v := w.pick(song); *v.TakeID != tk[0] {
		t.Fatalf("clear challenger did not displace: %+v", v)
	}
	if n := len(w.events(song)); n != 3 { // initial newest, B ladder, A ladder
		t.Errorf("%d events", n)
	}
}

func TestPicks_TriggerEnqueue_Table(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, tk := w.song(2)
	cases := []struct {
		name string
		do   func()
	}{
		{"human rating insert", func() { w.sc.Rate(tk[0], 3) }},
		{"human rating update", func() { w.sc.Rate(tk[0], 4) }},
		{"status change", func() { w.sc.Status(tk[0], "to_relisten") }},
		{"human verdict", func() { w.sc.Verdict(w.sc.Duel(tk[0], tk[1]), "tie") }},
		{"tempo analysis", func() {
			w.exec(`INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo','mm-tempo/1 x','A','{}',?)`, tk[0], w.sc.At(2))
		}},
	}
	for _, c := range cases {
		w.exec(`DELETE FROM provenance_dirty`)
		c.do()
		if w.count(`SELECT COUNT(*) FROM provenance_dirty WHERE kind = 'track' AND id = ?`, tk[0]) != 1 {
			t.Errorf("%s did not mark the take", c.name)
		}
	}
	// a new take marks itself; an agent rating marks nothing
	w.exec(`DELETE FROM provenance_dirty`)
	w.exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'agent',5,'agent','agent:ag1','a',?,?)`, tk[0], w.sc.At(2), w.sc.At(2))
	if n := w.count(`SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("an agent rating marked %d keys", n)
	}
}

func TestPick_AgentRatingsNeverCount(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	w.exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'agent',5,'agent','agent:ag1','a',?,?)`, tk[0], w.sc.At(2), w.sc.At(2))
	w.exec(`UPDATE tracks SET status = status`)
	w.settle()
	if v := w.pick(song); v.Source != picks.SourceNewest || *v.TakeID != tk[1] {
		t.Fatalf("agent stars counted: %+v", v)
	}
}

func TestRatingIsolation_StillHolds(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, tk := w.song(1)
	// the schema still refuses an agent writing a human row, so a Pick can never be steered through the human source
	_, err := w.st.W.Exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'human',5,'agent','agent:ag1','a',?,?)`, tk[0], w.sc.At(2), w.sc.At(2))
	if err == nil {
		t.Fatal("an agent wrote a human rating")
	}
}

func TestProposals_ExpireOnDiscardOrPick(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(3) // pick: tk[2] newest
	p1, err := w.svc.Propose(ctx, agentA, song, tk[0], "older is better")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.svc.Propose(ctx, agentB, song, tk[1], "middle")
	if err != nil {
		t.Fatal(err)
	}
	w.sc.Status(tk[0], "discarded") // a proposed take is discarded
	w.settle()
	state := func(id string) string { return w.str(`SELECT state FROM pick_proposals WHERE id = ?`, id) }
	if state(p1.ID) != "expired" || w.str(`SELECT decided_by FROM pick_proposals WHERE id = ?`, p1.ID) != "system:provenance" {
		t.Errorf("p1 %s", state(p1.ID))
	}
	if state(p2.ID) != "open" {
		t.Errorf("p2 %s", state(p2.ID))
	}
	// the proposed take becomes the Pick (the newest is discarded)
	w.sc.Status(tk[2], "discarded")
	w.settle()
	if state(p2.ID) != "expired" {
		t.Errorf("p2 %s after becoming the pick", state(p2.ID))
	}
	_, err = w.svc.Pin(ctx, human, song, &tk[1], &p2.ID)
	wantProblem(t, err, 409, "PROPOSAL_CLOSED")
	// the 20-open cap counts only open proposals
	song2, tk2 := w.song(2)
	for i := range 25 {
		ag := agentA
		ag.ID = fmt.Sprintf("many%d", i)
		_, err := w.svc.Propose(ctx, ag, song2, tk2[0], "x")
		if i < 20 && err != nil {
			t.Fatalf("proposal %d: %v", i, err)
		}
		if i == 20 {
			wantProblem(t, err, 409, "TOO_MANY_PROPOSALS")
			w.sc.Status(tk2[0], "discarded") // expire them all, the cap frees up
			w.settle()
			w.sc.Status(tk2[0], "draft")
			w.settle()
			if n := w.count(`SELECT COUNT(*) FROM pick_proposals WHERE state = 'open' AND song_id = ?`, song2); n != 0 {
				t.Fatalf("%d open after expiry", n)
			}
		}
		if i > 20 && err != nil {
			t.Fatalf("proposal %d after expiry: %v", i, err)
		}
	}
}

// TestPicks_IntraDuelsMatchesLadderCounting: IntraDuels is the number of counted verdicts of the real duels fit.
func TestPicks_IntraDuelsMatchesLadderCounting(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	song, tk := w.song(2)
	ds := duels.New(duels.Options{Store: w.st, Library: library.New(w.st, nil, nil)})
	svc := picks.New(w.st, ds, w.svc2Now(), func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xf00000+w.n.Add(1)) })
	for i, verdict := range []string{"a", "b", "tie", "both_bad"} {
		d := w.sc.Duel(tk[0], tk[1])
		w.sc.Verdict(d, verdict)
		w.agentVerdict(d) // a critic verdict on a counted duel never counts
		if i%2 == 0 {
			w.sc.Verdict(w.sc.Duel(tk[1], tk[0]), verdict) // swapped slots count like any
		}
	}
	sighted := fmt.Sprintf("sighted-%d", w.n.Add(1))
	w.exec(`INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES(?,?,?,0,0,0,1000,'mm-clip/1','ffmpeg n9','off',0,0,'human:h','human',?,?)`, sighted, tk[0], tk[1], w.sc.At(1), w.sc.At(2))
	w.sc.Verdict(sighted, "a")
	// recompute through the service wired to the real duels ladder
	tx, err := w.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := ds.Strengths(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecomputeTx(context.Background(), tx, w.envFor(), []string{song}, st); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	e := evidenceOf(t, w.pick(song).Evidence)
	var n float64
	if v, ok := e["duels_n"]; ok {
		n = v.(float64)
	} else {
		n = e["ladder_skipped"].(map[string]any)["duels_n"].(float64)
	}
	if int(n) != st[tk[0]].NDuels || int(n) != st[tk[1]].NDuels || n != 6 {
		t.Fatalf("IntraDuels %v, fit n_duels %d / %d (want 6)", n, st[tk[0]].NDuels, st[tk[1]].NDuels)
	}
}
