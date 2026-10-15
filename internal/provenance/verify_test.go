package provenance_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// activePicks and activeSounds are do-nothing steps that are not the package's own no-ops, so Verify treats the pick
// and sound invariants as live (a library whose steps are wired must satisfy them).
type activePicks struct{}

func (activePicks) RecomputeTx(context.Context, *sql.Tx, store.Env, []string, map[string]ladder.Strength) (picks.Changes, error) {
	return picks.Changes{}, nil
}

type activeSounds struct{}

func (activeSounds) ComputeTx(context.Context, *sql.Tx, store.Env, []string, []string) (sounds.Changes, error) {
	return sounds.Changes{}, nil
}
func (activeSounds) SweepStaleTx(context.Context, *sql.Tx, store.Env, int) (int, error) {
	return 0, nil
}

func withActiveSteps(o *provenance.Options) { o.Picks, o.Sounds = activePicks{}, activeSounds{} }

// settledWorld is a small settled library: two draft Songs, a root Song with a child and a second take.
type settled struct {
	*world
	root, child, other string
	draftA, draftB     string
	songRoot           string
}

func newSettled(t *testing.T) *settled {
	t.Helper()
	w := newWorld(t, withActiveSteps)
	sc := w.sc
	r := sc.Recipe("s_v1", "pop")
	s := &settled{world: w}
	s.root = sc.Track(r, "Root", "", sc.At(0))
	s.child = sc.Track(r, "Child", s.root, sc.At(2))
	s.other = sc.Track(sc.Recipe("o_v1", "pop"), "Other", "", sc.At(4))
	rd := sc.Recipe("da_v1", "pop")
	sc.Ref(rd, "i.csv", "Synthetic Muse 001", "Synthetic Artist 001", "variant-a")
	s.draftA = sc.Draft("da_v1", librarytest.DraftOpts{Recipe: rd})
	s.draftB = sc.Draft("db_v1", librarytest.DraftOpts{})
	w.settle()
	s.songRoot = w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, s.root)
	return s
}

// TestVerify_DetectsEachAnomaly plants one fault per invariant I1-I9 in a scratch library (dropping the trigger that
// guards the table where it forbids the fault) and expects the named code.
func TestVerify_DetectsEachAnomaly(t *testing.T) {
	t.Parallel()
	// the settled baseline with no pick rows would report pick_missing: give the Songs with takes a derived pick first
	type plant func(s *settled)
	cases := []struct {
		name, code string
		fn         plant
	}{
		{"I1 track without song", "track_without_song", func(s *settled) {
			s.sc.Track(s.sc.Recipe("x_v1", "pop"), "Fresh", "", s.sc.At(6)) // inserted after the last Settle
		}},
		{"I1 take in a retired song", "take_in_retired_song", func(s *settled) {
			// retire the (takes-less) draft Song of draftA, then attach a fresh root to it behind the guard
			a, b := s.str(`SELECT id FROM songs WHERE draft_id = ?`, s.draftA), s.str(`SELECT id FROM songs WHERE draft_id = ?`, s.draftB)
			s.exec(`UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, store.FormatTime(librarytest.FixedNow), b, a)
			s.dropTrigger("song_roots_live_song")
			x := s.sc.Track(s.sc.Recipe("rx_v1", "pop"), "Fresh", "", s.sc.At(6))
			s.exec(`INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'own','a',?)`, x, a, s.sc.At(6))
		}},
		{"I1 root without song_root", "root_without_song_root", func(s *settled) {
			s.dropTrigger("song_roots_no_delete")
			s.exec(`DELETE FROM song_roots WHERE root_track_id = ?`, s.root)
		}},
		{"I2 song without anchor", "song_without_anchor", func(s *settled) {
			s.dropTrigger("songs_draft_once")
			s.exec(`PRAGMA foreign_keys = OFF`)
			s.exec(`UPDATE songs SET draft_id = 'ghost' WHERE draft_id = ?`, s.draftB)
		}},
		{"I2 draft song should retire", "draft_song_should_retire", func(s *settled) {
			d := s.sc.Draft("fc_v1", librarytest.DraftOpts{Fulfilled: s.child})
			s.exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES('planted',?,'x','draft','a',?,'a',?)`, d, s.sc.At(0), s.sc.At(0))
		}},
		{"I3 duplicate live draft song", "duplicate_live_song_draft", func(s *settled) {
			s.exec(`DROP INDEX songs_draft_live`)
			s.exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES('dup',?,'x','draft','a',?,'a',?)`, s.draftB, s.sc.At(0), s.sc.At(0))
		}},
		{"I3 song for a mothered draft", "song_for_mothered_draft", func(s *settled) {
			d := s.sc.Draft("mo_v1", librarytest.DraftOpts{Parent: s.root})
			s.exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES('mo',?,'x','draft','a',?,'a',?)`, d, s.sc.At(0), s.sc.At(0))
		}},
		{"I4 pick missing", "pick_missing", func(s *settled) {}}, // the settled library has takes and no song_picks rows
		{"I4 pick stale", "pick_stale", func(s *settled) {
			s.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
				VALUES('e1',?,1,?,NULL,?,'none','newest','initial','system:provenance','system','provenance','{}')`, s.songRoot, s.sc.At(1), s.root)
			s.exec(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,'newest','{}','e1',?,?)`, s.songRoot, s.root, s.sc.At(1), s.sc.At(1))
		}},
		{"I5 pick chain broken", "pick_chain_broken", func(s *settled) {
			s.dropTrigger("pick_events_chain")
			s.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
				VALUES('e9',?,3,?,NULL,?,'none','newest','initial','system:provenance','system','provenance','{}')`, s.songRoot, s.sc.At(1), s.root)
		}},
		{"I5 pick event mismatch", "pick_event_mismatch", func(s *settled) {
			s.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
				VALUES('e1',?,1,?,NULL,?,'none','newest','initial','system:provenance','system','provenance','{}')`, s.songRoot, s.sc.At(1), s.child)
			s.exec(`INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES(?,?,'newest','{}','e1',?,?)`, s.songRoot, s.child, s.sc.At(1), s.sc.At(1))
			s.dropTrigger("song_picks_event_required")
			s.exec(`UPDATE song_picks SET source = 'stars', take_id = ?`, s.root)
		}},
		{"I6 muse key mismatch", "muse_key_mismatch", func(s *settled) {
			s.exec(`UPDATE muses SET key = 'zzz' WHERE kind = 'reference'`)
		}},
		{"I6 song muse missing", "song_muse_missing", func(s *settled) {
			s.exec(`PRAGMA foreign_keys = OFF`)
			s.exec(`UPDATE songs SET muse_id = 'ghost', muse_source = 'manual' WHERE draft_id = ?`, s.draftB)
		}},
		{"I7 session member missing", "session_member_missing", func(s *settled) {
			s.dropTrigger("session_members_exists")
			s.exec(`PRAGMA foreign_keys = OFF`)
			s.exec(`INSERT INTO sessions(id,kind,name,started_at,created_by,created_at,updated_by,updated_at) VALUES('sx','import','x',?,'a',?,'a',?)`, s.sc.At(1), s.sc.At(1), s.sc.At(1))
			s.exec(`INSERT INTO session_members(session_id,member_kind,member_id,added_at) VALUES('sx','track','ghost',?)`, s.sc.At(1))
		}},
		{"I7 run without session", "run_without_session", func(s *settled) { s.sc.Run("completed", 8) }},
		{"I7 batch without session", "batch_without_session", func(s *settled) {
			b := s.sc.Batch(`[{"item_index":0}]`, 1, "active")
			s.sc.Job(b, 0, "", "", s.sc.Recipe("jb_v1", "pop"))
		}},
		{"I8 invalid sound value", "sound_value_invalid", func(s *settled) {
			s.exec(`INSERT INTO take_sounds(track_id,facet,value,origin) VALUES(?,'era','bad','style')`, s.root)
		}},
		{"I8 sound stale", "sound_stale", func(s *settled) {}}, // sounds are wired but nothing computed
		{"I9 lease rows", "lease_rows", func(s *settled) {
			s.dropTrigger("provenance_lease_no_delete")
			s.exec(`DELETE FROM provenance_lease`)
		}},
		{"I9 open proposal on a dead take", "proposal_open_but_dead", func(s *settled) {
			s.exec(`INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES('pp',?,?,'why','agent:a','a','open',?)`, s.songRoot, s.child, s.sc.At(1))
			s.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, s.child)
			s.exec(`DELETE FROM provenance_dirty`) // the pick step would have expired it: pretend the queue is empty
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSettled(t)
			c.fn(s)
			if c.code != "track_without_song" && c.code != "run_without_session" && c.code != "batch_without_session" {
				s.exec(`DELETE FROM provenance_dirty`) // checks that need an empty queue (picks, sounds) run only then
			}
			an, err := s.prov.Verify(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !hasCode(an, c.code) {
				t.Errorf("planted %q, Verify reported %q", c.code, codes(an))
			}
		})
	}
}

// TestVerify_CleanWorldIsEmpty: a settled library with the no-op steps has no anomaly.
func TestVerify_CleanWorldIsEmpty(t *testing.T) {
	t.Parallel()
	w := l1World(t)
	w.settle()
	w.verifyEmpty()
}
