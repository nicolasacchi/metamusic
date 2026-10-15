package playlists_test

import (
	"context"
	"errors"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

func offlineDetails(t *testing.T, err error) map[string]any {
	t.Helper()
	var pe *playlists.Error
	if !errors.As(err, &pe) {
		t.Fatalf("not a playlists.Error: %v", err)
	}
	return pe.Details
}

// TestOfflineSet_Limits: more than 100 items and hiding are refused with stable codes (C111).
func TestOfflineSet_Limits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()

	big := make([]string, playlists.OfflineMaxItems+1)
	for i := range big {
		big[i] = lt.TrackLuna
	}
	_, err := e.svc.Update(ctx, human, playlists.OfflineSetID, playlists.PlaylistPatch{Revision: 1, TrackIDs: big})
	problem(t, err, 422, "OFFLINE_SET_TOO_LARGE")
	if d := offlineDetails(t, err); d["items"] != len(big) || d["limit"] != playlists.OfflineMaxItems {
		t.Errorf("details = %v", d)
	}

	p, err := e.svc.Update(ctx, human, playlists.OfflineSetID, playlists.PlaylistPatch{Revision: 1, TrackIDs: big[:playlists.OfflineMaxItems]})
	if err != nil || len(p.Items) != playlists.OfflineMaxItems {
		t.Fatalf("100 items: %d %v", len(p.Items), err)
	}
	_, err = e.svc.Update(ctx, human, playlists.OfflineSetID, playlists.PlaylistPatch{Revision: 2, AddTrackIDs: []string{lt.TrackLuna}})
	problem(t, err, 422, "OFFLINE_SET_TOO_LARGE")

	yes := true
	_, err = e.svc.Update(ctx, human, playlists.OfflineSetID, playlists.PlaylistPatch{Revision: 2, Hidden: &yes})
	problem(t, err, 422, "VALIDATION_FAILED")
	if d := offlineDetails(t, err); d["reason"] != "offline_set_cannot_be_hidden" {
		t.Errorf("details = %v", d)
	}
	no := false
	name := "Keep these"
	if _, err := e.svc.Update(ctx, human, playlists.OfflineSetID, playlists.PlaylistPatch{Revision: 2, Hidden: &no, Name: &name}); err != nil {
		t.Errorf("rename: %v", err)
	}
}

// TestOfflineSet_AlwaysListed: a direct SQL edit that hides the set does not remove it from List (C111).
func TestOfflineSet_AlwaysListed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.st.W.ExecContext(ctx, `UPDATE playlists SET hidden_at = '2026-01-01T00:00:00Z' WHERE id = ?`, playlists.OfflineSetID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.List(ctx, false)
	found := false
	for _, s := range got {
		found = found || s.ID == playlists.OfflineSetID
	}
	if err != nil || !found {
		t.Fatalf("list = %+v %v", got, err)
	}
}

// TestOfflineFacts: per-track media facts for the offline plan (C111).
func TestOfflineFacts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	lt.ApplyM2c(t, e.st, lt.FixedNow)
	got, err := e.svc.OfflineFacts(ctx, []string{lt.TrackLuna, lt.TrackRefFlac, lt.TrackLossy, "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["nope"]; ok || len(got) != 3 {
		t.Fatalf("facts = %+v", got)
	}
	luna := got[lt.TrackLuna]
	if luna.Lossy || luna.Flac.SHA256 == "" || luna.Flac.Bytes <= 0 || luna.Flac.SampleRate == 0 || luna.Flac.BitsPerSample == 0 ||
		luna.MP3 == nil || luna.MP3.SHA256 == "" || luna.Title != "Luna Park" || luna.Album == "" || luna.DurationS <= 0 {
		t.Errorf("luna = %+v", luna)
	}
	lossy := got[lt.TrackLossy]
	if !lossy.Lossy || lossy.MP3 == nil || lossy.MP3.Bytes <= 0 || lossy.Flac != nil {
		t.Errorf("lossy = %+v", lossy)
	}
}
