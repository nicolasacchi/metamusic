package songstest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// HumanP and AgentP are the principals the scenes act as.
var (
	HumanP = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}
	AgentP = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

// Lib is the L1 scene of the state, filter and board tests: 8 live Songs (plus an archived draft Song) covering every
// state and filter dimension, two Muses, one import session and sound rows. Synthetic data only.
type Lib struct {
	*World
	MuseA, MuseB, RefMuse                              string
	Rendered, Judged, Picked, StarsPick, Draft, Arch   string // Song ids
	Hidden, Queued, Proposed                           string
	PickedTakes                                        [2]string
	DraftID, ArchDraftID                               string
	Session                                            string
	RenderedTake, JudgedTake, QueuedTake, ProposedTake string
}

func NewLib(t testing.TB) *Lib {
	t.Helper()
	w := New(t)
	l := &Lib{World: w}
	sc := w.Sc
	ctx := context.Background()

	run := sc.Run("completed", 0)
	mk := func(title string, at int) (track string) {
		r := sc.Recipe(fmt.Sprintf("syn_%s_v1", strings.ReplaceAll(strings.ToLower(title), " ", "_")), "reggae, male vocal, 76bpm")
		return sc.RootInRun(r, title, run, sc.At(at))
	}
	l.RenderedTake = mk("Synthetic Rendered", 0)
	l.JudgedTake = mk("Synthetic Judged", 2)
	p1 := mk("Synthetic Picked", 4)
	child := sc.Track(sc.Recipe("syn_picked_child", "pop"), "Synthetic Picked Child", p1, sc.At(6))
	l.PickedTakes = [2]string{p1, child}
	stars := mk("Synthetic Stars", 8)
	stars2 := sc.Track(sc.Recipe("syn_stars_child", "pop"), "Synthetic Stars Child", stars, sc.At(10))
	hidden := mk("Synthetic Hidden", 12)
	l.QueuedTake = mk("Synthetic Queued", 14)
	l.ProposedTake = mk("Synthetic Proposed", 16)

	rd := sc.Recipe("syn_draft_v1", "pop")
	sc.Ref(rd, "i.csv", "Synthetic Muse 001", "Synthetic Artist 001", "variant-a")
	l.DraftID = sc.Draft("syn_draft_v1", librarytest.DraftOpts{Recipe: rd, Group: "g", Label: "variant-a"})
	l.ArchDraftID = sc.Draft("syn_arch_v1", librarytest.DraftOpts{Group: "g"})
	sc.Rate(l.JudgedTake, 4)
	sc.Rate(stars2, 5)
	w.Settle()
	if _, err := w.St.W.Exec(`UPDATE recipe_drafts SET status = 'archived' WHERE id = ?`, l.ArchDraftID); err != nil {
		t.Fatal(err)
	}
	w.Settle()

	l.Rendered, l.Judged = w.SongOfTrack(l.RenderedTake), w.SongOfTrack(l.JudgedTake)
	l.Picked, l.StarsPick = w.SongOfTrack(p1), w.SongOfTrack(stars)
	l.Hidden, l.Queued, l.Proposed = w.SongOfTrack(hidden), w.SongOfTrack(l.QueuedTake), w.SongOfTrack(l.ProposedTake)
	l.Draft, l.Arch = w.SongOfDraft(l.DraftID), w.SongOfDraft(l.ArchDraftID)

	l.RefMuse = w.Str(`SELECT muse_id FROM songs WHERE id = ?`, l.Draft)
	l.Pick(l.Picked, child, "pinned")
	l.Pick(l.StarsPick, stars2, "stars")
	l.Proposal(l.Proposed, l.ProposedTake, AgentP.Key(), "open")
	// a live job on the queued Song's take
	b := sc.Batch(`[{"item_index":0}]`, 1, "active")
	sc.Job(b, 0, "", l.QueuedTake, sc.Recipe("syn_job", "pop"))

	var err error
	if l.MuseA, err = l.NewMuse("Synthetic Alpha Muse", "Synthetic Alpha Artist"); err != nil {
		t.Fatal(err)
	}
	if l.MuseB, err = l.NewMuse("Synthetic Beta Muse", ""); err != nil {
		t.Fatal(err)
	}
	set := func(song string, p songs.Patch) {
		t.Helper()
		cur := l.Count(`SELECT revision FROM songs WHERE id = ?`, song)
		p.Revision = cur
		if _, err := w.Songs.Update(ctx, HumanP, song, p); err != nil {
			t.Fatalf("update %s: %v", song, err)
		}
	}
	set(l.Rendered, songs.Patch{MuseID: songs.OptString{Set: true, Value: &l.MuseA}, Angle: songs.OptString{Set: true, Value: strp("variant-a")}})
	set(l.Judged, songs.Patch{MuseID: songs.OptString{Set: true, Value: &l.MuseA}, Angle: songs.OptString{Set: true, Value: strp("variant-b")}})
	set(l.Picked, songs.Patch{MuseID: songs.OptString{Set: true, Value: &l.MuseB}})
	set(l.Hidden, songs.Patch{Hidden: boolp(true)})

	w.Sound(l.Rendered, "genre_family", "reggae", "style", "")
	w.Sound(l.Rendered, "vocal", "male", "style", "")
	w.Sound(l.Rendered, "tempo_band", "slow", "declared", `{"mismatch":true}`)
	w.Sound(l.Judged, "genre_family", "reggae", "style", "")
	w.Sound(l.Judged, "tempo_band", "slow", "declared", `{"mismatch":false}`)
	w.Sound(l.Picked, "genre_family", "pop", "style", "")
	l.Session = w.Str(`SELECT id FROM sessions WHERE kind = 'import' LIMIT 1`)
	return l
}

func (l *Lib) NewMuse(title, artist string) (string, error) {
	var a *string
	if artist != "" {
		a = &artist
	}
	m, err := l.Muses.Create(context.Background(), HumanP, muses.NewMuse{Kind: muses.KindReference, Title: title, Artist: a})
	return m.ID, err
}
