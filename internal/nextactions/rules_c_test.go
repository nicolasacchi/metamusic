package nextactions_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

const placeholderDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// putFeatures gives the Song a song_features row whose subject is its root take; bpm and energy of 0 are NULL. When measured, the
// root take also gets a take_features row with a measured tempo.
func putFeatures(t *testing.T, r *servertest.Rig, label string, bpm, energy int, measured bool) {
	t.Helper()
	song := songByTitle(t, r, label)
	take := str(t, r, `SELECT root_track_id FROM songs WHERE id = ?`, song)
	nul := func(v int) any {
		if v == 0 {
			return nil
		}
		return v
	}
	basis := func(v int, b string) any {
		if v == 0 {
			return nil
		}
		return b
	}
	exec(t, r, `INSERT INTO song_features(song_id,subject_kind,subject_id,bpm_x100,bpm_basis,energy_x10000,energy_basis,digest,feature_def,computed_at)
		VALUES(?,'take',?,?,?,?,?,?,'mm-features/1','2026-10-07T10:00:00Z')`, song, take, nul(bpm), basis(bpm, "declared"), nul(energy), basis(energy, "predicted"), placeholderDigest)
	if measured {
		exec(t, r, `INSERT INTO take_features(track_id,bpm_x100,bpm_basis,bpm_measured_x100,digest,feature_def,computed_at)
			VALUES(?,?,'measured',?,?,'mm-features/1','2026-10-07T10:00:00Z')`, take, bpm, bpm, placeholderDigest)
	}
}

// setTempo gives the Song a collapsed tempo_band row with the detail the sounds package writes.
func setTempo(t *testing.T, r *servertest.Rig, label string, req, got float64, mismatch, octave bool) {
	t.Helper()
	exec(t, r, `DELETE FROM song_sounds WHERE song_id = ? AND facet = 'tempo_band'`, songByTitle(t, r, label))
	exec(t, r, `INSERT INTO song_sounds(song_id,facet,value,origin,detail_json) VALUES(?, 'tempo_band', 'mid', 'measured', ?)`, songByTitle(t, r, label),
		fmt.Sprintf(`{"requested_bpm":%v,"plan_bpm":null,"measured_bpm":%v,"basis":"measured","mismatch":%t,"octave":%t}`, req, got, mismatch, octave))
}

// rulesTableC is the NA3 half of TestNextActions_Rules_Table: for each of the five rules, fires, does not fire, text and hint.
func rulesTableC(t *testing.T) {
	t.Run("song_tempo_mismatch", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		s1, s2, s3 := songByTitle(t, r, "S1"), songByTitle(t, r, "S2"), songByTitle(t, r, "S3")
		if got := onlyCode(t, r, agentRead, "song_tempo_mismatch"); len(got) != 0 {
			t.Fatalf("the fixture has no mismatch: %v", subjects(got))
		}
		setTempo(t, r, "S1", 120, 96.4, true, false)
		setTempo(t, r, "S2", 60, 120, true, true) // an octave error is not a mismatch
		setTempo(t, r, "S3", 100, 101, false, false)
		as := onlyCode(t, r, agentRead, "song_tempo_mismatch")
		if !slices.Equal(subjects(as), []string{s1}) || hasSubject(as, s2) || hasSubject(as, s3) {
			t.Fatalf("song_tempo_mismatch on %v, want S1 only", subjects(as))
		}
		a := as[0]
		want := `Song "Synthetic Song S1": requested 120 BPM, measured about 96 — the tempo cue was probably ignored; write a stronger tempo cue into the style and estimate it (estimate_variant, mode style_edit, with your own patch.style)`
		if a.Text != want || a.Priority != 50 || a.Audience != nextactions.AudienceAgent || a.Evidence["requested_bpm"] != 120 || a.Evidence["measured_bpm"] != 96 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "get_song" || a.Hint.Args["id"] != s1 || len(a.Hint.Args) != 1 {
			t.Errorf("hint %+v", a.Hint)
		}
		if len(onlyCode(t, r, humanP, "song_tempo_mismatch")) != 0 {
			t.Error("song_tempo_mismatch is for agents")
		}
		// the priority is the context floor, so ForTake sees it
		fa, err := r.Services.NextActions.ForTake(context.Background(), agentRead, str(t, r, `SELECT root_track_id FROM songs WHERE id = ?`, s1), 5)
		if err != nil || len(byCode(nextactions.Page{Items: fa}, "song_tempo_mismatch")) != 1 {
			t.Errorf("ForTake: %+v %v", fa, err)
		}
		exec(t, r, `UPDATE songs SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, s1)
		if len(onlyCode(t, r, agentRead, "song_tempo_mismatch")) != 0 {
			t.Error("a hidden Song is silent")
		}
	})
	t.Run("record_unsequenced", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		two := recordByTitle(t, r, "Synthetic Record Two") // a draft with two live slots
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Fatal("two slots are too few")
		}
		addSlots := func(labels ...string) {
			for i, l := range labels {
				exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
					fmt.Sprintf("01900000-0000-7000-8000-00000000f%03x", i+1), two, songByTitle(t, r, l))
			}
		}
		addSlots("T1", "T2")
		// four live slots, but no Song has a tempo or an energy feature yet
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Fatal("no feature: the planner would have nothing to order by")
		}
		putFeatures(t, r, "T1", 9600, 0, false)
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Fatal("one featured Song is not enough")
		}
		putFeatures(t, r, "T2", 0, 4500, false)
		as := onlyCode(t, r, agentRead, "record_unsequenced")
		if !hasSubject(as, two) {
			t.Fatalf("record_unsequenced on %v", subjects(as))
		}
		var a nextactions.Action
		for _, x := range as {
			if *x.Subject.ID == two {
				a = x
			}
		}
		if a.Text != `Record "Synthetic Record Two" has not been sequenced — preview the order (suggest_sequence, arc)` || a.Priority != 45 || a.Audience != nextactions.AudienceBoth || a.Evidence["slots"] != 4 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "suggest_sequence" || a.Hint.Args["record_id"] != two || len(a.Hint.Args) != 1 {
			t.Errorf("hint %+v", a.Hint)
		}
		if !hasSubject(onlyCode(t, r, humanP, "record_unsequenced"), two) {
			t.Error("for both audiences")
		}
		// a published Record is not a draft
		one := recordByTitle(t, r, "Synthetic Record One")
		if hasSubject(as, one) {
			t.Error("a published Record is never listed")
		}
		// any sequence row, ever, silences it
		exec(t, r, `INSERT INTO record_sequences(id,record_id,record_revision,strategy,order_before_json,order_after_json,exact,cost_input,cost_output,coverage_json,digest,
			explanation_json,actor,actor_kind,actor_name,at) VALUES('01900000-0000-7000-8000-00000000f9f1',?,1,'arc','[]','[]',1,0,0,'{}',?,'{}','human:fixture','human','fixture','2026-10-07T10:00:00Z')`, two, placeholderDigest)
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Error("a sequenced Record is still listed")
		}
	})
	t.Run("record_unsequenced hidden and removed", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		two := recordByTitle(t, r, "Synthetic Record Two")
		for i, l := range []string{"T1", "T2"} {
			exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
				fmt.Sprintf("01900000-0000-7000-8000-00000000f%03x", i+1), two, songByTitle(t, r, l))
			putFeatures(t, r, l, 9600+i*100, 0, false)
		}
		if !hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Fatal("baseline")
		}
		exec(t, r, `UPDATE record_slots SET removed_at = '2026-10-07T11:00:00Z', removed_by = 'human:fixture' WHERE id = '01900000-0000-7000-8000-00000000f001'`)
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Error("a removed slot still counts toward the four")
		}
	})
	t.Run("record_unsequenced hidden", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		two := recordByTitle(t, r, "Synthetic Record Two")
		for i, l := range []string{"T1", "T2"} {
			exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
				fmt.Sprintf("01900000-0000-7000-8000-00000000f%03x", i+1), two, songByTitle(t, r, l))
			putFeatures(t, r, l, 9600+i*100, 0, false)
		}
		exec(t, r, `UPDATE records SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, two)
		if hasSubject(onlyCode(t, r, agentRead, "record_unsequenced"), two) {
			t.Error("a hidden Record is silent")
		}
	})
	t.Run("taxonomy_unmapped", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		r.Settle(t) // the Reconciler derives song_unmapped_terms from the styles
		skank := func() []nextactions.Action {
			var out []nextactions.Action
			for _, a := range onlyCode(t, r, agentRead, "taxonomy_unmapped") {
				if *a.Subject.ID == "skank" {
					out = append(out, a)
				}
			}
			return out
		}
		as := skank()
		if len(as) != 1 || as[0].Subject.Kind != "taxonomy" {
			t.Fatalf("taxonomy_unmapped: %+v", onlyCode(t, r, agentRead, "taxonomy_unmapped"))
		}
		a := as[0]
		if a.Text != `The term "skank" is unmapped in 5 Songs — propose a mapping (propose_taxonomy)` || a.Priority != 35 || a.Audience != nextactions.AudienceAgent ||
			a.Evidence["term"] != "skank" || a.Evidence["songs"] != 5 {
			t.Errorf("%q %v", a.Text, a.Evidence)
		}
		if a.Hint == nil || a.Hint.Op != "get_sound_taxonomy" || a.Hint.Args["unmapped"] != true || len(a.Hint.Args) != 1 {
			t.Errorf("hint %+v", a.Hint)
		}
		if len(onlyCode(t, r, humanP, "taxonomy_unmapped")) != 0 {
			t.Error("taxonomy_unmapped is for agents")
		}
		// hiding one of the five Songs drops the term below the threshold
		exec(t, r, `UPDATE songs SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, songByTitle(t, r, "d4"))
		if len(skank()) != 0 {
			t.Error("four live Songs are below the threshold")
		}
		exec(t, r, `UPDATE songs SET hidden_at = NULL, hidden_by = NULL WHERE id = ?`, songByTitle(t, r, "d4"))
		// an open proposal that maps the term silences it, a rejected one does not
		ins := func(id, state, term, digest string) {
			exec(t, r, `INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
				VALUES(?, 'open', 1, 'r', ?, ?, '{}', 'agent:tok', 'agent', 'Synthetic Agent', '2026-10-06T10:00:00Z')`,
				id, fmt.Sprintf(`[{"term":%q,"facet":"mood","value":"chill","op":"add"}]`, term), digest)
			if state != "open" {
				exec(t, r, `UPDATE taxonomy_proposals SET state=?, decided_at='2026-10-06T11:00:00Z', decided_by='human:fixture' WHERE id = ?`, state, id)
			}
		}
		ins("p1", "rejected", "skank", strings.Repeat("a", 64))
		if len(skank()) != 1 {
			t.Error("a rejected proposal must not silence the term")
		}
		ins("p2", "open", "Skank", strings.Repeat("b", 64))
		if got := skank(); len(got) != 0 {
			t.Errorf("an open proposal names the term: %+v", got)
		}
	})
	t.Run("song_no_muse", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		s6 := songByTitle(t, r, "S6")
		as := onlyCode(t, r, agentRead, "song_no_muse")
		if !slices.Equal(subjects(as), []string{s6}) {
			t.Fatalf("song_no_muse on %v, want S6 only (S7 and d4 have no take)", subjects(as))
		}
		a := as[0]
		if a.Text != `Song "Synthetic Song S6" has no Muse — if you know its inspiration: create_muse, then assign_muse_songs` || a.Priority != 25 || a.Audience != nextactions.AudienceAgent {
			t.Errorf("%q", a.Text)
		}
		if a.Hint == nil || a.Hint.Op != "get_song" || a.Hint.Args["id"] != s6 {
			t.Errorf("hint %+v", a.Hint)
		}
		if len(onlyCode(t, r, humanP, "song_no_muse")) != 0 {
			t.Error("song_no_muse is for agents")
		}
		// a render of the draft Song d4 (no Muse, no take yet) makes it a candidate: recipe-known with a take
		renderDraft(t, r, "d4", str(t, r, `SELECT root_track_id FROM songs WHERE id = ?`, s6), tk1)
		if !hasSubject(onlyCode(t, r, agentRead, "song_no_muse"), songByTitle(t, r, "d4")) {
			t.Error("a rendered Song with no Muse is listed")
		}
		// the operator clearing the Muse (muse_source manual) silences it
		exec(t, r, `UPDATE songs SET muse_source = 'manual' WHERE id = ?`, s6)
		if hasSubject(onlyCode(t, r, agentRead, "song_no_muse"), s6) {
			t.Error("a manual clear is not a missing Muse")
		}
		// a Muse silences it too
		exec(t, r, `UPDATE songs SET muse_id = (SELECT id FROM muses LIMIT 1), muse_source = 'manual' WHERE id = ?`, songByTitle(t, r, "d4"))
		if hasSubject(onlyCode(t, r, agentRead, "song_no_muse"), songByTitle(t, r, "d4")) {
			t.Error("a Song with a Muse is listed")
		}
	})
	t.Run("features_missing", func(t *testing.T) {
		t.Parallel()
		r := l3Rig(t)
		if got := onlyCode(t, r, humanP, "features_missing"); len(got) != 0 {
			t.Fatalf("no feature row yet: %+v", got)
		}
		putFeatures(t, r, "T1", 9600, 0, false)
		putFeatures(t, r, "T2", 9800, 0, false)
		putFeatures(t, r, "T3", 10000, 0, true) // measured tempo: not missing
		as := onlyCode(t, r, humanP, "features_missing")
		if len(as) != 1 {
			t.Fatalf("features_missing: %+v", as)
		}
		a := as[0]
		if a.Subject.Kind != "library" || a.Subject.ID != nil || a.Evidence["songs"] != 2 || a.Priority != 20 || a.Audience != nextactions.AudienceHuman || a.Hint != nil ||
			a.Text != `2 Song(s) have no audio measurements yet — run "metamusic analyze" on the server` {
			t.Errorf("%+v", a)
		}
		exec(t, r, `UPDATE songs SET hidden_at = '2026-10-07T11:00:00Z', hidden_by = 'human:fixture' WHERE id = ?`, songByTitle(t, r, "T2"))
		if as := onlyCode(t, r, humanP, "features_missing"); len(as) != 1 || as[0].Evidence["songs"] != 1 {
			t.Errorf("a hidden Song still counts: %+v", as)
		}
	})
}

// TestNextActions_SongNoMuse_ExcludesReferences: the rule is `muse_id IS NULL AND muse_source IS NOT 'manual'`, restricted to
// Songs with a take whose recipe is known: the originals and every reference Song (recipe unknown) are never listed, by design
// they have no Muse and there is no dismissal.
func TestNextActions_SongNoMuse_ExcludesReferences(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	s5, s6 := songByTitle(t, r, "S5"), songByTitle(t, r, "S6")
	if got := str(t, r, `SELECT engine FROM recipes WHERE id = (SELECT recipe_id FROM tracks WHERE id = (SELECT root_track_id FROM songs WHERE id = ?))`, s5); got != "unknown" {
		t.Fatalf("S5 is the reference Song of the fixture, engine %q", got)
	}
	// S5 never had a Muse set: still not listed, its recipe is not known
	exec(t, r, `UPDATE songs SET muse_id = NULL, muse_source = NULL WHERE id = ?`, s5)
	as := onlyCode(t, r, agentRead, "song_no_muse")
	if hasSubject(as, s5) || !hasSubject(as, s6) {
		t.Errorf("song_no_muse on %v: the reference Song S5 must be excluded, S6 listed", subjects(as))
	}
	// with the Muse cleared by the operator S6 is not listed either
	exec(t, r, `UPDATE songs SET muse_source = 'manual' WHERE id = ?`, s6)
	if hasSubject(onlyCode(t, r, agentRead, "song_no_muse"), s6) {
		t.Error("an operator-cleared Muse is not missing")
	}
}

// TestNextActions_FeaturesMissing_HumanOnly: the rule is for the human (an agent has no analyze operation): an agent never gets it,
// not even by asking for the library subject.
func TestNextActions_FeaturesMissing_HumanOnly(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	putFeatures(t, r, "T1", 9600, 0, false)
	if len(onlyCode(t, r, humanP, "features_missing")) != 1 {
		t.Fatal("the human does not get features_missing")
	}
	for _, p := range []nextactions.Filter{{}, {SubjectKind: "library"}} {
		if got := byCode(list(t, r, agentWrite, p), "features_missing"); len(got) != 0 {
			t.Errorf("an agent got features_missing with filter %+v: %+v", p, got)
		}
	}
	for _, rule := range nextactions.Rules {
		if rule.Code == "features_missing" && rule.Audience != nextactions.AudienceHuman {
			t.Errorf("audience %s", rule.Audience)
		}
	}
	if nextactions.ForTakeCodes()["features_missing"] {
		t.Error("features_missing is library-level: not a /context rule")
	}
}

// TestNextActions_RecordUnsequenced_NeverRunsPlanner: the rule is one batch statement over rows: the number of statements a call
// issues does not change when a Record that qualifies exists, and none of them names the planner's inputs beyond the feature
// columns (no per-Song query).
func TestNextActions_RecordUnsequenced_NeverRunsPlanner(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	count := func() (int, []string) {
		var seen []string
		r.Services.NextActions.SetStatementHook(func(q string) { seen = append(seen, q) })
		_ = list(t, r, agentWrite, nextactions.Filter{Limit: 50})
		return len(seen), seen
	}
	before, _ := count()
	two := recordByTitle(t, r, "Synthetic Record Two")
	for i, l := range []string{"T1", "T2"} {
		exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
			fmt.Sprintf("01900000-0000-7000-8000-00000000f%03x", i+1), two, songByTitle(t, r, l))
		putFeatures(t, r, l, 9600+i*100, 0, false)
	}
	after, seen := count()
	if before != after {
		t.Errorf("statements %d before the Record qualified, %d after", before, after)
	}
	n := 0
	for _, q := range seen {
		if strings.Contains(q, "record_sequences") {
			n++
			if strings.Contains(q, "song_features") != true {
				t.Errorf("the record_unsequenced statement should read song_features rows only: %.80s", q)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d statements name record_sequences, want 1", n)
	}
	if got := onlyCode(t, r, agentWrite, "record_unsequenced"); len(got) != 1 {
		t.Fatalf("record_unsequenced: %+v", got)
	}
}

// TestNextActions_RulesC_HintsValidate: every hint of the NA3 rules names a free registry operation the viewer may call, with
// arguments that satisfy its generated input schema.
func TestNextActions_RulesC_HintsValidate(t *testing.T) {
	t.Parallel()
	doc, ops := loadSpec(t)
	r := l3Rig(t)
	r.Settle(t)
	setTempo(t, r, "S1", 120, 96.4, true, false)
	two := recordByTitle(t, r, "Synthetic Record Two")
	for i, l := range []string{"T1", "T2"} {
		exec(t, r, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES(?,?,?,'human:fixture','2026-10-07T10:00:00Z')`,
			fmt.Sprintf("01900000-0000-7000-8000-00000000f%03x", i+1), two, songByTitle(t, r, l))
	}
	putFeatures(t, r, "T1", 9600, 0, false)
	putFeatures(t, r, "T2", 9800, 0, false)
	seen := map[string]bool{}
	for _, a := range list(t, r, agentRead, nextactions.Filter{Limit: 50}).Items {
		for _, rule := range nextactions.RuleSets()[2] {
			if rule.Code == a.Code && a.Hint != nil {
				seen[a.Code] = true
				validateHint(t, doc, ops, agentRead, *a.Hint)
			}
		}
	}
	for _, code := range []string{"song_tempo_mismatch", "record_unsequenced", "taxonomy_unmapped", "song_no_muse"} {
		if !seen[code] {
			t.Errorf("%s emitted no hint to validate (seen %v)", code, seen)
		}
	}
}
