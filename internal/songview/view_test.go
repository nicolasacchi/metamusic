package songview_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeLadder map[string]ladder.Strength

func (f fakeLadder) Strengths(_ context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	out := map[string]ladder.Strength{}
	for _, id := range ids {
		if s, ok := f[id]; ok {
			out[id] = s
		}
	}
	return out, 1, nil
}

type fakeFlags map[string][]string

func (f fakeFlags) FlagCodes(_ context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ids {
		if c, ok := f[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func view(t *testing.T, w *songstest.World, by, id string, take, events int) songview.View {
	t.Helper()
	v, err := w.View.Get(context.Background(), songstest.HumanP, id, songview.GetOptions{By: by, TakeLimit: take, IncludeEvents: events})
	if err != nil {
		t.Fatalf("Get(%s %s): %v", by, id, err)
	}
	return v
}

func problemOf(err error) *problem.Error {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

func TestSongView_Takes(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	child := l.PickedTakes[1] // pinned pick; the root is the older take
	v := view(t, l.World, "song", l.Picked, 0, 10)
	if v.Song.ID != l.Picked || v.Song.State != "picked" || v.TakesTotal != 2 || len(v.Takes) != 2 {
		t.Fatalf("song %+v takes %d/%d", v.Song, len(v.Takes), v.TakesTotal)
	}
	if v.Takes[0].ID != child || !v.Takes[0].IsPick || !v.Takes[0].Pinned || v.Takes[1].IsPick || v.Takes[1].Pinned {
		t.Errorf("the pick must come first: %+v", v.Takes)
	}
	for _, c := range v.Takes {
		if c.Ladder != nil || len(c.Flags) != 0 || c.Flags == nil {
			t.Errorf("nil collaborators: ladder %v flags %v (flags must be [] not null)", c.Ladder, c.Flags)
		}
	}
	if v.Pick == nil || v.Pick.TakeID == nil || *v.Pick.TakeID != child || v.Pick.Source != "pinned" || v.Pick.PinIneligible {
		t.Errorf("pick: %+v", v.Pick)
	}
	if len(v.Events) != 1 || v.Events[0].ToSource != "pinned" {
		t.Errorf("events: %+v", v.Events)
	}
	if v.Lineage == nil || v.Lineage.Generations != 2 || v.Lineage.ChildrenTotal != 1 || v.Lineage.RootTrackID != l.PickedTakes[0] {
		t.Errorf("lineage: %+v", v.Lineage)
	}
	if len(v.Song.RootTrackIDs) != 1 || v.Song.RootTrackID == nil {
		t.Errorf("roots: %+v", v.Song)
	}
	if v.Muse == nil || v.Muse.ID != l.MuseB {
		t.Errorf("muse: %+v", v.Muse)
	}
	if len(v.Sessions) != 1 || v.Sessions[0].Kind != "import" {
		t.Errorf("sessions: %+v", v.Sessions)
	}

	// take_limit keeps the pick and cuts the rest
	if v := view(t, l.World, "song", l.Picked, 1, 0); len(v.Takes) != 1 || v.Takes[0].ID != child || v.TakesTotal != 2 || len(v.Events) != 0 {
		t.Errorf("take_limit 1: %+v events %d", v.Takes, len(v.Events))
	}
}

func TestSongView_TakesWithCollaborators(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	child := l.PickedTakes[1]
	sv := songview.New(l.St, l.Muses, l.Songs, l.Sessions, l.Picks, nil, fakeLadder{child: {U: 1.5, SD: 0.5, NDuels: 4}}, fakeFlags{l.PickedTakes[0]: {"clipping", "mono"}}, nowFn(l))
	v, err := sv.Get(context.Background(), songstest.HumanP, l.Picked, songview.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]songview.TakeCard{}
	for _, c := range v.Takes {
		by[c.ID] = c
	}
	if c := by[child]; c.Ladder == nil || c.Ladder.U != 1.5 || c.Ladder.NDuels != 4 || len(c.Flags) != 0 {
		t.Errorf("pick card: %+v", c)
	}
	if c := by[l.PickedTakes[0]]; c.Ladder != nil || !slices.Equal(c.Flags, []string{"clipping", "mono"}) {
		t.Errorf("root card: %+v", c)
	}
}

func TestSongView_TakeCardFields(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	r := sc.Recipe("syn_cards_v1", "reggae, 76bpm")
	root := sc.Track(r, "Synthetic Root", "", sc.At(0))
	c1 := sc.Track(r, "Synthetic Child 1", root, sc.At(3)) // 24-char instant
	c2 := sc.Track(r, "Synthetic Child 2", root, sc.At(2)) // 20-char instant, older than c1
	sc.Rate(c1, 4)
	if _, err := w.St.W.Exec(`INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'agent',2,'agent','agent:a','a',?,?)`, c1, sc.At(3), sc.At(3)); err != nil {
		t.Fatal(err)
	}
	w.Settle()
	v := view(t, w, "track", c2, 0, 0)
	var ids []string
	for _, c := range v.Takes {
		ids = append(ids, c.ID)
	}
	if want := []string{c1, c2, root}; !slices.Equal(ids, want) {
		t.Fatalf("order (recorded_at desc as instants) = %v, want %v", ids, want)
	}
	card := v.Takes[0]
	if card.Stars == nil || *card.Stars != 4 || card.CriticStars == nil || *card.CriticStars != 2 || !card.RecipeKnown || card.Lossy || card.Generation != 1 ||
		card.ParentTrackID == nil || *card.ParentTrackID != root || card.Origin == "" || card.DurationS != 10 || card.CostDisplayUSD != nil {
		t.Errorf("card: %+v", card)
	}
	if v.Takes[0].RequestedBPM == nil || *v.Takes[0].RequestedBPM != 76 {
		t.Errorf("requested bpm from the style: %v", v.Takes[0].RequestedBPM)
	}
	if v.Pick != nil {
		t.Errorf("no pick row yet: %+v", v.Pick)
	}
}

func TestSongView_TempoRequestedVsMeasured(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	r := sc.Recipe("syn_tempo_v1", "reggae, male vocal, 76bpm")
	declared := w.TrackX(r, "Synthetic Declared", "", sc.At(0), false, 70)
	measured := sc.Track(r, "Synthetic Measured", declared, sc.At(2))
	if _, err := w.St.W.Exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'tempo', 1, 'A', '{"bpm":95.5,"confidence":0.9}', ?)`, measured, sc.At(3)); err != nil {
		t.Fatal(err)
	}
	w.Settle()
	v := view(t, w, "track", measured, 0, 0)
	by := map[string]songview.TakeCard{}
	for _, c := range v.Takes {
		by[c.ID] = c
	}
	if c := by[declared]; c.RequestedBPM == nil || *c.RequestedBPM != 70 || c.MeasuredBPM != nil {
		t.Errorf("declared bpm wins over the style: %+v %+v", c.RequestedBPM, c.MeasuredBPM)
	}
	if c := by[measured]; c.RequestedBPM == nil || *c.RequestedBPM != 76 || c.MeasuredBPM == nil || *c.MeasuredBPM != 95.5 {
		t.Errorf("requested 76 (style) vs measured 95.5: %+v %+v", c.RequestedBPM, c.MeasuredBPM)
	}
}

func TestSongView_PendingFlag(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	r := sc.Recipe("syn_pend_v1", "pop")
	root := sc.Track(r, "Synthetic Root", "", sc.At(0))
	// the key is queued, no Song yet: 404 with details.pending
	_, err := w.View.Get(context.Background(), songstest.HumanP, root, songview.GetOptions{By: "track"})
	if pe := problemOf(err); pe == nil || pe.Code != "NOT_FOUND" || pe.Status != 404 || pe.Details["pending"] != true {
		t.Fatalf("queued take: %v", err)
	}
	// an unknown id is a plain 404
	_, err = w.View.Get(context.Background(), songstest.HumanP, "nope", songview.GetOptions{By: "track"})
	if pe := problemOf(err); pe == nil || pe.Code != "NOT_FOUND" || pe.Details["pending"] != nil {
		t.Fatalf("unknown take: %v", err)
	}
	w.Settle()
	v := view(t, w, "track", root, 0, 0)
	if v.Pending || v.Song.Retired {
		t.Errorf("settled: pending %v", v.Pending)
	}
	sc.Track(r, "Synthetic Child", root, sc.At(2)) // a dirty take: the Song's picks and sounds lag
	v = view(t, w, "song", v.Song.ID, 0, 0)
	if !v.Pending {
		t.Error("a queued take must set pending")
	}
	w.Settle()
	if v = view(t, w, "song", v.Song.ID, 0, 0); v.Pending {
		t.Error("pending after Settle")
	}
}

func TestGetSong_ByDraftAndTrack(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	rd := sc.Recipe("syn_d_v1", "pop")
	backlog := sc.Draft("syn_d_v1", librarytest.DraftOpts{Recipe: rd})
	rf := sc.Recipe("syn_f_v1", "pop")
	fulfilled := sc.Draft("syn_f_v1", librarytest.DraftOpts{Recipe: rf})
	t1 := sc.Track(rf, "Synthetic Fulfilling", "", sc.At(2))
	sc.Fulfil(fulfilled, t1)
	child := sc.Track(rf, "Synthetic Child", t1, sc.At(4))
	// queued: 404 pending
	_, err := w.View.Get(context.Background(), songstest.HumanP, backlog, songview.GetOptions{By: "draft"})
	if pe := problemOf(err); pe == nil || pe.Code != "NOT_FOUND" || pe.Details["pending"] != true {
		t.Fatalf("queued draft: %v", err)
	}
	w.Settle()
	vb := view(t, w, "draft", backlog, 0, 0)
	if vb.Song.DraftID == nil || *vb.Song.DraftID != backlog || vb.Draft == nil || vb.Draft.Status != "backlog" || len(vb.Takes) != 0 || vb.Song.State != "draft" || vb.Lineage != nil {
		t.Errorf("backlog draft view: %+v draft %+v", vb.Song, vb.Draft)
	}
	vf := view(t, w, "draft", fulfilled, 0, 0)
	vc := view(t, w, "track", child, 0, 0)
	if vf.Song.ID != vc.Song.ID || vf.Song.ID != w.SongOfTrack(t1) || vf.TakesTotal != 2 {
		t.Errorf("fulfilled draft -> %s, child take -> %s", vf.Song.ID, vc.Song.ID)
	}
	if _, err := w.View.Get(context.Background(), songstest.HumanP, "nope", songview.GetOptions{}); problemOf(err) == nil || problemOf(err).Code != "NOT_FOUND" {
		t.Errorf("unknown song: %v", err)
	}
	if _, err := w.View.Get(context.Background(), songstest.HumanP, backlog, songview.GetOptions{By: "bogus"}); problemOf(err) == nil || problemOf(err).Status != 422 {
		t.Errorf("bad by: %v", err)
	}
}

func TestGetSong_RetiredAnswers200(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	d := l.Sc.Draft("syn_retired_v1", librarytest.DraftOpts{})
	id := l.NewID()
	if _, err := l.St.W.Exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,?,'Retired','draft','system:test',?,'system:test',?)`, id, d, l.Sc.At(0), l.Sc.At(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.St.W.Exec(`UPDATE songs SET retired_at=?, retired_into=? WHERE id=?`, l.Sc.At(1), l.Rendered, id); err != nil {
		t.Fatal(err)
	}
	v := view(t, l.World, "song", id, 0, 10)
	if !v.Song.Retired || v.Song.RetiredInto == nil || *v.Song.RetiredInto != l.Rendered || len(v.Takes) != 0 || v.Pick != nil || len(v.Events) != 0 || v.TakesTotal != 0 {
		t.Errorf("retired view: %+v", v)
	}
}

func TestMuses_SummaryCounts(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	ms, err := l.Muses.List(context.Background(), museListAll())
	if err != nil {
		t.Fatal(err)
	}
	var plain []musesMuse
	for _, m := range ms.Items {
		plain = append(plain, m.Muse)
	}
	sums, err := l.View.MuseSummaries(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != len(plain) {
		t.Fatalf("%d summaries for %d muses", len(sums), len(plain))
	}
	for _, s := range sums {
		q := func(state string) int {
			return l.Count(`SELECT COUNT(*) FROM songs s JOIN (`+songStateSQL+`) st ON st.song_id = s.id WHERE s.muse_id = ? AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0 AND `+songStateCase+` = ?`, s.Muse.ID, state)
		}
		want := songview.Coverage{
			Songs: q("draft") + q("rendered") + q("judged") + q("picked"), Draft: q("draft"), Rendered: q("rendered"), Judged: q("judged"), Picked: q("picked"),
			QueuedJobs: l.Count(`SELECT COALESCE(SUM(st.queued_jobs),0) FROM songs s JOIN (`+songStateSQL+`) st ON st.song_id = s.id WHERE s.muse_id = ? AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0`, s.Muse.ID),
		}
		if s.Coverage != want {
			t.Errorf("muse %s: %+v, want %+v", s.Muse.ID, s.Coverage, want)
		}
		if one, err := l.View.MuseCoverage(context.Background(), s.Muse.ID); err != nil || one != want {
			t.Errorf("MuseCoverage %s: %+v %v", s.Muse.ID, one, err)
		}
	}
	// the facts of the scene: Muse A has the rendered and the judged Song, Muse B the picked one
	if c, _ := l.View.MuseCoverage(context.Background(), l.MuseA); c.Songs != 2 || c.Rendered != 1 || c.Judged != 1 {
		t.Errorf("muse A: %+v", c)
	}
	if c, _ := l.View.MuseCoverage(context.Background(), l.MuseB); c.Songs != 1 || c.Picked != 1 {
		t.Errorf("muse B: %+v", c)
	}
	ang, err := l.View.MuseAngles(context.Background(), l.MuseA)
	if err != nil || len(ang) != 2 {
		t.Errorf("angles: %+v %v", ang, err)
	}
	if empty, _ := l.View.MuseCoverage(context.Background(), "nope"); empty != (songview.Coverage{}) {
		t.Errorf("unknown muse: %+v", empty)
	}
}

func TestContext_LibrarySections(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	sec, err := l.View.ForTrack(context.Background(), l.PickedTakes[0])
	if err != nil {
		t.Fatal(err)
	}
	if sec.Song == nil || sec.Muse == nil || sec.Sounds == nil {
		t.Fatalf("sections: %+v", sec)
	}
	if sec.Song["id"] != l.Picked || sec.Song["takes"] != 2 || sec.Song["state"] != "picked" {
		t.Errorf("song: %+v", sec.Song)
	}
	pk, _ := sec.Song["pick"].(map[string]any)
	if pk["source"] != "pinned" || pk["is_this"] != false {
		t.Errorf("pick: %+v", pk)
	}
	sib, _ := sec.Song["siblings"].([]map[string]any)
	if len(sib) != 1 || sib[0]["id"] != l.PickedTakes[1] || sib[0]["is_pick"] != true {
		t.Errorf("siblings (this take excluded): %+v", sib)
	}
	if sec.Muse["id"] != l.MuseB || sec.Muse["brief"] != nil {
		t.Errorf("muse: %+v", sec.Muse)
	}
	if fs, _ := sec.Sounds["facets"].(map[string]any); fs["genre_family"] == nil {
		t.Errorf("sounds: %+v", sec.Sounds)
	}
	// a take without a Song: three nils, not an error
	w := songstest.New(t)
	root := w.Sc.Track(w.Sc.Recipe("syn_x_v1", "pop"), "Synthetic Unsettled", "", w.Sc.At(0))
	sec, err = w.View.ForTrack(context.Background(), root)
	if err != nil || sec.Song != nil || sec.Muse != nil || sec.Sounds != nil {
		t.Errorf("unsettled take: %+v %v", sec, err)
	}
	_ = fmt.Sprint
}

func TestSongView_LossyAndReferenceCards(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	info := librarytest.FixtureL1(t, st, librarytest.FixedNow)
	w := songstest.Over(t, st)
	w.Settle()
	group := info.Groups[0]
	v := view(t, w, "track", group[0], 0, 0)
	if v.TakesTotal != len(group) {
		t.Fatalf("a lossy group is one Song of %d takes, got %d", len(group), v.TakesTotal)
	}
	for _, c := range v.Takes {
		if !c.Lossy || c.RecipeKnown {
			t.Errorf("reference lossy card: %+v", c)
		}
		if c.RequestedBPM != nil {
			t.Errorf("a reference take has no requested tempo: %v", c.RequestedBPM)
		}
	}
	if v.Muse != nil || v.Draft != nil {
		t.Errorf("reference group: muse %+v draft %+v", v.Muse, v.Draft)
	}
}
