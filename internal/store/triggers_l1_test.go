package store

import (
	"context"
	"strings"
	"testing"
)

const l1ts = "2026-10-15T12:00:00.000Z"

// l1Track inserts a take; parent "" makes a root (generation 0), else a child of parent (generation 1, root = parent's root).
func l1Track(t *testing.T, s *Store, id, recipe, parent string) {
	t.Helper()
	if parent == "" {
		insertTrack(t, s, id, recipe, "T "+id, "f-"+id, 100+len(id))
		return
	}
	mustExec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES(?,?,?,?,?,(SELECT root_track_id FROM tracks WHERE id = ?),1,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','r',?,?)`,
		id, "slug", "T "+id, recipe, parent, parent, 500+len(id), "masters/"+id, "sha-"+id, "f-"+id, ts, ts)
}

func l1Song(s *Store, t *testing.T, id string, root, draft any) error {
	t.Helper()
	return exec(t, s, `INSERT INTO songs(id,root_track_id,draft_id,title,origin,created_by,created_at,updated_by,updated_at)
		VALUES(?,?,?,?,?,'system:provenance',?,'system:provenance',?)`, id, root, draft, "Song "+id, map[bool]string{true: "root", false: "draft"}[root != nil], l1ts, l1ts)
}

func l1Event(s *Store, t *testing.T, id, song string, seq int, fromTake any, fromSrc string, toTake any, toSrc, reason string) error {
	t.Helper()
	return exec(t, s, `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES(?,?,?,?,?,?,?,?,?,'system:provenance','system','provenance','{}')`, id, song, seq, l1ts, fromTake, toTake, fromSrc, toSrc, reason)
}

// l1World seeds one row in every L1 table (the DELETE test needs one each) on top of two lineages.
func l1World(t *testing.T, s *Store) {
	t.Helper()
	r := insertRecipe(t, s, "l1a", 1)
	r2 := insertRecipe(t, s, "l1b", 2)
	l1Track(t, s, "t1", r, "")
	l1Track(t, s, "t1c", r, "t1")
	l1Track(t, s, "t2", r2, "")
	mustExec(t, s, `INSERT INTO muses(id,kind,title,title_fold,artist,key,source,created_by,created_at,updated_by,updated_at)
		VALUES('m1','reference','Synthetic Muse 001','synthetic muse 001','Synthetic Artist 001','k1','human','human:h',?,'human:h',?)`, l1ts, l1ts)
	if err := l1Song(s, t, "s1", "t1", nil); err != nil {
		t.Fatal(err)
	}
	if err := l1Song(s, t, "s2", "t2", nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES('t1','s1','own','system:provenance',?),('t2','s2','own','system:provenance',?)`, l1ts, l1ts)
	if err := l1Event(s, t, "e1", "s1", 1, nil, "none", "t1c", "newest", "initial"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at) VALUES('s1','t1c','newest','{}','e1',?,?)`, l1ts, l1ts)
	mustExec(t, s, `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES('p1','s1','t1','why','agent:a1','a1','open',?)`, l1ts)
	mustExec(t, s, `INSERT INTO sessions(id,kind,source_key,name,started_at,created_by,created_at,updated_by,updated_at) VALUES('ses1','import','import:x','Import',?,'system:provenance',?,'system:provenance',?)`, l1ts, l1ts, l1ts)
	mustExec(t, s, `INSERT INTO session_members(session_id,member_kind,member_id,added_at) VALUES('ses1','track','t1',?)`, l1ts)
	mustExec(t, s, `INSERT INTO sound_taxonomy_versions(version,kind,seed_id,created_by,created_at) VALUES(1,'seed','seed/1','system:seed',?)`, l1ts)
	mustExec(t, s, `INSERT INTO sound_terms(version,term,facet,value,op) VALUES(1,'reggae','genre_family','reggae','add')`)
}

func TestTriggers_L1(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)

	for _, tb := range []string{"muses", "songs", "song_roots", "pick_events", "song_picks", "pick_proposals", "sessions", "session_members",
		"sound_taxonomy_versions", "sound_terms", "provenance_lease"} {
		expectErr(t, exec(t, s, `DELETE FROM `+tb), "")
	}
	for _, c := range []struct{ name, q, want string }{
		{"pick_events append-only", `UPDATE pick_events SET reason = 'pin' WHERE id = 'e1'`, "append-only"},
		{"session_members append-only", `UPDATE session_members SET added_at = 'x'`, "append-only"},
		{"sound_terms append-only", `UPDATE sound_terms SET value = 'x'`, "append-only"},
		{"versions append-only", `UPDATE sound_taxonomy_versions SET note = 'x'`, "append-only"},
		{"muses.kind frozen", `UPDATE muses SET kind = 'brief' WHERE id = 'm1'`, "frozen column"},
		{"songs.origin frozen", `UPDATE songs SET origin = 'draft' WHERE id = 's1'`, "frozen column"},
		{"sessions.source_key frozen", `UPDATE sessions SET source_key = 'z'`, "frozen column"},
		{"song_picks.song_id frozen", `UPDATE song_picks SET song_id = 's2'`, "frozen column"},
		{"pick_proposals.take_id frozen", `UPDATE pick_proposals SET take_id = 't2'`, "frozen column"},
		{"root set twice", `UPDATE songs SET root_track_id = 't2' WHERE id = 's1'`, "set once"},
		{"retire song with root", `UPDATE songs SET retired_at = '` + l1ts + `', retired_into = 's2' WHERE id = 's1'`, ""},
		{"seq gap", `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES('eg','s1',3,'` + l1ts + `','t1c','t1','newest','stars','stars','x','system','p','{}')`, "broken chain"},
		{"wrong from", `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES('ew','s1',2,'` + l1ts + `','t1','t1c','newest','stars','stars','x','system','p','{}')`, "broken chain"},
		{"to take outside the song", `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES('eo','s1',2,'` + l1ts + `','t1c','t2','newest','stars','stars','x','system','p','{}')`, "not in the song"},
		{"no-op event", `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
			VALUES('en','s1',2,'` + l1ts + `','t1c','t1c','newest','newest','newest','x','system','p','{}')`, "CHECK"},
		{"pick without first event", `INSERT INTO song_picks(song_id,take_id,source,evidence_json,last_event_id,changed_at,computed_at)
			VALUES('s2','t2','newest','{}','e1','` + l1ts + `','` + l1ts + `')`, "first event"},
		{"proposal take outside song", `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at)
			VALUES('px','s1','t2','why','agent:a2','a2','open','` + l1ts + `')`, "not in the song"},
		{"second open proposal", `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at)
			VALUES('py','s1','t1c','why','agent:a1','a1','open','` + l1ts + `')`, "UNIQUE"},
		{"seed kind needs a seed id", `INSERT INTO sound_taxonomy_versions(version,kind,created_by,created_at) VALUES(2,'seed','human:h','` + l1ts + `')`, "CHECK"},
		{"version gap", `INSERT INTO sound_taxonomy_versions(version,kind,seed_id,created_by,created_at) VALUES(5,'seed','seed/5','x','` + l1ts + `')`, "consecutive"},
		{"brief with artist", `INSERT INTO muses(id,kind,title,title_fold,artist,brief,key,source,created_by,created_at,updated_by,updated_at)
			VALUES('mb','brief','B','b','A','text','brief:mb','human','h','` + l1ts + `','h','` + l1ts + `')`, "CHECK"},
		{"reference with brief", `INSERT INTO muses(id,kind,title,title_fold,brief,key,source,created_by,created_at,updated_by,updated_at)
			VALUES('mr','reference','B','b','text','kr','human','h','` + l1ts + `','h','` + l1ts + `')`, "CHECK"},
		{"duplicate muse key", `INSERT INTO muses(id,kind,title,title_fold,key,source,created_by,created_at,updated_by,updated_at)
			VALUES('md','reference','B','b','k1','human','h','` + l1ts + `','h','` + l1ts + `')`, "UNIQUE"},
		{"unknown member", `INSERT INTO session_members(session_id,member_kind,member_id,added_at) VALUES('ses1','track','nope','` + l1ts + `')`, "unknown member"},
		{"unknown draft member", `INSERT INTO session_members(session_id,member_kind,member_id,added_at) VALUES('ses1','draft','nope','` + l1ts + `')`, "unknown member"},
		{"non-root song", `INSERT INTO songs(id,root_track_id,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES('sn','t1c','x','root','a','` + l1ts + `','a','` + l1ts + `')`, "not a lineage root"},
		{"duplicate root", `INSERT INTO songs(id,root_track_id,title,origin,created_by,created_at,updated_by,updated_at)
			VALUES('sd','t1','x','root','a','` + l1ts + `','a','` + l1ts + `')`, "UNIQUE"},
	} {
		err := exec(t, s, c.q)
		if err == nil {
			t.Errorf("%s: want an error", c.name)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q lacks %q", c.name, err, c.want)
		}
	}

	// evidence-only update of a pick is fine; changing the take without an event is not
	mustExec(t, s, `UPDATE song_picks SET evidence_json = '{"a":1}', computed_at = ? WHERE song_id = 's1'`, l1ts)
	expectErr(t, exec(t, s, `UPDATE song_picks SET take_id = 't1' WHERE song_id = 's1'`), "needs a matching new event")
	// an old version refuses terms once a newer one exists
	mustExec(t, s, `INSERT INTO sound_taxonomy_versions(version,kind,created_by,created_at) VALUES(2,'extension','human:h',?)`, l1ts)
	expectErr(t, exec(t, s, `INSERT INTO sound_terms(version,term,facet,value,op) VALUES(1,'dub','genre_family','reggae','add')`), "only the newest version")
	mustExec(t, s, `INSERT INTO sound_terms(version,term,facet,value,op) VALUES(2,'dub','genre_family','reggae','add')`)
}

func TestTriggers_L1_PickChain(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)
	// a legal second event and the matching song_picks update
	if err := l1Event(s, t, "e2", "s1", 2, "t1c", "newest", "t1", "stars", "stars"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE song_picks SET take_id='t1', source='stars', last_event_id='e2', changed_at=? WHERE song_id='s1'`, l1ts)
	// a stale from: event 3 claims the pick was still t1c
	if err := l1Event(s, t, "e3", "s1", 3, "t1c", "newest", "t1c", "pinned", "pin"); err == nil {
		t.Errorf("event with a stale from must be refused")
	}
	// an event that is not the newest of the Song cannot back an update
	if err := l1Event(s, t, "e3", "s1", 3, "t1", "stars", "t1c", "ladder", "ladder"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE song_picks SET take_id='t1c', source='ladder', last_event_id='e2' WHERE song_id='s1'`), "needs a matching new event")
	mustExec(t, s, `UPDATE song_picks SET take_id='t1c', source='ladder', last_event_id='e3' WHERE song_id='s1'`)
	// source pinned requires take = pinned take
	if err := l1Event(s, t, "e4", "s1", 4, "t1c", "ladder", "t1", "pinned", "pin"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE song_picks SET take_id='t1', source='pinned', last_event_id='e4', pinned_take_id='t1c', pinned_by='h', pinned_at=? WHERE song_id='s1'`, l1ts), "CHECK")
	mustExec(t, s, `UPDATE song_picks SET take_id='t1', source='pinned', last_event_id='e4', pinned_take_id='t1', pinned_by='h', pinned_at=? WHERE song_id='s1'`, l1ts)
}

func TestTriggers_L1_Songs(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	r := insertRecipe(t, s, "sg", 1)
	l1Track(t, s, "t1", r, "")
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','x_v1','{"id":"x_v1"}',?,?)`, ts, ts)
	// two live Songs for one draft refused; a retired + live pair is fine
	if err := l1Song(s, t, "sa", nil, "d1"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, l1Song(s, t, "sb", nil, "d1"), "UNIQUE")
	if err := l1Song(s, t, "sc", "t1", nil); err != nil {
		t.Fatal(err)
	}
	// retire sa into sc: allowed (takes-less), then the retired song is immutable
	mustExec(t, s, `UPDATE songs SET retired_at = ?, retired_into = 'sc' WHERE id = 'sa'`, l1ts)
	expectErr(t, exec(t, s, `UPDATE songs SET title = 'x' WHERE id = 'sa'`), "immutable")
	if err := l1Song(s, t, "sd", nil, "d1"); err != nil {
		t.Fatalf("a retired + live pair for one draft must be ok: %v", err)
	}
	// retire target retired
	expectErr(t, exec(t, s, `UPDATE songs SET retired_at = ?, retired_into = 'sa' WHERE id = 'sd'`, l1ts), "retire target is retired")
	// draft_id is set once
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d2','import_backlog','y_v1','{"id":"y_v1"}',?,?)`, ts, ts)
	expectErr(t, exec(t, s, `UPDATE songs SET draft_id = 'd2' WHERE id = 'sd'`), "set once")
}

func TestTriggers_L1_Rev2(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)
	// song_roots: update/delete refused, non-root refused, retired Song refused
	expectErr(t, exec(t, s, `UPDATE song_roots SET via = 'draft' WHERE root_track_id = 't1'`), "append-only")
	expectErr(t, exec(t, s, `DELETE FROM song_roots WHERE root_track_id = 't1'`), "no physical delete")
	expectErr(t, exec(t, s, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES('t1c','s1','own','x',?)`, l1ts), "not a lineage root")
	r3 := insertRecipe(t, s, "l1c", 3)
	l1Track(t, s, "t3", r3, "")
	l1Track(t, s, "t4", r3, "")
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','x_v1','{"id":"x_v1"}',?,?)`, ts, ts)
	if err := l1Song(s, t, "sx", nil, "d1"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE songs SET retired_at = ?, retired_into = 's1' WHERE id = 'sx'`, l1ts)
	expectErr(t, exec(t, s, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES('t3','sx','draft','x',?)`, l1ts), "song is retired")
	// a Song that owns a song_roots row cannot retire (even with root_track_id NULL)
	if err := l1Song(s, t, "sy", nil, nil); err == nil {
		t.Errorf("a song needs a root or a draft")
	}
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d2','import_backlog','z_v1','{"id":"z_v1"}',?,?)`, ts, ts)
	if err := l1Song(s, t, "sz", nil, "d2"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES('t3','sz','draft','x',?)`, l1ts)
	expectErr(t, exec(t, s, `UPDATE songs SET retired_at = ?, retired_into = 's1' WHERE id = 'sz'`, l1ts), "cannot retire")
	// a second root of the same Song is accepted by the take-in-song triggers and a take of another Song refused
	mustExec(t, s, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES('t4','sz','draft','x',?)`, l1ts)
	if err := l1Event(s, t, "ez", "sz", 1, nil, "none", "t4", "newest", "initial"); err != nil {
		t.Errorf("second root must be accepted: %v", err)
	}
	if err := l1Event(s, t, "ez2", "sz", 2, "t4", "newest", "t2", "stars", "stars"); err == nil {
		t.Errorf("a take of another Song must be refused")
	}
	expectErr(t, exec(t, s, `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES('pz','sz','t2','w','agent:q','q','open',?)`, l1ts), "not in the song")
	mustExec(t, s, `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES('pz','sz','t4','w','agent:q','q','open',?)`, l1ts)
	// reasons
	expectErr(t, exec(t, s, `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES('er','sz',2,?, 't4','t3','newest','pinned','pin_lost','x','system','p','{}')`, l1ts), "CHECK")
	// proposal state expired ok; second provenance_lease row refused
	mustExec(t, s, `UPDATE pick_proposals SET state='expired', decided_at=?, decided_by='system:provenance' WHERE id='pz'`, l1ts)
	expectErr(t, exec(t, s, `INSERT INTO provenance_lease(id,owner,epoch,expires_at) VALUES(2,'o',1,?)`, l1ts), "CHECK")
	// songs: muse_id set with muse_source NULL; angle_source 'cleared' with an angle
	expectErr(t, exec(t, s, `UPDATE songs SET muse_id = 'm1' WHERE id = 's1'`), "CHECK")
	mustExec(t, s, `UPDATE songs SET muse_id = 'm1', muse_source = 'manual' WHERE id = 's1'`)
	expectErr(t, exec(t, s, `UPDATE songs SET angle = 'x', angle_source = 'cleared' WHERE id = 's1'`), "CHECK")
	mustExec(t, s, `UPDATE songs SET angle = 'x', angle_source = 'manual' WHERE id = 's1'`)
	mustExec(t, s, `UPDATE songs SET angle = NULL, angle_source = 'cleared' WHERE id = 's1'`)
	// muse key frozen for briefs; reference key may change
	mustExec(t, s, `INSERT INTO muses(id,kind,title,title_fold,brief,key,source,created_by,created_at,updated_by,updated_at) VALUES('mb','brief','B','b','text','brief:mb','human','h',?,'h',?)`, l1ts, l1ts)
	expectErr(t, exec(t, s, `UPDATE muses SET key = 'other' WHERE id = 'mb'`), "frozen column")
	mustExec(t, s, `UPDATE muses SET key = 'k1b' WHERE id = 'm1'`)
}

func TestTriggers_L1_PinReasons(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)
	// chain: newest(t1c) -> pinned(t1) via 'pin' -> newest(t1c) via 'pin_ineligible' -> pinned(t1) via 'pin_restored'
	steps := []struct {
		id, fromTake, fromSrc, toTake, toSrc, reason string
	}{
		{"e2", "t1c", "newest", "t1", "pinned", "pin"},
		{"e3", "t1", "pinned", "t1c", "newest", "pin_ineligible"},
		{"e4", "t1c", "newest", "t1", "pinned", "pin_restored"},
	}
	for i, st := range steps {
		if err := l1Event(s, t, st.id, "s1", i+2, st.fromTake, st.fromSrc, st.toTake, st.toSrc, st.reason); err != nil {
			t.Fatalf("%s: %v", st.reason, err)
		}
	}
}

func TestQueueTriggers_Table(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	ctx := context.Background()
	count := func(kind, id string) (n int, at string, tries int) {
		t.Helper()
		err := s.R.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(at), ''), COALESCE(MAX(tries), 0) FROM provenance_dirty WHERE kind = ? AND id = ?`, kind, id).Scan(&n, &at, &tries)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	reset := func() {
		mustExec(t, s, `UPDATE provenance_dirty SET tries = 0`)
		mustExec(t, s, `DELETE FROM provenance_dirty`)
	}
	r := insertRecipe(t, s, "q1", 1)
	l1Track(t, s, "t1", r, "")
	if n, _, _ := count("track", "t1"); n != 1 {
		t.Fatalf("track insert: %d rows", n)
	}
	// a re-mark overwrites `at` (one row per key) and resets tries
	mustExec(t, s, `UPDATE provenance_dirty SET at = '2000-01-01T00:00:00.000Z', tries = 3 WHERE kind='track' AND id='t1'`)
	mustExec(t, s, `UPDATE tracks SET status = 'final' WHERE id = 't1'`)
	if n, at, tries := count("track", "t1"); n != 1 || at == "2000-01-01T00:00:00.000Z" || tries != 0 {
		t.Errorf("status update: n=%d at=%s tries=%d", n, at, tries)
	}
	reset()
	// ratings: human rows enqueue, agent rows do not
	mustExec(t, s, `INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES('t1','agent',3,'agent','agent:a','a',?,?)`, ts, ts)
	if n, _, _ := count("track", "t1"); n != 0 {
		t.Errorf("agent rating must not enqueue")
	}
	mustExec(t, s, `INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES('t1','human',4,'human','human:h','h',?,?)`, ts, ts)
	if n, _, _ := count("track", "t1"); n != 1 {
		t.Errorf("human rating insert must enqueue")
	}
	reset()
	mustExec(t, s, `UPDATE ratings SET stars = 5 WHERE track_id='t1' AND source='human'`)
	if n, _, _ := count("track", "t1"); n != 1 {
		t.Errorf("human stars update must enqueue")
	}
	reset()
	mustExec(t, s, `UPDATE ratings SET stars = 2 WHERE track_id='t1' AND source='agent'`)
	if n, _, _ := count("track", "t1"); n != 0 {
		t.Errorf("agent stars update must not enqueue")
	}
	// analyses: tempo yes, loudness no
	mustExec(t, s, `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1','loudness','v','A','{}',?)`, ts)
	if n, _, _ := count("track", "t1"); n != 0 {
		t.Errorf("loudness must not enqueue")
	}
	mustExec(t, s, `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1','tempo','mm-tempo/1','A','{}',?)`, ts)
	if n, _, _ := count("track", "t1"); n != 1 {
		t.Errorf("tempo must enqueue")
	}
	reset()
	// drafts: insert, parent update, status update
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','x_v1','{"id":"x_v1"}',?,?)`, ts, ts)
	if n, _, _ := count("draft", "d1"); n != 1 {
		t.Errorf("draft insert")
	}
	reset()
	mustExec(t, s, `UPDATE recipe_drafts SET parent_track_id = 't1' WHERE id = 'd1'`)
	if n, _, _ := count("draft", "d1"); n != 1 {
		t.Errorf("draft parent update")
	}
	reset()
	mustExec(t, s, `UPDATE recipe_drafts SET status = 'archived' WHERE id = 'd1'`)
	if n, _, _ := count("draft", "d1"); n != 1 {
		t.Errorf("draft status update")
	}
	reset()
	// collections: collection + draft rows
	mustExec(t, s, `INSERT INTO draft_collections(draft_id,name,source) VALUES('d1','Synthetic Collection','c.json')`)
	if n, _, _ := count("collection", "Synthetic Collection"); n != 1 {
		t.Errorf("collection key")
	}
	if n, _, _ := count("draft", "d1"); n != 1 {
		t.Errorf("collection insert must also mark the draft")
	}
	reset()
	// runs
	insertRun(t, s, "run1", "running")
	if n, _, _ := count("run", "run1"); n != 1 {
		t.Errorf("run insert")
	}
	reset()
	mustExec(t, s, `UPDATE import_runs SET state = 'completed', finished_at = ? WHERE id = 'run1'`, ts)
	if n, _, _ := count("run", "run1"); n != 1 {
		t.Errorf("run update")
	}
	reset()
	// recipe_refs: the drafts of that recipe and its generation-0 tracks
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,source_recipe_id,request_id,request_json,created_at,updated_at) VALUES('d3','import_backlog',?,'w_v1','{"id":"w_v1"}',?,?)`, r, ts, ts)
	reset()
	mustExec(t, s, `INSERT INTO recipe_refs(recipe_id,source,ref_json,created_at) VALUES(?,?,'{"source_title":"x"}',?)`, r, "i.csv", ts)
	if n, _, _ := count("draft", "d3"); n != 1 {
		t.Errorf("recipe_refs insert must mark the draft of that recipe")
	}
	if n, _, _ := count("track", "t1"); n != 1 {
		t.Errorf("recipe_refs insert must mark the generation-0 track of that recipe")
	}
}

// batches and jobs triggers share the M3 world helpers
func TestQueueTriggers_BatchesAndJobs(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	r := insertRecipe(t, s, "qb", 1)
	if err := m3Estimate(t, s, "e1", 25); err != nil {
		t.Fatal(err)
	}
	if err := m3Batch(t, s, "b1", "e1", "active", 25); err != nil {
		t.Fatal(err)
	}
	var n int
	row := func(k, id string) int {
		if err := s.R.QueryRow(`SELECT COUNT(*) FROM provenance_dirty WHERE kind=? AND id=?`, k, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if row("batch", "b1") != 1 {
		t.Fatal("batch insert must enqueue")
	}
	mustExec(t, s, `DELETE FROM provenance_dirty`)
	if err := m3Job(t, s, "j1", "b1", 0, r, "queued", nil); err != nil {
		t.Fatal(err)
	}
	if row("batch", "b1") != 0 {
		t.Fatal("a job insert alone does not enqueue")
	}
	mustExec(t, s, `UPDATE batches SET state = 'finished', finished_at = ? WHERE id = 'b1'`, m3ts)
	if row("batch", "b1") != 1 {
		t.Fatal("batch state update must enqueue")
	}
}
