package nextactions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func subjectID(s nextactions.Subject) string {
	if s.ID == nil {
		return ""
	}
	return *s.ID
}

// labelled renders (code, subject) pairs with the fixture labels of the subjects, in the engine's order.
func labelled(t *testing.T, r *servertest.Rig, as []nextactions.Action, only func(code string) bool) []string {
	t.Helper()
	label := map[string]string{
		museByTitle(t, r, lt.L3Muse3Title): "M3", museByTitle(t, r, lt.L3Muse4Title): "M4", museByTitle(t, r, lt.L3Muse5Title): "M5",
		recordByTitle(t, r, "Synthetic Record One"): "Record One", recordByTitle(t, r, "Synthetic Record Two"): "Record Two",
	}
	for _, s := range []string{"S3", "S4", "T1", "u1"} {
		label[songByTitle(t, r, s)] = s
	}
	var out []string
	for _, a := range as {
		if !only(a.Code) {
			continue
		}
		who := "-"
		if a.Subject.ID != nil {
			if who = label[*a.Subject.ID]; who == "" {
				who = a.Subject.Kind + "?"
			}
		}
		out = append(out, a.Code+" "+who)
	}
	return out
}

// TestAcceptance_L3_MuseAwareNextActions: the (code, subject) list of the L3 fixture for an agent that may generate, for the
// rules of NA1 and NA2 (those of NA3 are asserted at INT-B), with the worked case of Muse M3: its text names the Muse and "3
// Song(s), none rendered", its hint is the free estimate of the first draft (get_muse for a read-only agent), a human sees the
// human-audience items besides, and rendering one of M3's Songs removes the item.
func TestAcceptance_L3_MuseAwareNextActions(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	gen := nextactions.Viewer(auth.KindAgent, auth.ScopeRead, auth.ScopeGenerate)
	readOnly := nextactions.Viewer(auth.KindAgent, auth.ScopeRead)
	human := nextactions.Viewer(auth.KindHuman, auth.ScopeRead, auth.ScopeWrite)
	coreCodes := map[string]bool{}
	for _, set := range [][]nextactions.Rule{nextactions.RuleSets()[0], nextactions.RuleSets()[1]} {
		for _, rule := range set {
			coreCodes[rule.Code] = true
		}
	}
	inCore := func(code string) bool { return coreCodes[code] }

	pg := list(t, r, gen, nextactions.Filter{Limit: 50})
	got := labelled(t, r, pg.Items, inCore)
	for i, g := range got { // the Muse of L2's fixture is not labelled by C146.1
		got[i] = strings.Replace(g, "muse?", "L2 Muse", 1)
	}
	want := []string{
		"record_undecided_slots Record One", "record_undecided_slots Record Two",
		"muse_record_candidate M5",
		"muse_unrendered M3",
		"song_duel S3", "song_duel S4",
		"muse_unjudged L2 Muse", "muse_unjudged M4",
		"muse_partial_angles M4",
		"record_empty_slot Record One",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("(code, subject) list for an agent with generate scope:\n got %q\nwant %q", got, want)
	}
	if len(pg.Groups) != 0 || pg.Total != len(want) {
		t.Errorf("groups %v total %d", pg.Groups, pg.Total)
	}

	// the whole page of the fixture is pinned (ids of the fixture are fixed, texts synthetic)
	type goldItem struct {
		Code, Audience, Subject, Text string
		Priority                      int
		Hint                          *nextactions.Hint
		Evidence                      map[string]any
	}
	var gold []goldItem
	for _, a := range pg.Items {
		gold = append(gold, goldItem{a.Code, string(a.Audience), a.Subject.Kind + " " + subjectID(a.Subject), a.Text, a.Priority, a.Hint, a.Evidence})
	}
	raw, _ := json.MarshalIndent(gold, "", "  ")
	raw = append(raw, '\n')
	const goldenName = "testdata/next_actions_fixture.golden.json"
	if *update {
		if err := os.WriteFile(goldenName, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if want, err := os.ReadFile(goldenName); err != nil || string(want) != string(raw) {
		t.Errorf("%s differs (run with -update): %v\n%s", goldenName, err, raw)
	}

	m3 := museByTitle(t, r, lt.L3Muse3Title)
	d1 := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song d1'`)
	m3item := func(p auth.Principal) nextactions.Action {
		as := byCode(list(t, r, p, nextactions.Filter{Limit: 50}), "muse_unrendered")
		if len(as) != 1 || *as[0].Subject.ID != m3 {
			t.Fatalf("muse_unrendered: %v", subjects(as))
		}
		return as[0]
	}
	a := m3item(gen)
	if a.Text != fmt.Sprintf("Muse %s has 3 Song(s), none rendered — estimate the first (the estimate is free)", m3[28:]) {
		t.Errorf("M3 text %q", a.Text)
	}
	if a.Hint == nil || a.Hint.Op != "estimate_generation" || fmt.Sprint(a.Hint.Args) != fmt.Sprint(map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{d1}}}}) {
		t.Errorf("M3 hint %+v", a.Hint)
	}
	if h := m3item(readOnly).Hint; h == nil || h.Op != "get_muse" || h.Args["id"] != m3 {
		t.Errorf("read-only hint %+v", h)
	}

	// a human sees the human-audience items too, and none of the agent-only ones
	rec := str(t, r, `SELECT id FROM records WHERE status = 'draft' AND hidden_at IS NULL ORDER BY id LIMIT 1`)
	exec(t, r, `UPDATE records SET status='proposed', proposal_note='n', proposed_by='agent:tok', proposed_at='2026-10-06T10:00:00Z' WHERE id = ?`, rec)
	hg := labelled(t, r, list(t, r, human, nextactions.Filter{Limit: 50}).Items, inCore)
	proposed := "Record Two"
	if rec == recordByTitle(t, r, "Synthetic Record One") {
		proposed = "Record One"
	}
	if !slices.Contains(hg, "record_proposed_waiting "+proposed) ||
		slices.Contains(hg, "muse_unrendered M3") || slices.Contains(hg, "muse_partial_angles M4") || !slices.Contains(hg, "muse_record_candidate M5") {
		t.Errorf("human list %q", hg)
	}

	// rendering one of M3's Songs: the item disappears, and the Muse is now partly rendered
	renderDraft(t, r, "d1", str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song T1'`), tk1)
	after := list(t, r, gen, nextactions.Filter{Limit: 50})
	if slices.Contains(subjects(byCode(after, "muse_unrendered")), m3) {
		t.Error("M3 is still unrendered after a Song of it was rendered")
	}
	if !slices.Contains(subjects(byCode(after, "muse_partial_angles")), m3) {
		t.Error("M3 has one rendered and two draft Songs: muse_partial_angles should fire")
	}
}

// TestNextActions_SongScope: a call about one Song (a filter, or a take's /context) restricts the Song-level statements to that
// Song and gets the same item the library-wide call gives it.
func TestNextActions_SongScope(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	gen := nextactions.Viewer(auth.KindAgent, auth.ScopeRead, auth.ScopeGenerate)
	for _, label := range []string{"S3", "S4"} {
		id := songByTitle(t, r, label)
		all := byCode(list(t, r, gen, nextactions.Filter{Limit: 50}), "song_duel")
		one := byCode(list(t, r, gen, nextactions.Filter{SubjectKind: "song", SubjectID: id}), "song_duel")
		var want []nextactions.Action
		for _, a := range all {
			if *a.Subject.ID == id {
				want = append(want, a)
			}
		}
		if len(one) != 1 || len(want) != 1 || one[0].Text != want[0].Text || fmt.Sprint(one[0].Evidence) != fmt.Sprint(want[0].Evidence) {
			t.Errorf("%s: filtered %+v, library-wide %+v", label, one, want)
		}
		take := str(t, r, `SELECT root_track_id FROM songs WHERE id = ?`, id)
		fa, err := r.Services.NextActions.ForTake(context.Background(), gen, take, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(byCode(nextactions.Page{Items: fa}, "song_duel")) != 1 {
			t.Errorf("%s: ForTake gave %+v", label, fa)
		}
	}
	// a statement that is about one Song asks for that one Song only
	var scoped []string
	r.Services.NextActions.SetStatementHook(func(q string) {
		if strings.Contains(q, "WITH sel AS") {
			scoped = append(scoped, q)
		}
	})
	_ = list(t, r, gen, nextactions.Filter{SubjectKind: "song", SubjectID: songByTitle(t, r, "S3")})
	if len(scoped) != 1 {
		t.Errorf("song_duel statements %d, want 1", len(scoped))
	}
}

// TestContext_NextActions_RealReader: the worked cases of C139.4 on the L3 fixture through the real reader and the REST bundle
// of an agent with generate scope: the Muse-aware strings go after the base rules, at most min(2, 5 - base - 1) of them, then the
// generation hint when it still fits, and the critique is last.
func TestContext_NextActions_RealReader(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	c, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	next := func(song string) []string {
		take := str(t, r, `SELECT root_track_id FROM songs WHERE title = ?`, "Synthetic Song "+song)
		resp := c.Do(t, "GET", "/api/tracks/"+take+"/context", nil)
		if resp.Status != 200 {
			t.Fatalf("context of %s: %d %s", song, resp.Status, resp.Body)
		}
		var out []string
		for _, s := range resp.JSON(t).(map[string]any)["next_actions"].([]any) {
			out = append(out, s.(string))
		}
		return out
	}
	const (
		rate     = "ask the human to rate this take"
		untimed  = "section times need a score"
		hint     = "try a new-seed variant"
		critique = "record your critique as a comment"
	)
	cases := []struct {
		song string
		want []string // a substring per entry, in order
	}{
		// base {untimed}: the Muse has six strong Songs in no Record; one Muse-aware string, the hint still fits (2 + 2 <= 5)
		{"T1", []string{untimed, "6 Songs rated 4★+ that are in no Record", hint, critique}},
		// base {untimed} and two Record items of the same priority band: undecided slots (80) and the duel (68) win, hint fits (3 + 2 = 5)
		{"S4", []string{untimed, "2 slot(s) play an undecided Pick", "2 takes and 0 blind duel(s)", hint, critique}},
		// base {rate, untimed}: room for two, the hint no longer fits (4 + 2 > 5)
		{"S3", []string{rate, untimed, "2 slot(s) play an undecided Pick", "2 takes and 0 blind duel(s)", critique}},
		// the Record with an empty slot: the two highest of undecided (80), empty slot (60), nothing else eligible
		{"S5", []string{rate, untimed, "2 slot(s) play an undecided Pick", "slot 4 has no take yet", critique}},
		// an unjudged Muse with a partial render: unjudged (65) before partial angles (60)
		{"u1", []string{rate, untimed, "nobody has judged", "still draft — estimate 1 more", critique}},
	}
	for _, tc := range cases {
		got := next(tc.song)
		if len(got) != len(tc.want) || len(got) > 5 {
			t.Errorf("%s: %q, want %d entries", tc.song, got, len(tc.want))
			continue
		}
		for i, sub := range tc.want {
			if !strings.Contains(got[i], sub) {
				t.Errorf("%s entry %d = %q, want it to contain %q", tc.song, i, got[i], sub)
			}
		}
	}
}
