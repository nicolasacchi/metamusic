// Package songstest is the test world of the L1 library lane (B5): a migrated store, a Scene, the Reconciler and the
// L1 services over one database, plus row-level helpers for picks, proposals and sounds. Synthetic data only.
package songstest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// World is one library with the L1 services over it. It is not safe for concurrent use by several goroutines of one
// test, but any number of Worlds can run in parallel tests.
type World struct {
	T        testing.TB
	St       *store.Store
	Sc       *librarytest.Scene
	Prov     *provenance.Service
	Muses    *muses.Service
	Songs    *songs.Service
	Sessions *sessions.Service
	Picks    *picks.Service
	View     *songview.Service
	Now      time.Time
	Path     string // the database file
	n        int
}

// New builds a World over an empty migrated database.
func New(t testing.TB) *World {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := Over(t, st)
	w.Path = path
	return w
}

// Over builds a World over an existing store (fixtures already applied).
func Over(t testing.TB, st *store.Store) *World {
	t.Helper()
	w := &World{T: t, St: st, Now: librarytest.FixedNow}
	w.Sc = librarytest.NewScene(t, st, w.Now)
	clock := func() time.Time { return w.Now }
	w.Muses = muses.New(st, clock, w.NewID)
	w.Songs = songs.New(st, clock, w.NewID)
	w.Sessions = sessions.New(st, clock)
	w.Picks = picks.New(st, nil, clock, w.NewID)
	w.View = songview.New(st, w.Muses, w.Songs, w.Sessions, w.Picks, nil, nil, nil, clock)
	w.Prov = provenance.New(provenance.Options{Store: st, Muses: w.Muses, Songs: w.Songs, Sessions: w.Sessions, Now: clock, NewID: w.NewID})
	return w
}

// NewID returns a fresh deterministic UUID-shaped id.
func (w *World) NewID() string {
	w.n++
	return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe30000+w.n)
}

// Settle drains the Reconciler queue.
func (w *World) Settle() {
	w.T.Helper()
	if _, err := w.Prov.Settle(context.Background()); err != nil {
		w.T.Fatalf("settle: %v", err)
	}
}

func (w *World) exec(q string, args ...any) {
	w.T.Helper()
	if _, err := w.St.W.Exec(q, args...); err != nil {
		w.T.Fatalf("%v\n%s", err, q)
	}
}

// Str returns the first column of the first row as a string ("" for NULL or no row).
func (w *World) Str(q string, args ...any) string {
	w.T.Helper()
	var s sql.NullString
	if err := w.St.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		w.T.Fatalf("%v\n%s", err, q)
	}
	return s.String
}

// Count returns an integer query result.
func (w *World) Count(q string, args ...any) int {
	w.T.Helper()
	var n int
	if err := w.St.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.T.Fatalf("%v\n%s", err, q)
	}
	return n
}

// SongOfTrack is the Song a take belongs to.
func (w *World) SongOfTrack(track string) string {
	w.T.Helper()
	return w.Str(`SELECT song_id FROM take_songs WHERE track_id = ?`, track)
}

// SongOfDraft is the live Song of a draft.
func (w *World) SongOfDraft(draft string) string {
	w.T.Helper()
	return w.Str(`SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL`, draft)
}

// RootSong creates a root take (one recipe, one take) titled title and settles; it returns (track, song).
func (w *World) RootSong(title string, at int) (track, song string) {
	w.T.Helper()
	r := w.Sc.Recipe(fmt.Sprintf("req_%d_%d", at, w.n), "pop")
	track = w.Sc.Track(r, title, "", w.Sc.At(at))
	w.Settle()
	return track, w.SongOfTrack(track)
}

// Pick records a Pick of take for song through a chained event (the C91 rows, written the way picks does).
func (w *World) Pick(song, take, source string) {
	w.T.Helper()
	prevTake, prevSource, seq := "", "none", 0
	var pt, ps sql.NullString
	var sq int
	err := w.St.R.QueryRow(`SELECT p.take_id, p.source, (SELECT MAX(seq) FROM pick_events WHERE song_id = p.song_id) FROM song_picks p WHERE p.song_id = ?`, song).Scan(&pt, &ps, &sq)
	hasRow := err == nil
	if hasRow {
		prevTake, prevSource, seq = pt.String, ps.String, sq
	}
	ev := w.NewID()
	var from, to any
	if prevTake != "" {
		from = prevTake
	}
	if take != "" {
		to = take
	}
	w.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES(?,?,?,?,?,?,?,?,?,'system:provenance','system','provenance','{"def":"mm.pick/1"}')`,
		ev, song, seq+1, w.Sc.At(5), from, to, prevSource, source, reasonOf(source, seq == 0))
	var pinned, pinnedBy, pinnedAt any
	if source == "pinned" {
		pinned, pinnedBy, pinnedAt = take, "human:h", w.Sc.At(5)
	}
	if !hasRow {
		w.exec(`INSERT INTO song_picks(song_id,take_id,source,pinned_take_id,pinned_by,pinned_at,evidence_json,last_event_id,changed_at,computed_at)
			VALUES(?,?,?,?,?,?,'{"def":"mm.pick/1"}',?,?,?)`, song, to, source, pinned, pinnedBy, pinnedAt, ev, w.Sc.At(5), w.Sc.At(5))
		return
	}
	w.exec(`UPDATE song_picks SET take_id=?, source=?, pinned_take_id=?, pinned_by=?, pinned_at=?, last_event_id=?, changed_at=?, computed_at=? WHERE song_id=?`,
		to, source, pinned, pinnedBy, pinnedAt, ev, w.Sc.At(5), w.Sc.At(5), song)
}

func reasonOf(source string, first bool) string {
	switch {
	case source == "none":
		return "emptied"
	case source == "pinned":
		return "pin"
	case first:
		return "initial"
	}
	return source
}

// Proposal inserts a pick proposal of an agent (state open | dismissed | accepted ...).
func (w *World) Proposal(song, take, principal, state string) string {
	w.T.Helper()
	id := w.NewID()
	var at, by any
	if state != "open" {
		at, by = w.Sc.At(6), "human:h"
	}
	w.exec(`INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at,decided_at,decided_by)
		VALUES(?,?,?,'synthetic rationale',?,?,?,?,?,?)`, id, song, take, principal, principal, state, w.Sc.At(4), at, by)
	return id
}

// Sound writes a song_sounds row (a derived cache row, written the way the sounds step does).
func (w *World) Sound(song, facet, value, origin, detail string) {
	w.T.Helper()
	var d any
	if detail != "" {
		d = detail
	}
	w.exec(`INSERT INTO song_sounds(song_id,facet,value,origin,detail_json) VALUES(?,?,?,?,?)`, song, facet, value, origin, d)
}

// TrackX inserts a take like Scene.Track with a lossy flag and a declared BPM (0 = none); both columns are frozen after insert.
func (w *World) TrackX(recipeID, title, parent, at string, lossy bool, bpm int) string {
	w.T.Helper()
	id := w.NewID()
	h := librarytest.Sha("t" + id)
	var par, decl any
	root := id
	gen := 0
	if parent != "" {
		par, gen = parent, 1
		root = w.Str(`SELECT root_track_id FROM tracks WHERE id = ?`, parent)
	}
	if bpm > 0 {
		decl = bpm
	}
	l := 0
	if lossy {
		l = 1
	}
	w.n++
	w.exec(`INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at,lossy,bpm_declared)
		VALUES(?,?,?,?,?,?,?,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','slug',?,?,?,?)`,
		id, "slug", title, recipeID, par, root, gen, 5000+w.n, "masters/"+id, h, h, at, at, l, decl)
	return id
}
