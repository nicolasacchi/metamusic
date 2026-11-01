package sounds_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

var errHookBoom = errors.New("hook boom")

type hookCall struct {
	songs []string
	// fresh is how many of the call's Songs already had the new taxonomy version in sound_state inside the batch tx.
	fresh int
}

// recordingHook returns a hook that records every call and, like the features refresh, writes one row per Song into
// hook_log from the batch transaction. failOn (1-based) makes that call fail.
func recordingHook(t *testing.T, w *world, version int, failOn int) (*[]hookCall, sounds.RecomputeHook) {
	t.Helper()
	w.exec(`CREATE TABLE hook_log(song_id TEXT PRIMARY KEY, family TEXT NOT NULL)`)
	var calls []hookCall
	return &calls, func(ctx context.Context, tx *sql.Tx, env sounds.Env, songIDs []string) error {
		c := hookCall{songs: slices.Clone(songIDs)}
		for _, id := range songIDs {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sound_state WHERE subject_kind = 'song' AND subject_id = ? AND taxonomy_version = ?`, id, version).Scan(&n); err != nil {
				return err
			}
			c.fresh += n
			if _, err := tx.ExecContext(ctx, `INSERT INTO hook_log(song_id, family) VALUES(?, COALESCE((SELECT value FROM song_sounds WHERE song_id = ? AND facet = 'genre_family' ORDER BY ord LIMIT 1), ''))
				ON CONFLICT(song_id) DO UPDATE SET family = excluded.family`, id, id); err != nil {
				return err
			}
		}
		calls = append(calls, c)
		if failOn > 0 && len(calls) == failOn {
			return errHookBoom
		}
		return nil
	}
}

func TestMap_RecomputeHookCalledPerBatch(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	bulkLibrary(t, w, 150) // 150 takes + 150 Songs: the first batch mixes 100 subjects, later ones are Songs only
	calls, hook := recordingHook(t, w, 2, 0)
	w.sv.SetRecomputeHook(hook)
	res, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("wobble 3", "mood", "dark", "add")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecomputedSongs != 150 || res.RecomputedTakes != 150 {
		t.Fatalf("recomputed %+v", res)
	}
	if len(*calls) < 2 {
		t.Fatalf("hook called %d times, want once per batch that rewrote Songs", len(*calls))
	}
	seen := map[string]int{}
	for i, c := range *calls {
		if len(c.songs) == 0 || len(c.songs) > sounds.BatchKeys {
			t.Errorf("call %d carried %d Songs", i, len(c.songs))
		}
		if c.fresh != len(c.songs) {
			t.Errorf("call %d: %d of %d Songs were rewritten inside the batch transaction", i, c.fresh, len(c.songs))
		}
		if !slices.IsSorted(c.songs) {
			t.Errorf("call %d: ids not sorted", i)
		}
		for _, id := range c.songs {
			seen[id]++
		}
	}
	if len(seen) != 150 {
		t.Errorf("hook saw %d distinct Songs, want 150", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("Song %s passed to the hook %d times", id, n)
		}
	}
	// A no-op Map runs no batch with work, so the hook stays quiet.
	before := len(*calls)
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("wobble 3", "mood", "dark", "add")}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != before {
		t.Errorf("hook called for an unchanged Map")
	}
}

func TestMap_RecomputeHookErrorAbortsBatch(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	bulkLibrary(t, w, 150)
	calls, hook := recordingHook(t, w, 2, 2)
	w.sv.SetRecomputeHook(hook)
	_, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("wobble 3", "mood", "dark", "add")}})
	if !errors.Is(err, errHookBoom) {
		t.Fatalf("err = %v, want the hook error", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("hook called %d times, the failing call must be the last", len(*calls))
	}
	// The version is committed; the failed batch rolled back with its hook writes, the first batch stays.
	if n := w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`); n != 2 {
		t.Errorf("versions = %d", n)
	}
	first := (*calls)[0].songs
	if n := w.count(`SELECT COUNT(*) FROM sound_state WHERE subject_kind = 'song' AND taxonomy_version = 2`); n != len(first) {
		t.Errorf("%d Songs at version 2, want only the first call's %d", n, len(first))
	}
	if n := w.count(`SELECT COUNT(*) FROM hook_log`); n != len(first) {
		t.Errorf("hook_log has %d rows, want %d", n, len(first))
	}
	for _, id := range (*calls)[1].songs {
		if w.count(`SELECT COUNT(*) FROM hook_log WHERE song_id = ?`, id) != 0 || w.count(`SELECT COUNT(*) FROM sound_state WHERE subject_id = ? AND taxonomy_version = 2`, id) != 0 {
			t.Fatalf("Song %s of the failed batch left a trace", id)
		}
	}
	// The next Map (no hook) finishes the stale subjects, as the startup sweep does.
	w.sv.SetRecomputeHook(nil)
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("wobble 4", "mood", "dark", "add")}}); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT COUNT(*) FROM sound_state WHERE taxonomy_version <> 3`); n != 0 {
		t.Errorf("%d subjects stale after the next Map", n)
	}
}

func TestMap_RefreshesFeaturesInPlace(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	d := w.draft("skank, 90bpm")
	w.settle()
	song := w.songOfDraft(d)
	_, hook := recordingHook(t, w, 2, 0)
	w.sv.SetRecomputeHook(hook)
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT family FROM hook_log WHERE song_id = ?`, song); got != "reggae" {
		t.Errorf("features-shaped row family = %q, want reggae (written with song_sounds)", got)
	}
}
