package playlists_test

import (
	"context"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// TestPlaylists_RoleRead: Get and List return Role "offline" for the migration row and nil for every other playlist (C111).
func TestPlaylists_RoleRead(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	const offlineSet = "00000000-0000-7000-8000-0000000000f1"

	off, err := e.svc.Get(ctx, offlineSet)
	if err != nil || off.Role == nil || *off.Role != "offline" {
		t.Fatalf("offline Get: %+v %v", off, err)
	}
	mix, err := e.svc.Get(ctx, lt.PlaylistMix)
	if err != nil || mix.Role != nil {
		t.Fatalf("mix Get: %+v %v", mix, err)
	}
	list, err := e.svc.List(ctx, true)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, s := range list {
		switch s.ID {
		case offlineSet:
			if s.Role == nil || *s.Role != "offline" {
				t.Errorf("offline summary role = %v", s.Role)
			}
		default:
			if s.Role != nil {
				t.Errorf("%s role = %v, want nil", s.ID, *s.Role)
			}
		}
	}
	created, err := e.svc.Create(ctx, human, playlists.NewPlaylist{Name: "Plain"})
	if err != nil || created.Role != nil {
		t.Fatalf("Create sets no role: %+v %v", created, err)
	}
}

func TestOfflineMaxItems(t *testing.T) {
	t.Parallel()
	if got := playlists.OfflineMaxItems; got != 100 {
		t.Fatalf("OfflineMaxItems = %d, want 100", got)
	}
}
