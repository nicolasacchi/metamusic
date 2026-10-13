package playlists_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	st  *store.Store
	svc *playlists.Service
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, now: lt.FixedNow.Add(time.Hour)}
	lt.Fixture(t, st, lt.FixedNow)
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	lib := library.New(st, root, func() time.Time { return e.now })
	var n atomic.Int64
	n.Store(0x6000)
	e.svc = playlists.New(st, lib, func() time.Time { return e.now }, func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1))
	})
	return e
}

var human = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}

func problem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var pe *playlists.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("error %v, want %d %s", err, status, code)
	}
}

func ids(p playlists.Playlist) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Track.ID)
	}
	return out
}

func TestPlaylists_Revision(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	p, err := e.svc.Create(ctx, human, playlists.NewPlaylist{Name: "  Night  ", Description: "d", TrackIDs: []string{lt.TrackCitta, lt.TrackLuna}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Night" || p.Revision != 1 || p.CreatedBy != "human:fixture" || !slices.Equal(ids(p), []string{lt.TrackCitta, lt.TrackLuna}) {
		t.Fatalf("created %+v", p)
	}
	if p.Items[1].Position != 2 || p.Items[0].Track.Title != "Città lenta" {
		t.Errorf("items %+v", p.Items)
	}

	e.now = e.now.Add(time.Minute)
	name := "Day"
	p2, err := e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 1, Name: &name, AddTrackIDs: []string{lt.TrackLuna, lt.TrackTwinA}})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Revision != 2 || p2.Name != "Day" || p2.UpdatedAt == p.UpdatedAt || p2.CreatedAt != p.CreatedAt ||
		!slices.Equal(ids(p2), []string{lt.TrackCitta, lt.TrackLuna, lt.TrackLuna, lt.TrackTwinA}) {
		t.Fatalf("updated %+v", p2)
	}

	_, err = e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 1, Name: &name})
	problem(t, err, 409, "REVISION_CONFLICT")
	_, err = e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 2, TrackIDs: []string{lt.TrackLuna}, AddTrackIDs: []string{lt.TrackLuna}})
	problem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 2, AddTrackIDs: []string{"nope"}})
	problem(t, err, 422, "UNKNOWN_TRACK")
	_, err = e.svc.Create(ctx, human, playlists.NewPlaylist{Name: "x", TrackIDs: []string{"nope"}})
	problem(t, err, 422, "UNKNOWN_TRACK")
	_, err = e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 2})
	problem(t, err, 422, "VALIDATION_FAILED")
	if _, err := e.svc.Update(ctx, human, "missing", playlists.PlaylistPatch{Revision: 1, Name: &name}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown playlist: %v", err)
	}

	// full replacement, including clearing
	p3, err := e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 2, TrackIDs: []string{lt.TrackShort}})
	if err != nil || !slices.Equal(ids(p3), []string{lt.TrackShort}) || p3.Revision != 3 {
		t.Fatalf("replace: %+v %v", p3, err)
	}
	p4, err := e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 3, TrackIDs: []string{}})
	if err != nil || len(p4.Items) != 0 {
		t.Fatalf("clear: %+v %v", p4, err)
	}
}

func TestPlaylists_Limit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	many := slices.Repeat([]string{lt.TrackLuna}, 1001)
	_, err := e.svc.Create(ctx, human, playlists.NewPlaylist{Name: "big", TrackIDs: many})
	problem(t, err, 422, "VALIDATION_FAILED")
	p, err := e.svc.Create(ctx, human, playlists.NewPlaylist{Name: "big", TrackIDs: many[:1000]})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Update(ctx, human, p.ID, playlists.PlaylistPatch{Revision: 1, AddTrackIDs: []string{lt.TrackLuna}})
	problem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Create(ctx, human, playlists.NewPlaylist{Name: strings.Repeat("n", 121)})
	problem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Create(ctx, human, playlists.NewPlaylist{Name: " "})
	problem(t, err, 422, "VALIDATION_FAILED")
}

func TestPlaylists_ListHidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	got, err := e.svc.List(ctx, false)
	if err != nil || len(got) != 1 || got[0].ID != lt.PlaylistMix || got[0].ItemsCount != 3 || got[0].DurationS != 26 {
		t.Fatalf("list %+v %v", got, err)
	}
	yes := true
	e.now = e.now.Add(time.Minute)
	p, err := e.svc.Update(ctx, human, lt.PlaylistMix, playlists.PlaylistPatch{Revision: 1, Hidden: &yes})
	if err != nil || !p.Hidden {
		t.Fatalf("hide: %+v %v", p, err)
	}
	if got, _ := e.svc.List(ctx, false); len(got) != 0 {
		t.Errorf("hidden listed: %+v", got)
	}
	got, _ = e.svc.List(ctx, true)
	if len(got) != 1 || !got[0].Hidden || got[0].Revision != 2 {
		t.Errorf("include_hidden: %+v", got)
	}
	no := false
	if p, _ = e.svc.Update(ctx, human, lt.PlaylistMix, playlists.PlaylistPatch{Revision: 2, Hidden: &no}); p.Hidden {
		t.Error("unhide failed")
	}
	g, err := e.svc.Get(ctx, lt.PlaylistMix)
	if err != nil || len(g.Items) != 3 || g.Items[0].Track.Title != "Luna Park" || g.Items[0].Track.Stars == nil || *g.Items[0].Track.Stars != 5 {
		t.Errorf("get: %+v %v", g, err)
	}
	if _, err := e.svc.Get(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("get unknown: %v", err)
	}
}
