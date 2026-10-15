package library_test

import (
	"context"
	"github.com/google/go-cmp/cmp"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var allStatuses = []string{"draft", "to_relisten", "final", "discarded"}

func l1Svc(t *testing.T) (*songstest.Lib, *library.Service) {
	t.Helper()
	l := songstest.NewLib(t)
	return l, library.New(l.St, nil, func() time.Time { return l.Now })
}

func searchOn(t *testing.T, svc *library.Service, p library.SearchParams) library.TrackPage {
	t.Helper()
	if p.Status == nil {
		p.Status = allStatuses
	}
	page, err := svc.SearchTracks(context.Background(), p)
	if err != nil {
		t.Fatalf("%+v: %v", p, err)
	}
	return page
}

func TestSearchTracks_SongFilters(t *testing.T) {
	t.Parallel()
	l, svc := l1Svc(t)
	yes, no := true, false

	sameSet(t, "song_id", ids(searchOn(t, svc, library.SearchParams{SongID: l.Picked})), l.PickedTakes[0], l.PickedTakes[1])
	if got := ids(searchOn(t, svc, library.SearchParams{SongID: "no-such-song"})); len(got) != 0 {
		t.Errorf("unknown song_id matched %v", got)
	}
	picks := ids(searchOn(t, svc, library.SearchParams{IsPick: &yes}))
	if !slices.Contains(picks, l.PickedTakes[1]) || slices.Contains(picks, l.PickedTakes[0]) {
		t.Errorf("is_pick=true: %v", picks)
	}
	nonPicks := ids(searchOn(t, svc, library.SearchParams{IsPick: &no}))
	if slices.Contains(nonPicks, l.PickedTakes[1]) || !slices.Contains(nonPicks, l.PickedTakes[0]) {
		t.Errorf("is_pick=false: %v", nonPicks)
	}
	if len(picks)+len(nonPicks) != searchOn(t, svc, library.SearchParams{}).Total {
		t.Errorf("is_pick partitions the library: %d + %d", len(picks), len(nonPicks))
	}
	both := ids(searchOn(t, svc, library.SearchParams{SongID: l.Picked, IsPick: &yes}))
	sameSet(t, "song_id and is_pick", both, l.PickedTakes[1])
	inSession := ids(searchOn(t, svc, library.SearchParams{SessionID: l.Session}))
	if !slices.Contains(inSession, l.RenderedTake) {
		t.Errorf("session_id lost a member: %v", inSession)
	}
	if got := ids(searchOn(t, svc, library.SearchParams{SessionID: "no-such-session"})); len(got) != 0 {
		t.Errorf("unknown session matched %v", got)
	}
	// the same selection serves the exports
	m, err := svc.MatchTrackIDs(context.Background(), library.SearchParams{SongID: l.Picked, Status: allStatuses})
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, "MatchTrackIDs", m, l.PickedTakes[0], l.PickedTakes[1])
}

func TestTrackSummary_SongFields(t *testing.T) {
	t.Parallel()
	l, svc := l1Svc(t)
	for _, it := range searchOn(t, svc, library.SearchParams{}).Items {
		if it.SongID == nil {
			t.Errorf("take %s has no song_id", it.ID)
			continue
		}
		wantSong := l.SongOfTrack(it.ID)
		var pick string
		_ = l.St.R.QueryRow(`SELECT COALESCE(take_id,'') FROM song_picks WHERE song_id = ?`, wantSong).Scan(&pick)
		if *it.SongID != wantSong || it.IsPick != (pick == it.ID) {
			t.Errorf("take %s: song %v is_pick %v, want %s %v", it.ID, *it.SongID, it.IsPick, wantSong, pick == it.ID)
		}
	}
}

func TestTrackDetail_Song(t *testing.T) {
	t.Parallel()
	l, svc := l1Svc(t)
	d, err := svc.Track(context.Background(), l.PickedTakes[1])
	if err != nil {
		t.Fatal(err)
	}
	if d.Song == nil || d.Song.ID != l.Picked || d.Song.Title == "" || d.Song.MuseID == nil || *d.Song.MuseID != l.MuseB ||
		d.Song.PickSource == nil || *d.Song.PickSource != "pinned" {
		t.Errorf("song %+v", d.Song)
	}
	d, err = svc.Track(context.Background(), l.PickedTakes[0])
	if err != nil || d.Song == nil || d.Song.ID != l.Picked {
		t.Fatalf("sibling take: %+v %v", d.Song, err)
	}
	// a Song with no Pick has a null pick_source; a take without a Song has no song object
	d, err = svc.Track(context.Background(), l.RenderedTake)
	if err != nil || d.Song == nil || d.Song.Angle == nil || *d.Song.Angle != "variant-a" {
		t.Fatalf("rendered take: %+v %v", d.Song, err)
	}
}

func TestLibrary_OldSchemaFixturesStillRead(t *testing.T) {
	t.Parallel()
	fixtures := map[string]func(testing.TB, *store.Store, time.Time) lt.Fixtures{
		"FixtureV2": lt.FixtureV2, "Fixture": lt.Fixture, "FixtureM2c": lt.FixtureM2c, "FixtureM3": lt.FixtureM3,
	}
	yes := true
	for name, fx := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnvWith(t, fx)
			page := searchOn(t, e.svc, library.SearchParams{})
			if len(page.Items) == 0 {
				t.Fatal("empty library")
			}
			for _, it := range page.Items {
				if it.SongID != nil || it.IsPick {
					t.Errorf("%s: song_id %v is_pick %v on a library without Songs", it.ID, it.SongID, it.IsPick)
				}
				d, err := e.svc.Track(context.Background(), it.ID)
				if err != nil || d.Song != nil {
					t.Errorf("%s: song %+v %v", it.ID, d.Song, err)
				}
			}
			// the new filters match nothing instead of failing
			for _, p := range []library.SearchParams{{SongID: "x"}, {IsPick: &yes}, {SessionID: "x"}} {
				if got := searchOn(t, e.svc, p); len(got.Items) != 0 {
					t.Errorf("%+v matched %v", p, ids(got))
				}
			}
		})
	}
}

func TestGetStats_LibraryModel(t *testing.T) {
	t.Parallel()
	l, svc := l1Svc(t)
	svc.UseMuseCounter(func(ctx context.Context) (int, error) {
		var n int
		err := l.St.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM muses WHERE hidden_at IS NULL`).Scan(&n)
		return n, err
	})
	st, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := st.LibraryModel
	if m == nil {
		t.Fatal("library_model is nil on a migrated schema")
	}
	// independent SQL: live = not hidden, not retired, not an archived draft
	live := `FROM songs g WHERE g.hidden_at IS NULL AND g.retired_at IS NULL
		AND (EXISTS (SELECT 1 FROM take_songs ts WHERE ts.song_id = g.id) OR NOT COALESCE((SELECT d.status = 'archived' FROM recipe_drafts d WHERE d.id = g.draft_id), 0))`
	if got := l.Count(`SELECT COUNT(*) ` + live); m.Songs.Total != got || got == 0 {
		t.Errorf("songs total %d, want %d", m.Songs.Total, got)
	}
	if m.Songs.Draft+m.Songs.Rendered+m.Songs.Judged+m.Songs.Picked != m.Songs.Total {
		t.Errorf("states do not add up: %+v", m.Songs)
	}
	if m.Songs.Picked < 1 || m.Songs.Judged < 1 || m.Songs.Rendered < 1 || m.Songs.Draft < 1 {
		t.Errorf("the fixture covers every state: %+v", m.Songs)
	}
	if want := l.Count(`SELECT COUNT(*) FROM muses WHERE hidden_at IS NULL`); m.Muses != want || want < 2 {
		t.Errorf("muses %d, want %d", m.Muses, want)
	}
	if want := l.Count(`SELECT COUNT(*) FROM sessions WHERE hidden_at IS NULL`); m.Sessions != want {
		t.Errorf("sessions %d, want %d", m.Sessions, want)
	}
	if want := l.Count(`SELECT COALESCE(MAX(version),0) FROM sound_taxonomy_versions`); m.TaxonomyVersion != want {
		t.Errorf("taxonomy_version %d, want %d", m.TaxonomyVersion, want)
	}
	// a new take queues provenance work
	before := m.Pending
	l.Sc.Track(l.Sc.Recipe("syn_stats_pending", "pop"), "Synthetic Pending", "", l.Sc.At(30))
	st, err = svc.Stats(context.Background())
	if err != nil || st.LibraryModel.Pending <= before {
		t.Errorf("pending %d -> %v (%v)", before, st.LibraryModel, err)
	}
}

func TestDraftReference_AgentView(t *testing.T) {
	t.Parallel()
	l, svc := l1Svc(t)
	ctx := context.Background()
	// the Muse the draft's Song carries (settled by the Reconciler)
	museTitle := l.Str(`SELECT title FROM muses WHERE id = ?`, l.RefMuse)
	museArtist := l.Str(`SELECT artist FROM muses WHERE id = ?`, l.RefMuse)
	if museTitle == "" || museArtist == "" {
		t.Fatalf("fixture Muse: %q %q", museTitle, museArtist)
	}
	h, err := svc.Draft(ctx, l.DraftID, true)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Draft(ctx, l.DraftID, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.Reference["source_title"] != museTitle || h.Reference["source_artist"] != museArtist || h.Reference["variant_type"] != "variant-a" {
		t.Errorf("human view: %v", h.Reference)
	}
	if diff := cmp.Diff(map[string]string{"source_title": museTitle, "source_artist": museArtist}, a.Reference); diff != "" {
		t.Errorf("agent view equals the Muse text and nothing more (-want +got):\n%s", diff)
	}
	// a draft with no reference: null for both
	for _, human := range []bool{true, false} {
		n, err := svc.Draft(ctx, l.ArchDraftID, human)
		if err != nil || n.HasReference || n.Reference != nil {
			t.Errorf("human=%v: %+v %v", human, n.Reference, err)
		}
	}
}
