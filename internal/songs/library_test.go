package songs_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
)

var (
	humanP = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}
	agentP = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }

func problemOf(err error) (code string, status int, details map[string]any) {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Status, pe.Details
	}
	return "", 0, nil
}

type lib struct{ *songstest.Lib }

func newLib(t *testing.T) *lib { t.Helper(); return &lib{songstest.NewLib(t)} }

func (l *lib) newMuse(title, artist string) (string, error) { return l.NewMuse(title, artist) }

func (l *lib) ids(t *testing.T, f songs.ListFilter) []string {
	t.Helper()
	f.Limit = 100
	p, err := l.Songs.List(context.Background(), f)
	if err != nil {
		t.Fatalf("%+v: %v", f, err)
	}
	if p.Total != len(p.Items) {
		t.Errorf("%+v: total %d, items %d", f, p.Total, len(p.Items))
	}
	var out []string
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	slices.Sort(out)
	return out
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

func TestSongs_StateTable(t *testing.T) {
	t.Parallel()
	l := newLib(t)
	p, err := l.Songs.List(context.Background(), songs.ListFilter{Limit: 100, IncludeHidden: true, IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]songs.Summary{}
	for _, it := range p.Items {
		got[it.ID] = it
	}
	rows := []struct {
		name  string
		song  string
		state songs.State
		takes int
		queue int
		props int
		pick  string
		arch  bool
	}{
		{"one take => rendered", l.Rendered, songs.StateRendered, 1, 0, 0, "", false},
		{"human star => judged", l.Judged, songs.StateJudged, 1, 0, 0, "", false},
		{"pinned pick => picked", l.Picked, songs.StatePicked, 2, 0, 0, "pinned", false},
		{"stars pick => judged, not picked", l.StarsPick, songs.StateJudged, 2, 0, 0, "stars", false},
		{"backlog draft => draft", l.Draft, songs.StateDraft, 0, 0, 0, "", false},
		{"archived draft => archived", l.Arch, songs.StateDraft, 0, 0, 0, "", true},
		{"a live job keeps the base state", l.Queued, songs.StateRendered, 1, 1, 0, "", false},
		{"open proposal counted", l.Proposed, songs.StateRendered, 1, 0, 1, "", false},
	}
	for _, r := range rows {
		s, ok := got[r.song]
		if !ok {
			t.Errorf("%s: missing", r.name)
			continue
		}
		pick := ""
		if s.Pick != nil {
			pick = s.Pick.Source
		}
		if s.State != r.state || s.Takes != r.takes || s.QueuedJobs != r.queue || s.OpenProposals != r.props || pick != r.pick || s.Archived != r.arch {
			t.Errorf("%s: %+v", r.name, s)
		}
	}
	if !got[l.Hidden].Hidden {
		t.Error("hidden flag")
	}
}

func TestSongs_StateTable_LiveJobs(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	track, song := w.RootSong("Synthetic Jobs", 0)
	b := w.Sc.Batch(`[{"item_index":0}]`, 8, "active")
	path := []string{"queued", "submitting", "starting", "generating", "transferring", "ingesting"}
	for i := range path {
		j := w.Sc.Job(b, i, "", track, w.Sc.Recipe(fmt.Sprintf("syn_j%d", i), "pop"))
		for _, step := range path[1 : i+1] {
			if _, err := w.St.W.Exec(`UPDATE jobs SET state = ? WHERE id = ?`, step, j); err != nil {
				t.Fatalf("job %d -> %s: %v", i, step, err)
			}
		}
	}
	// a terminal job and a job of an unconfirmed batch do not count
	j := w.Sc.Job(b, 6, "", track, w.Sc.Recipe("syn_cancel", "pop"))
	if _, err := w.St.W.Exec(`UPDATE jobs SET state = 'cancelled', finished_at = ? WHERE id = ?`, w.Sc.At(9), j); err != nil {
		t.Fatal(err)
	}
	ab := w.Sc.Batch(`[{"item_index":0}]`, 1, "active")
	ts := w.Sc.At(1)
	if _, err := w.St.W.Exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
		VALUES(?,?,0,'generate','yue2','fake',?,?,NULL,'reroll_seed','{}','Job',216,96,186,10,20,'awaiting_confirmation',?,?,?,?)`,
		w.NewID(), ab, w.Sc.Recipe("syn_await", "pop"), track, ts, ts, ts, ts); err != nil {
		t.Fatal(err)
	}
	p, err := w.Songs.List(context.Background(), songs.ListFilter{})
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != song {
		t.Fatalf("%+v %v", p, err)
	}
	if p.Items[0].QueuedJobs != 6 {
		t.Errorf("queued_jobs = %d, want 6 (queued, submitting, starting, generating, transferring, ingesting)", p.Items[0].QueuedJobs)
	}
}

func TestSongs_ListFilters_Table(t *testing.T) {
	t.Parallel()
	l := newLib(t)
	yes, no := true, false
	live := sorted(l.Rendered, l.Judged, l.Picked, l.StarsPick, l.Draft, l.Queued, l.Proposed)
	cases := []struct {
		name string
		f    songs.ListFilter
		want []string
	}{
		{"defaults hide hidden and archived", songs.ListFilter{}, live},
		{"include hidden", songs.ListFilter{IncludeHidden: true}, sorted(append(slices.Clone(live), l.Hidden)...)},
		{"include archived", songs.ListFilter{IncludeArchived: true}, sorted(append(slices.Clone(live), l.Arch)...)},
		{"muse", songs.ListFilter{MuseID: l.MuseA}, sorted(l.Rendered, l.Judged)},
		{"no muse", songs.ListFilter{NoMuse: true}, sorted(l.StarsPick, l.Queued, l.Proposed)},
		{"angle", songs.ListFilter{Angle: "variant-b"}, sorted(l.Judged)},
		{"state draft", songs.ListFilter{States: []songs.State{songs.StateDraft}}, sorted(l.Draft)},
		{"state judged", songs.ListFilter{States: []songs.State{songs.StateJudged}}, sorted(l.Judged, l.StarsPick)},
		{"state or", songs.ListFilter{States: []songs.State{songs.StateDraft, songs.StatePicked}}, sorted(l.Draft, l.Picked)},
		{"queued", songs.ListFilter{Queued: &yes}, sorted(l.Queued)},
		{"not queued", songs.ListFilter{Queued: &no}, sorted(l.Rendered, l.Judged, l.Picked, l.StarsPick, l.Draft, l.Proposed)},
		{"session via derived membership", songs.ListFilter{SessionID: l.Session}, sorted(l.Rendered, l.Judged, l.Picked, l.StarsPick, l.Queued, l.Proposed)},
		{"sound or inside a facet", songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae", "pop"}}}, sorted(l.Rendered, l.Judged, l.Picked)},
		{"sound and across facets", songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae"}, "vocal": {"male"}}}, sorted(l.Rendered)},
		{"tempo mismatch", songs.ListFilter{TempoMismatch: &yes}, sorted(l.Rendered)},
		{"no tempo mismatch", songs.ListFilter{TempoMismatch: &no}, sorted(l.Judged, l.Picked, l.StarsPick, l.Draft, l.Queued, l.Proposed)},
		{"has pick", songs.ListFilter{HasPick: &yes}, sorted(l.Picked, l.StarsPick)},
		{"no pick", songs.ListFilter{HasPick: &no}, sorted(l.Rendered, l.Judged, l.Draft, l.Queued, l.Proposed)},
		{"open proposals", songs.ListFilter{HasOpenProposals: &yes}, sorted(l.Proposed)},
		{"title substring", songs.ListFilter{Q: "ENDERE"}, sorted(l.Rendered)},
		{"like wildcards are literal", songs.ListFilter{Q: "%"}, nil},
		{"combined", songs.ListFilter{MuseID: l.MuseA, States: []songs.State{songs.StateJudged}, Angle: "variant-b"}, sorted(l.Judged)},
		{"combined empty", songs.ListFilter{MuseID: l.MuseB, Angle: "variant-b"}, nil},
	}
	for _, c := range cases {
		if got := l.ids(t, c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %d %v, want %d %v", c.name, len(got), got, len(c.want), c.want)
		}
	}
}

func TestSongs_ListKeysetPagination(t *testing.T) {
	t.Parallel()
	l := newLib(t)
	var seen []string
	cur := ""
	for i := 0; i < 20; i++ {
		p, err := l.Songs.List(context.Background(), songs.ListFilter{Limit: 3, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 7 {
			t.Errorf("total = %d, want 7", p.Total)
		}
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		if p.NextCursor == nil {
			break
		}
		cur = *p.NextCursor
	}
	if len(seen) != 7 || len(slices.Compact(sorted(slices.Clone(seen)...))) != 7 {
		t.Errorf("pages cover %d ids: %v", len(seen), seen)
	}
	// order is (created_at DESC, id DESC)
	for i := 1; i < len(seen); i++ {
		a := l.Str(`SELECT created_at||id FROM songs WHERE id = ?`, seen[i-1])
		b := l.Str(`SELECT created_at||id FROM songs WHERE id = ?`, seen[i])
		if a < b {
			t.Errorf("order broken at %d", i)
		}
	}
	if _, err := l.Songs.List(context.Background(), songs.ListFilter{Cursor: "%%"}); err == nil {
		t.Error("malformed cursor accepted")
	}
}

func TestListSongs_ByDraftAndTrack(t *testing.T) {
	t.Parallel()
	w := songstest.New(t)
	sc := w.Sc
	rd := sc.Recipe("syn_a_v1", "pop")
	backlog := sc.Draft("syn_a_v1", librarytest.DraftOpts{Recipe: rd})
	rf := sc.Recipe("syn_b_v1", "pop")
	fulfilled := sc.Draft("syn_b_v1", librarytest.DraftOpts{Recipe: rf})
	t1 := sc.Track(rf, "Synthetic Fulfilling", "", sc.At(2))
	sc.Fulfil(fulfilled, t1)
	child := sc.Track(rf, "Synthetic Child", t1, sc.At(4))
	w.Settle()
	list := func(f songs.ListFilter) []string {
		p, err := w.Songs.List(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range p.Items {
			out = append(out, it.ID)
		}
		return out
	}
	if got := list(songs.ListFilter{DraftID: backlog}); len(got) != 1 || got[0] != w.SongOfDraft(backlog) {
		t.Errorf("backlog draft: %v", got)
	}
	song := w.SongOfTrack(t1)
	if got := list(songs.ListFilter{DraftID: fulfilled}); len(got) != 1 || got[0] != song {
		t.Errorf("fulfilled draft: %v, want [%s]", got, song)
	}
	if got := list(songs.ListFilter{TrackID: child}); len(got) != 1 || got[0] != song {
		t.Errorf("child take: %v, want [%s]", got, song)
	}
	if got := list(songs.ListFilter{TrackID: "nope"}); len(got) != 0 {
		t.Errorf("unknown take: %v", got)
	}
	if got := list(songs.ListFilter{DraftID: "nope"}); len(got) != 0 {
		t.Errorf("unknown draft: %v", got)
	}
}

func TestListSongs_HasOpenProposals(t *testing.T) {
	t.Parallel()
	l := newLib(t)
	yes := true
	if got := l.ids(t, songs.ListFilter{HasOpenProposals: &yes}); !slices.Equal(got, sorted(l.Proposed)) {
		t.Fatalf("%v", got)
	}
	l.Proposal(l.Rendered, l.RenderedTake, "agent:b", "dismissed")
	if got := l.ids(t, songs.ListFilter{HasOpenProposals: &yes}); !slices.Equal(got, sorted(l.Proposed)) {
		t.Errorf("a dismissed proposal must not count: %v", got)
	}
}

func TestSongs_Update(t *testing.T) {
	t.Parallel()
	l := newLib(t)
	ctx := context.Background()
	s := l.StarsPick // never-set angle and Muse
	rev := func() int { return l.Count(`SELECT revision FROM songs WHERE id = ?`, s) }

	for name, p := range map[string]songs.Patch{
		"empty patch":    {Revision: 1},
		"empty title":    {Revision: 1, Title: sp("  ")},
		"long title":     {Revision: 1, Title: sp(strings.Repeat("t", 201))},
		"unslugged":      {Revision: 1, Angle: songs.OptString{Set: true, Value: sp("Not A Slug!")}},
		"slug too short": {Revision: 1, Angle: songs.OptString{Set: true, Value: sp("9")}},
	} {
		code, status, _ := problemOf(func() error { _, err := l.Songs.Update(ctx, humanP, s, p); return err }())
		want := "VALIDATION_FAILED"
		if strings.Contains(name, "slug") {
			want = "INVALID_ANGLE"
		}
		if code != want || status != 422 {
			t.Errorf("%s: %s %d, want %s 422", name, code, status, want)
		}
	}
	// stale revision
	_, err := l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: 99, Title: sp("x")})
	if code, status, d := problemOf(err); code != "REVISION_CONFLICT" || status != 409 || d["current_revision"] != rev() {
		t.Errorf("stale: %v", err)
	}
	// title, angle, hide in one patch
	got, err := l.Songs.Update(ctx, agentP, s, songs.Patch{Revision: rev(), Title: sp("  New Title "), Angle: songs.OptString{Set: true, Value: sp("variant-z")}, Hidden: bp(true)})
	if err != nil || got.Title != "New Title" || got.Angle == nil || *got.Angle != "variant-z" || *got.AngleSource != "manual" || got.HiddenAt == nil || got.Revision != 2 || got.UpdatedBy != agentP.Key() {
		t.Fatalf("update: %+v %v", got, err)
	}
	// clear the angle for good: source 'cleared', and a settle never refills it
	got, err = l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: rev(), Angle: songs.OptString{Set: true}, Hidden: bp(false)})
	if err != nil || got.Angle != nil || got.AngleSource == nil || *got.AngleSource != "cleared" || got.HiddenAt != nil {
		t.Fatalf("clear: %+v %v", got, err)
	}
	l.Settle()
	if l.Str(`SELECT angle_source FROM songs WHERE id = ?`, s) != "cleared" {
		t.Error("the Reconciler touched a cleared angle")
	}
	// muse: unknown, hidden, set, clear
	if _, err := l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: rev(), MuseID: songs.OptString{Set: true, Value: sp("nope")}}); func() bool { c, st, _ := problemOf(err); return c != "UNKNOWN_MUSE" || st != 422 }() {
		t.Errorf("unknown muse: %v", err)
	}
	hid, _ := l.newMuse("Synthetic Hidden Muse", "")
	if _, err := l.Muses.Update(ctx, humanP, hid, muses.Patch{Revision: 1, Hidden: bp(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: rev(), MuseID: songs.OptString{Set: true, Value: &hid}}); func() bool { c, st, _ := problemOf(err); return c != "MUSE_HIDDEN" || st != 409 }() {
		t.Errorf("hidden muse: %v", err)
	}
	got, err = l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: rev(), MuseID: songs.OptString{Set: true, Value: &l.MuseB}})
	if err != nil || got.MuseID == nil || *got.MuseID != l.MuseB || *got.MuseSource != "manual" {
		t.Fatalf("set muse: %+v %v", got, err)
	}
	got, err = l.Songs.Update(ctx, humanP, s, songs.Patch{Revision: rev(), MuseID: songs.OptString{Set: true}})
	if err != nil || got.MuseID != nil || got.MuseSource == nil || *got.MuseSource != "manual" {
		t.Fatalf("clear muse: %+v %v", got, err)
	}
	// unknown song, retired song
	if _, err := l.Songs.Update(ctx, humanP, "nope", songs.Patch{Revision: 1, Title: sp("x")}); func() bool { c, st, _ := problemOf(err); return c != "NOT_FOUND" || st != 404 }() {
		t.Errorf("unknown song: %v", err)
	}
	ret := l.retired(l.Rendered)
	_, err = l.Songs.Update(ctx, humanP, ret, songs.Patch{Revision: 1, Title: sp("x")})
	if code, status, d := problemOf(err); code != "SONG_RETIRED" || status != 409 || d["retired_into"] != l.Rendered {
		t.Errorf("retired: %v", err)
	}
}

// retired inserts a take-less Song retired into another.
func (l *lib) retired(into string) string {
	l.T.Helper()
	d := l.Sc.Draft("syn_retired_v1", librarytest.DraftOpts{})
	id := l.NewID()
	if _, err := l.St.W.Exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,?,'Retired','draft','system:test',?,'system:test',?)`, id, d, l.Sc.At(0), l.Sc.At(0)); err != nil {
		l.T.Fatal(err)
	}
	if _, err := l.St.W.Exec(`UPDATE songs SET retired_at=?, retired_into=? WHERE id=?`, l.Sc.At(1), into, id); err != nil {
		l.T.Fatal(err)
	}
	return id
}

func TestParseSoundFilters(t *testing.T) {
	t.Parallel()
	m, err := songs.ParseSoundFilters([]string{"genre_family:reggae", "genre_family:pop", "vocal:male"})
	if err != nil || !slices.Equal(m["genre_family"], []string{"reggae", "pop"}) || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"nofacet", "colour:red", "vocal:", ":x"} {
		_, err := songs.ParseSoundFilters([]string{bad})
		if code, status, _ := problemOf(err); code != "UNKNOWN_FACET" || status != 422 {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
