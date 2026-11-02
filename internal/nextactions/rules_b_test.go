package nextactions_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var (
	agentRead  = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead}}
	agentWrite = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}}
)

func songByTitle(t *testing.T, r *servertest.Rig, label string) string {
	return str(t, r, `SELECT id FROM songs WHERE title = ?`, "Synthetic Song "+label)
}

func recordByTitle(t *testing.T, r *servertest.Rig, title string) string {
	return str(t, r, `SELECT id FROM records WHERE title = ?`, title)
}

// cloneTake inserts another take of the Song whose root is root (a child of the root, so take_songs maps it to the Song),
// a copy of the take model with fresh audio identity. It returns the new take id.
func cloneTake(t *testing.T, r *servertest.Rig, model, parent, root string, generation int, id string) string {
	t.Helper()
	exec(t, r, `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status,status_set_by,origin,duration_s,sample_rate,bits_per_sample,
			channels,total_samples,master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		SELECT ?, slug||'-c', title, recipe_id, NULLIF(?, ''), ?, ?, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample, channels, total_samples,
			master_path||'.c', master_sha256||'c', master_bytes, source_sha256, frames_sha256||'c', audio_md5, import_rule, '2026-10-07T10:00:00Z', '2026-10-07T10:00:00Z'
		FROM tracks WHERE id = ?`, id, parent, root, generation, model)
	return id
}

// renderDraft gives the draft Song a first take, as a finished render would.
func renderDraft(t *testing.T, r *servertest.Rig, songLabel, model, id string) {
	t.Helper()
	cloneTake(t, r, model, "", id, 0, id)
	exec(t, r, `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at) VALUES(?,?,'draft','system:provenance','2026-10-07T10:00:00Z')`, id, songByTitle(t, r, songLabel))
}

const (
	tk1 = "01900000-0000-7000-8000-00000000c001"
	tk2 = "01900000-0000-7000-8000-00000000c002"
	tk3 = "01900000-0000-7000-8000-00000000c003"
)

func addVerdict(t *testing.T, r *servertest.Rig, n int, a, b, source string, blind int) {
	t.Helper()
	id := fmt.Sprintf("01900000-0000-7000-8000-00000000e%03x", n)
	exec(t, r, `INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES(?,?,?,0,?,0,1000,'mm-clip/1','ffmpeg t','off',0,0,'human:fixture','human','2026-10-07T10:00:00Z','2026-11-07T10:00:00Z')`, id, a, b, blind)
	kind, pid := "human", "human:fixture"
	if source == "critic" {
		kind, pid = "agent", "agent:t"
	}
	exec(t, r, `INSERT INTO duel_verdicts(id,duel_id,source,principal_kind,principal_id,verdict,winner_track_id,saw_reveal,created_at) VALUES(?,?,?,?,?,'a',?,0,'2026-10-07T10:00:00Z')`,
		id+"v", id, source, kind, pid, a)
}

func hasSubject(as []nextactions.Action, id string) bool { return slices.Contains(subjects(as), id) }

func onlyCode(t *testing.T, r *servertest.Rig, p auth.Principal, code string) []nextactions.Action {
	t.Helper()
	return byCode(list(t, r, p, nextactions.Filter{Limit: 50}), code)
}

// rulesTableB is the NA2 half of TestNextActions_Rules_Table: for each of the six rules, fires, does not fire, text and hint.
func rulesTableB(t *testing.T) {
	t.Run("song_duel", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		s3, s1 := songByTitle(t, r, "S3"), songByTitle(t, r, "S1")
		as := onlyCode(t, r, agentWrite, "song_duel")
		if !hasSubject(as, s3) || hasSubject(as, s1) {
			t.Fatalf("song_duel on %v: want S3 (two takes), not S1 (one take)", subjects(as))
		}
		var a nextactions.Action
		for _, x := range as {
			if *x.Subject.ID == s3 {
				a = x
			}
		}
		newest := str(t, r, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.recorded_at DESC, t.created_at DESC, t.id DESC LIMIT 1`, s3)
		second := str(t, r, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.recorded_at DESC, t.created_at DESC, t.id DESC LIMIT 1 OFFSET 1`, s3)
		want := `Song "Synthetic Song S3" has 2 takes and 0 blind duel(s) — 3 decide the Pick; start one between the two newest`
		if a.Text != want || a.Priority != 68 || a.Audience != nextactions.AudienceBoth || a.Evidence["takes"] != 2 || a.Evidence["duels"] != 0 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "create_duel" || a.Hint.Args["a_track_id"] != newest || a.Hint.Args["b_track_id"] != second || a.Hint.Args["blind"] != true {
			t.Errorf("hint %+v, want create_duel between %s and %s", a.Hint, newest, second)
		}
		if h := byCode(list(t, r, agentRead, nextactions.Filter{SubjectKind: "song", SubjectID: s3}), "song_duel"); len(h) != 1 || h[0].Hint == nil || h[0].Hint.Op != "get_song" {
			t.Errorf("a read-only viewer's hint: %+v", h)
		}
		if len(onlyCode(t, r, humanP, "song_duel")) == 0 {
			t.Error("song_duel is for both audiences")
		}
		// only counted verdicts: human, blind, between takes of the Song. A critic's verdict and a non-blind one do not count.
		addVerdict(t, r, 1, newest, second, "critic", 1)
		addVerdict(t, r, 2, newest, second, "human", 0)
		addVerdict(t, r, 3, newest, second, "human", 1)
		addVerdict(t, r, 4, second, newest, "human", 1)
		as = byCode(list(t, r, agentWrite, nextactions.Filter{SubjectKind: "song", SubjectID: s3}), "song_duel")
		if len(as) != 1 || as[0].Evidence["duels"] != 2 {
			t.Fatalf("after two counted verdicts: %+v", as)
		}
		addVerdict(t, r, 5, newest, second, "human", 1)
		if as := byCode(list(t, r, agentWrite, nextactions.Filter{SubjectKind: "song", SubjectID: s3}), "song_duel"); len(as) != 0 {
			t.Errorf("three counted verdicts decide the Pick: %+v", as)
		}
	})
	t.Run("song_duel does not fire", func(t *testing.T) {
		t.Parallel()
		for name, mutate := range map[string]func(t *testing.T, r *servertest.Rig, song, take string){
			"a take is discarded": func(t *testing.T, r *servertest.Rig, _, take string) {
				exec(t, r, `UPDATE tracks SET status = 'discarded' WHERE id = ?`, take)
			},
			"the Song is hidden": func(t *testing.T, r *servertest.Rig, song, _ string) {
				exec(t, r, `UPDATE songs SET hidden_at = '2026-10-07T10:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, song)
			},
			"the Song is pinned": func(t *testing.T, r *servertest.Rig, song, take string) {
				if _, err := r.Services.Picks.Pin(context.Background(), humanP, song, &take, nil); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				r := l3Rig(t)
				s3 := songByTitle(t, r, "S3")
				take := str(t, r, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.id LIMIT 1`, s3)
				if !hasSubject(onlyCode(t, r, agentRead, "song_duel"), s3) {
					t.Fatal("baseline: S3 does not fire")
				}
				mutate(t, r, s3, take)
				if hasSubject(onlyCode(t, r, agentRead, "song_duel"), s3) {
					t.Error("still fires")
				}
			})
		}
	})
	t.Run("muse_record_candidate", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		m5, m4 := museByTitle(t, r, lt.L3Muse5Title), museByTitle(t, r, lt.L3Muse4Title)
		as := onlyCode(t, r, agentRead, "muse_record_candidate")
		if len(as) != 1 || *as[0].Subject.ID != m5 || as[0].Evidence["songs"] != 6 {
			t.Fatalf("want M5 with its 6 strong Songs (T7 is 3 stars, T8 unrated): %+v", as)
		}
		a := as[0]
		if a.Text != fmt.Sprintf("Muse %s has 6 Songs rated 4★+ that are in no Record — build one: create_record, then propose_record with a sequence (arc)", m5[28:]) || a.Priority != 75 {
			t.Errorf("text %q priority %d", a.Text, a.Priority)
		}
		if a.Hint == nil || a.Hint.Op != "list_songs" || a.Hint.Args["muse_id"] != m5 || a.Hint.Args["min_stars"] != 4 || a.Hint.Args["in_record"] != false {
			t.Errorf("hint %+v", a.Hint)
		}
		if hasSubject(as, m4) {
			t.Error("M4 has no strong Song")
		}
		if len(onlyCode(t, r, humanP, "muse_record_candidate")) != 1 {
			t.Error("for both audiences")
		}
		// a Record that holds a strong Song takes it out of the count; three strong Songs are enough, two are not
		rec := recordByTitle(t, r, "Synthetic Record One")
		slot := func(n int, label string) {
			exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
				fmt.Sprintf("01900000-0000-7000-8000-00000000d%03x", n), rec, songByTitle(t, r, label))
			exec(t, r, `UPDATE records SET order_json = (SELECT json_group_array(s.id) FROM record_slots s WHERE s.record_id = ? AND s.removed_at IS NULL) WHERE id = ?`, rec, rec)
		}
		slot(1, "T1")
		slot(2, "T2")
		slot(3, "T3")
		if as := onlyCode(t, r, agentRead, "muse_record_candidate"); len(as) != 1 || as[0].Evidence["songs"] != 3 {
			t.Fatalf("three left: %+v", as)
		}
		slot(4, "T4")
		if as := onlyCode(t, r, agentRead, "muse_record_candidate"); len(as) != 0 {
			t.Errorf("two left: %+v", as)
		}
		exec(t, r, `UPDATE records SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, rec)
		if as := onlyCode(t, r, agentRead, "muse_record_candidate"); len(as) != 1 || as[0].Evidence["songs"] != 6 {
			t.Errorf("a hidden Record holds nothing: %+v", as)
		}
		exec(t, r, `UPDATE ratings SET stars = 3 WHERE track_id IN (SELECT root_track_id FROM songs WHERE title IN ('Synthetic Song T1','Synthetic Song T2','Synthetic Song T3','Synthetic Song T4'))`)
		if as := onlyCode(t, r, agentRead, "muse_record_candidate"); len(as) != 0 {
			t.Errorf("rated down: %+v", as)
		}
	})
	t.Run("muse_partial_angles", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		m3, m4, m5 := museByTitle(t, r, lt.L3Muse3Title), museByTitle(t, r, lt.L3Muse4Title), museByTitle(t, r, lt.L3Muse5Title)
		as := onlyCode(t, r, agentRead, "muse_partial_angles")
		if len(as) != 1 || *as[0].Subject.ID != m4 {
			t.Fatalf("want M4 (u1 rendered, u2 draft), not M3 (no take) or M5 (no draft): %v", subjects(as))
		}
		if as[0].Text != fmt.Sprintf("Muse %s: untagged rendered, untagged still draft — estimate 1 more", m4[28:]) {
			t.Errorf("text %q", as[0].Text)
		}
		exec(t, r, `UPDATE songs SET angle = 'remake-long', angle_source = 'manual' WHERE title = 'Synthetic Song u1'`)
		exec(t, r, `UPDATE songs SET angle = 'long', angle_source = 'manual' WHERE title = 'Synthetic Song u2'`)
		a := onlyCode(t, r, agentWrite, "muse_partial_angles")[0]
		d := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song u2'`)
		if a.Text != fmt.Sprintf("Muse %s: remake-long rendered, long still draft — estimate 1 more", m4[28:]) || a.Priority != 60 || a.Audience != nextactions.AudienceAgent ||
			a.Evidence["rendered"] != 1 || a.Evidence["drafts"] != 1 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "estimate_generation" || fmt.Sprint(a.Hint.Args) != fmt.Sprint(map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{d}}}}) {
			t.Errorf("hint %+v", a.Hint)
		}
		if h := onlyCode(t, r, agentRead, "muse_partial_angles")[0].Hint; h == nil || h.Op != "get_muse" || h.Args["id"] != m4 {
			t.Errorf("read-only hint %+v", h)
		}
		if len(onlyCode(t, r, humanP, "muse_partial_angles")) != 0 {
			t.Error("muse_partial_angles is for agents")
		}
		// the angle list shows three and an ellipsis: render M3's drafts partially, then give its drafts four distinct angles
		_ = m3
		_ = m5
		renderDraft(t, r, "d1", str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song T1'`), tk1)
		exec(t, r, `UPDATE songs SET angle = 'remake-long', angle_source = 'manual' WHERE title = 'Synthetic Song d1'`)
		exec(t, r, `UPDATE songs SET angle = 'alt-take-long', angle_source = 'manual' WHERE title = 'Synthetic Song d2'`)
		exec(t, r, `UPDATE songs SET angle = 'long', angle_source = 'manual' WHERE title = 'Synthetic Song d3'`)
		exec(t, r, `UPDATE songs SET muse_id = ?, muse_source = 'manual', angle = 'short', angle_source = 'manual' WHERE title = 'Synthetic Song u2'`, m3)
		exec(t, r, `UPDATE songs SET angle = 'acoustic', angle_source = 'manual', muse_id = ?, muse_source = 'manual' WHERE title = 'Synthetic Song d4'`, m3)
		got := byCode(list(t, r, agentRead, nextactions.Filter{SubjectKind: "muse", SubjectID: m3}), "muse_partial_angles")
		if len(got) != 1 || got[0].Text != fmt.Sprintf("Muse %s: remake-long rendered, alt-take-long, long, short, … still draft — estimate 4 more", m3[28:]) {
			t.Errorf("%+v", got)
		}
		if muse := onlyCode(t, r, agentRead, "muse_unrendered"); hasSubject(muse, m3) {
			t.Error("a Muse with a rendered Song is no longer unrendered")
		}
	})
	t.Run("muse_partial_angles does not fire with a queued job", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		m4 := museByTitle(t, r, lt.L3Muse4Title)
		draft := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song u2'`)
		exec(t, r, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
				expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
			SELECT 'job-na2', batch_id, 99, kind, engine, backend, recipe_id, ?, 'from_draft', '{}', 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd,
			       'queued', next_attempt_at, queued_at, created_at, updated_at FROM jobs LIMIT 1`, draft)
		if hasSubject(onlyCode(t, r, agentRead, "muse_partial_angles"), m4) {
			t.Error("fires with a queued job")
		}
		exec(t, r, `UPDATE muses SET hidden_at='2026-10-07T11:00:00Z', hidden_by='human:fixture' WHERE id = ?`, m4)
		if hasSubject(onlyCode(t, r, agentRead, "muse_partial_angles"), m4) {
			t.Error("fires for a hidden Muse")
		}
	})
	t.Run("record_undecided_slots", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		one, two := recordByTitle(t, r, "Synthetic Record One"), recordByTitle(t, r, "Synthetic Record Two")
		as := onlyCode(t, r, agentRead, "record_undecided_slots")
		if !slices.Equal(subjects(as), slices.Sorted(slices.Values([]string{one, two}))) {
			t.Fatalf("subjects %v", subjects(as))
		}
		for _, a := range as {
			title := map[string]string{one: "Synthetic Record One", two: "Synthetic Record Two"}[*a.Subject.ID]
			if a.Text != fmt.Sprintf(`Record "%s": 2 slot(s) play an undecided Pick — duel them, pin them, or freeze the Record (pin_all)`, title) || a.Priority != 80 || a.Evidence["slots"] != 2 {
				t.Errorf("%q %v", a.Text, a.Evidence)
			}
			if a.Hint == nil || a.Hint.Op != "get_record" || a.Hint.Args["id"] != *a.Subject.ID || fmt.Sprint(a.Hint.Args["include"]) != "[changes]" {
				t.Errorf("hint %+v", a.Hint)
			}
		}
		if len(onlyCode(t, r, humanP, "record_undecided_slots")) != 2 {
			t.Error("for both audiences")
		}
		// pinning a slot decides it; hiding the Record silences it
		rows, err := r.Store.R.Query(`SELECT id, song_id FROM record_slots WHERE record_id = ? AND removed_at IS NULL AND pinned_take_id IS NULL
			AND song_id IN (SELECT song_id FROM song_picks WHERE source IN ('newest','stars') AND take_id IS NOT NULL) ORDER BY id`, two)
		if err != nil {
			t.Fatal(err)
		}
		type pair struct{ slot, song string }
		var ps []pair
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.slot, &p.song); err != nil {
				t.Fatal(err)
			}
			ps = append(ps, p)
		}
		_ = rows.Close()
		take := str(t, r, `SELECT take_id FROM song_picks WHERE song_id = ?`, ps[0].song)
		exec(t, r, `UPDATE record_slots SET pinned_take_id = ?, pinned_by = 'human:fixture', pinned_at = '2026-10-07T10:00:00Z' WHERE id = ?`, take, ps[0].slot)
		if as := byCode(list(t, r, agentRead, nextactions.Filter{SubjectKind: "record", SubjectID: two}), "record_undecided_slots"); len(as) != 1 || as[0].Evidence["slots"] != 1 {
			t.Fatalf("one pinned: %+v", as)
		}
		take = str(t, r, `SELECT take_id FROM song_picks WHERE song_id = ?`, ps[1].song)
		exec(t, r, `UPDATE record_slots SET pinned_take_id = ?, pinned_by = 'human:fixture', pinned_at = '2026-10-07T10:00:00Z' WHERE id = ?`, take, ps[1].slot)
		if hasSubject(onlyCode(t, r, agentRead, "record_undecided_slots"), two) {
			t.Error("a fully pinned Record is decided")
		}
		exec(t, r, `UPDATE records SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, one)
		if hasSubject(onlyCode(t, r, agentRead, "record_undecided_slots"), one) {
			t.Error("a hidden Record is silent")
		}
	})
	t.Run("record_empty_slot", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		one := recordByTitle(t, r, "Synthetic Record One")
		as := onlyCode(t, r, agentRead, "record_empty_slot")
		if len(as) != 1 || *as[0].Subject.ID != one {
			t.Fatalf("want Record One only: %v", subjects(as))
		}
		a := as[0]
		if a.Text != `Record "Synthetic Record One" slot 4 has no take yet — render the Song or remove the slot` || a.Priority != 60 || a.Evidence["slot"] != 4 || a.Evidence["empty"] != 1 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "get_record" || a.Hint.Args["id"] != one || len(a.Hint.Args) != 1 {
			t.Errorf("hint %+v", a.Hint)
		}
		if len(onlyCode(t, r, humanP, "record_empty_slot")) != 1 {
			t.Error("for both audiences")
		}
		// removing the empty slot resolves it
		slot := str(t, r, `SELECT rs.id FROM record_slots rs LEFT JOIN song_picks sp ON sp.song_id = rs.song_id WHERE rs.record_id = ? AND rs.removed_at IS NULL AND rs.pinned_take_id IS NULL AND sp.take_id IS NULL`, one)
		exec(t, r, `UPDATE record_slots SET removed_at = '2026-10-07T10:00:00Z', removed_by = 'human:fixture' WHERE id = ?`, slot)
		exec(t, r, `UPDATE records SET order_json = (SELECT json_group_array(s.id) FROM record_slots s WHERE s.record_id = ? AND s.removed_at IS NULL) WHERE id = ?`, one, one)
		if len(onlyCode(t, r, agentRead, "record_empty_slot")) != 0 {
			t.Error("a removed slot still counts")
		}
	})
	t.Run("song_proposals_waiting", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		s3 := songByTitle(t, r, "S3")
		if len(onlyCode(t, r, humanP, "song_proposals_waiting")) != 0 {
			t.Fatal("no proposal yet")
		}
		take := str(t, r, `SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = ? ORDER BY t.id LIMIT 1`, s3)
		prop := func(id, principal string) {
			exec(t, r, `INSERT INTO pick_proposals(id,song_id,take_id,rationale,principal_id,principal_name,state,created_at) VALUES(?,?,?,'because',?,'Synthetic Agent','open','2026-10-07T10:00:00Z')`, id, s3, take, principal)
		}
		prop("01900000-0000-7000-8000-00000000f001", "agent:one")
		h := onlyCode(t, r, humanP, "song_proposals_waiting")
		if len(h) != 1 || h[0].Text != `Song "Synthetic Song S3": 1 agent proposal(s) wait — accept or dismiss` || h[0].Priority != 55 || h[0].Evidence["proposals"] != 1 {
			t.Fatalf("human: %+v", h)
		}
		if h[0].Hint == nil || h[0].Hint.Op != "get_song" || h[0].Hint.Args["id"] != s3 {
			t.Errorf("hint %+v", h[0].Hint)
		}
		if a := onlyCode(t, r, agentRead, "song_proposals_waiting"); len(a) != 1 || a[0].Text != `Song "Synthetic Song S3": your proposal is waiting for the human` {
			t.Fatalf("agent: %+v", a)
		}
		prop("01900000-0000-7000-8000-00000000f002", "agent:two")
		if h := onlyCode(t, r, humanP, "song_proposals_waiting"); len(h) != 1 || h[0].Evidence["proposals"] != 2 || !strings.Contains(h[0].Text, "2 agent proposal(s)") {
			t.Errorf("two proposals: %+v", h)
		}
		exec(t, r, `UPDATE pick_proposals SET state = 'dismissed', decided_at = '2026-10-07T11:00:00Z', decided_by = 'human:fixture'`)
		if len(onlyCode(t, r, humanP, "song_proposals_waiting")) != 0 {
			t.Error("decided proposals do not wait")
		}
	})
}

// TestNextActions_RulesB_Quiet: on a world with no Record, no Pick proposal and no Song with two takes, the six rules of NA2 that
// need those stay silent, whatever the audience.
func TestNextActions_RulesB_Quiet(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true})
	exec(t, r, `UPDATE records SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture'`)
	for _, code := range []string{"record_undecided_slots", "record_empty_slot", "song_proposals_waiting"} {
		if got := onlyCode(t, r, humanP, code); len(got) != 0 {
			t.Errorf("%s: %+v", code, got)
		}
	}
}
