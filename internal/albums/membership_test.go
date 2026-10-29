package albums_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func list(t *testing.T, w *world, kinds ...albums.Kind) []albums.Album {
	t.Helper()
	got, err := w.Svc.List(context.Background(), kinds...)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func takeIDs(songs []albums.Song) []string {
	var out []string
	for _, s := range songs {
		out = append(out, s.TakeID)
	}
	return out
}

// TestAlbums_MembershipTable: the record kind of C126.1 on FixtureL2, one mutation per row.
func TestAlbums_MembershipTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, w *world)
		listed bool     // R1 is listed
		takes  []string // labels, in order
		discs  []int
	}{
		{name: "fixture: A, B, C pinned, E; the empty slot D is not listed", listed: true, takes: []string{"a1", "b1", "c2", "e1"}, discs: []int{1, 1, 1, 1}},
		{name: "discarded pinned Take leaves the album", listed: true, takes: []string{"a1", "b1", "e1"}, discs: []int{1, 1, 1},
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id = ?`, w.Info.Takes["c2"])
			}},
		{name: "unpinning the slot follows the Pick", listed: true, takes: []string{"a1", "b1", "c1", "e1"}, discs: []int{1, 1, 1, 1},
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE record_slots SET pinned_take_id = NULL, pinned_by = NULL, pinned_at = NULL WHERE id = ?`, w.Info.Slots["C"])
			}},
		{name: "discs follow the sides", listed: true, takes: []string{"a1", "b1", "c2", "e1"}, discs: []int{1, 2, 4, 3},
			mutate: func(t *testing.T, w *world) {
				for k, side := range map[string]string{"A": "A", "B": "B", "C": "D", "E": "C"} {
					w.exec(t, `UPDATE record_slots SET side = ? WHERE id = ?`, side, w.Info.Slots[k])
				}
			}},
		{name: "a draft Record is not listed", listed: false,
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `DROP TRIGGER IF EXISTS records_status_path`)
				w.exec(t, `UPDATE records SET status = 'draft', published_by = NULL, published_at = NULL WHERE id = ?`, w.Info.Records["R1"])
			}},
		{name: "a hidden Record is not listed", listed: false,
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE records SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'human:fixture' WHERE id = ?`, w.Info.Records["R1"])
			}},
		{name: "a Record with no listable slot is not listed", listed: false,
			mutate: func(t *testing.T, w *world) {
				for _, k := range []string{"A", "B", "C", "E"} {
					w.exec(t, `UPDATE record_slots SET removed_by = 'human:fixture', removed_at = '2026-10-28T10:00:00.000Z' WHERE id = ?`, w.Info.Slots[k])
				}
			}},
		{name: "a removed slot is not listed", listed: true, takes: []string{"a1", "c2", "e1"}, discs: []int{1, 1, 1},
			mutate: func(t *testing.T, w *world) {
				w.exec(t, `UPDATE record_slots SET removed_by = 'human:fixture', removed_at = '2026-10-28T10:00:00.000Z' WHERE id = ?`, w.Info.Slots["B"])
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, nil)
			if tc.mutate != nil {
				tc.mutate(t, w)
			}
			got := list(t, w)
			id := albums.PrefixRecord + w.Info.Records["R1"]
			if !tc.listed {
				if len(got) != 0 {
					t.Fatalf("List = %+v, want nothing", got)
				}
				if _, _, err := w.Svc.Get(context.Background(), id); !errors.Is(err, albums.ErrNotFound) {
					t.Errorf("Get = %v, want ErrNotFound", err)
				}
				return
			}
			if len(got) != 1 || got[0].ID != id {
				t.Fatalf("List = %+v", got)
			}
			a, songs, err := w.Svc.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, l := range tc.takes {
				want = append(want, w.Info.Takes[l])
			}
			if d := cmp.Diff(want, takeIDs(songs)); d != "" {
				t.Errorf("songs (-want +got):\n%s", d)
			}
			if a.SongCount != len(songs) || a.Kind != albums.KindRecord || a.RecordID == nil || *a.RecordID != w.Info.Records["R1"] || a.MuseID != nil {
				t.Errorf("album = %+v", a)
			}
			seen := map[string]bool{}
			for i, s := range songs {
				if s.Position != i+1 || s.Disc != tc.discs[i] || s.SlotID == nil || seen[s.TakeID] {
					t.Errorf("song %d = %+v", i, s)
				}
				seen[s.TakeID] = true
			}
			if d := cmp.Diff(got[0], a); d != "" {
				t.Errorf("List and Get disagree:\n%s", d)
			}
		})
	}
}

// TestAlbums_PublishedAndOrder: publishing the draft R2 adds an album after R1 (first_published_at, then id); the kinds filter
// and the name rules hold.
func TestAlbums_PublishedAndOrder(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	w.exec(t, `UPDATE records SET title = ? WHERE id = ?`, "Élan  Vital", w.Info.Records["R2"])
	w.publish(t, w.Info.Records["R2"])
	got := list(t, w, albums.KindRecord)
	if len(got) != 2 || got[0].RecordID == nil || *got[0].RecordID != w.Info.Records["R1"] || *got[1].RecordID != w.Info.Records["R2"] {
		t.Fatalf("List = %+v", got)
	}
	// R1 was published at fixture time (earlier than 2026-10-28T09:00 by the fixture clock), R2 after it
	if got[1].Name != "Élan  Vital" || got[1].SortName != "elan vital" || got[1].SongCount != 2 {
		t.Errorf("R2 = %+v", got[1])
	}
	if got[0].Name != "Synthetic Record One" || got[0].SortName != "synthetic record one" {
		t.Errorf("R1 = %+v", got[0])
	}
	if n := len(list(t, w, albums.KindMuse, albums.KindUnfiled)); n != 0 {
		t.Errorf("muse and unfiled kinds belong to A2, got %d albums", n)
	}
	if n := len(list(t, w, albums.KindMuse, albums.KindRecord)); n != 2 {
		t.Errorf("a mixed kinds list returns the record albums, got %d", n)
	}
}

// TestAlbums_GetUnknown: unknown, malformed, draft and not-yet-built ids are ErrNotFound.
func TestAlbums_GetUnknown(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	for _, id := range []string{"", "rec-", "rec-nope", "al-" + w.Info.Records["R1"], w.Info.Records["R1"], "rec-" + w.Info.Records["R2"], albums.PrefixMuse + "x", albums.IDUnfiled} {
		if _, _, err := w.Svc.Get(context.Background(), id); !errors.Is(err, albums.ErrNotFound) {
			t.Errorf("Get(%q) = %v", id, err)
		}
	}
}

// TestAlbums_CoverInput: the cover is drawn from the Record's id and seed and the lineage of its slot Songs, never from the
// current Takes, so a Pick change leaves it alone.
func TestAlbums_CoverInput(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	var seed int64
	if err := w.St.R.QueryRow(`SELECT cover_seed FROM records WHERE id = ?`, w.Info.Records["R1"]).Scan(&seed); err != nil {
		t.Fatal(err)
	}
	before := list(t, w)[0].CoverInput
	if before.Kind != cover.KindRecord || before.ID != w.Info.Records["R1"] || before.Seed != seed || len(before.Parts) != 5 {
		t.Fatalf("cover input = %+v", before)
	}
	for _, p := range before.Parts {
		if p.RootID == "" {
			t.Errorf("part %+v has no root", p)
		}
	}
	w.exec(t, `UPDATE tracks SET status = 'discarded', status_set_by = 'x' WHERE id = ?`, w.Info.Takes["c2"])
	if d := cmp.Diff(before, list(t, w)[0].CoverInput); d != "" {
		t.Errorf("a discarded Take changed the cover input:\n%s", d)
	}
}

// goldenAlbum is the shape of albums_fixture.golden.json.
type goldenAlbum struct {
	Album albums.Album  `json:"album"`
	Songs []albums.Song `json:"songs"`
}

// TestAlbums_Golden: the full list and every album's songs on FixtureL2 (public-safe: synthetic names, fixture ids). S1's
// Subsonic tests compare ids and counts against the same file.
func TestAlbums_Golden(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	var out []goldenAlbum
	for _, a := range list(t, w) {
		_, songs, err := w.Svc.Get(context.Background(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, goldenAlbum{a, songs})
	}
	got, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/albums_fixture.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Errorf("golden mismatch (run with -update to accept):\n%s", got)
	}
}

// TestAlbums_StatementCount: List and Get take at most six statements however many Records there are (C126.6).
func TestAlbums_StatementCount(t *testing.T) {
	t.Parallel()
	w := newWorld(t, nil)
	w.publish(t, w.Info.Records["R2"])
	for name, f := range map[string]func(){
		"List": func() { list(t, w) },
		"Get":  func() { _, _, _ = w.Svc.Get(context.Background(), albums.PrefixRecord+w.Info.Records["R1"]) },
	} {
		before := w.N.Load()
		f()
		if got := w.N.Load() - before; got > 6 || got < 1 {
			t.Errorf("%s: %d statements, want 1..6", name, got)
		}
	}
}
