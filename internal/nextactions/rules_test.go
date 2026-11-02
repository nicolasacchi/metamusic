package nextactions_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func l3Rig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true})
}

func str(t *testing.T, r *servertest.Rig, q string, args ...any) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

func exec(t *testing.T, r *servertest.Rig, q string, args ...any) {
	t.Helper()
	if _, err := r.Store.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func museByTitle(t *testing.T, r *servertest.Rig, title string) string {
	return str(t, r, `SELECT id FROM muses WHERE title = ?`, title)
}

func list(t *testing.T, r *servertest.Rig, p auth.Principal, f nextactions.Filter) nextactions.Page {
	t.Helper()
	pg, err := r.Services.NextActions.List(context.Background(), p, f)
	if err != nil {
		t.Fatal(err)
	}
	return pg
}

func byCode(p nextactions.Page, code string) []nextactions.Action {
	var out []nextactions.Action
	for _, a := range p.Items {
		if a.Code == code {
			out = append(out, a)
		}
	}
	return out
}

func subjects(as []nextactions.Action) []string {
	var out []string
	for _, a := range as {
		if a.Subject.ID != nil {
			out = append(out, *a.Subject.ID)
		}
	}
	slices.Sort(out)
	return out
}

// sample is one candidate per rule with the fixed names the golden texts use.
type sample struct {
	subject nextactions.Subject
	ev      map[string]any
}

type fixedNames struct{}

func (fixedNames) Muse(id string) string { return "Muse " + id[len(id)-8:] }
func (fixedNames) Song(string) string    { return "Synthetic Song" }
func (fixedNames) Record(string) string  { return "Synthetic Record" }

func ptr(s string) *string { return &s }

var samples = map[string]sample{
	"record_proposed_waiting": {nextactions.Subject{Kind: "record", ID: ptr("01900000-0000-7000-8000-0000000000a1")}, map[string]any{"_agent": "Synthetic Agent"}},
	"muse_unrendered":         {nextactions.Subject{Kind: "muse", ID: ptr("019a1b2c-0000-7000-8000-0000000000a2")}, map[string]any{"songs": 3, "draft_id": "d"}},
	"taxonomy_proposals_open": {nextactions.Subject{Kind: "taxonomy"}, map[string]any{"open": 2}},
	"muse_unjudged":           {nextactions.Subject{Kind: "muse", ID: ptr("019a1b2c-0000-7000-8000-0000000000a2")}, map[string]any{"rendered": 4}},

	"record_undecided_slots": {nextactions.Subject{Kind: "record", ID: ptr("01900000-0000-7000-8000-0000000000a1")}, map[string]any{"slots": 2}},
	"muse_record_candidate":  {nextactions.Subject{Kind: "muse", ID: ptr("019a1b2c-0000-7000-8000-0000000000a2")}, map[string]any{"songs": 6}},
	"song_duel":              {nextactions.Subject{Kind: "song", ID: ptr("01900000-0000-7000-8000-0000000000a3")}, map[string]any{"takes": 3, "duels": 1}},
	"muse_partial_angles": {nextactions.Subject{Kind: "muse", ID: ptr("019a1b2c-0000-7000-8000-0000000000a2")}, map[string]any{
		"rendered": 1, "drafts": 4, "_rendered_angles": "remake-long", "_rendered_n": 1, "_draft_angles": "alt-take-long, long, short", "_draft_n": 4}},
	"record_empty_slot":      {nextactions.Subject{Kind: "record", ID: ptr("01900000-0000-7000-8000-0000000000a1")}, map[string]any{"slot": 3, "empty": 1}},
	"song_proposals_waiting": {nextactions.Subject{Kind: "song", ID: ptr("01900000-0000-7000-8000-0000000000a3")}, map[string]any{"proposals": 2}},
}

type goldenRow struct {
	Code     string `json:"code"`
	Priority int    `json:"priority"`
	Audience string `json:"audience"`
	MinScope string `json:"min_scope"`
	Hint     bool   `json:"hint"`
	Text     string `json:"text,omitempty"`
}

func project(rs []nextactions.Rule, text bool) []goldenRow {
	out := make([]goldenRow, len(rs))
	for i, r := range rs {
		out[i] = goldenRow{Code: r.Code, Priority: r.Priority, Audience: string(r.Audience), MinScope: string(r.MinScope), Hint: r.Hint != nil}
		if s, ok := samples[r.Code]; ok && text {
			out[i].Text = r.Text(nextactions.Candidate{Subject: s.subject, Evidence: s.ev}, fixedNames{})
		}
	}
	return out
}

func readGolden(t *testing.T, name string) []goldenRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var rows []goldenRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestNextActions_RulesGolden: the registry is the concatenation of the lanes' rule slices, and it equals the concatenated
// testdata/rules_v1_{a,b,c}.golden.json (code, priority, audience, scope and whether it hints). NA1 writes file a; NA2 and NA3 add
// theirs when their slices are no longer empty. The golden rows of this lane also hold the text of one sample candidate, which
// TestNextActions_Rules_Table checks.
func TestNextActions_RulesGolden(t *testing.T) {
	sets := nextactions.RuleSets()
	if want := slices.Concat(sets...); len(want) != len(nextactions.Rules) {
		t.Fatalf("registry has %d rules, the lane slices %d", len(nextactions.Rules), len(want))
	}
	var got []goldenRow
	for i, set := range sets {
		name := fmt.Sprintf("rules_v1_%c.golden.json", 'a'+i)
		if len(set) == 0 {
			if _, err := os.Stat(filepath.Join("testdata", name)); err == nil {
				t.Errorf("%s exists but the rule slice is empty", name)
			}
			continue
		}
		if *update {
			raw, _ := json.MarshalIndent(project(set, true), "", "  ")
			if err := os.WriteFile(filepath.Join("testdata", name), append(raw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		rows := readGolden(t, name)
		if len(rows) != len(set) {
			t.Errorf("%s has %d rows for %d rules", name, len(rows), len(set))
		}
		got = append(got, rows...)
	}
	strip := func(rs []goldenRow) []goldenRow {
		out := slices.Clone(rs)
		for i := range out {
			out[i].Text = ""
		}
		return out
	}
	if diff := cmp.Diff(strip(got), project(nextactions.Rules, false)); diff != "" {
		t.Errorf("registry differs from the golden files (-golden +registry):\n%s", diff)
	}
	seen := map[string]bool{}
	for _, r := range nextactions.Rules {
		if seen[r.Code] {
			t.Errorf("duplicate rule code %s", r.Code)
		}
		seen[r.Code] = true
		if r.Priority < 0 || r.Priority > 100 || r.Find == nil || r.Text == nil {
			t.Errorf("rule %s is incomplete", r.Code)
		}
	}
}

// TestNextActions_Rules_Table: for each of the four rules of NA1 the golden text of a sample candidate (the Muse named by an id
// tail, never a title), then fires and does not fire against the L3 fixture world.
func TestNextActions_Rules_Table(t *testing.T) {
	golden := map[string]goldenRow{}
	for i, set := range nextactions.RuleSets() {
		if len(set) == 0 {
			continue
		}
		for _, row := range readGolden(t, fmt.Sprintf("rules_v1_%c.golden.json", 'a'+i)) {
			golden[row.Code] = row
		}
	}
	for code, s := range samples {
		var rule *nextactions.Rule
		for i := range nextactions.Rules {
			if nextactions.Rules[i].Code == code {
				rule = &nextactions.Rules[i]
			}
		}
		if rule == nil {
			t.Errorf("no rule %s", code)
			continue
		}
		if got := rule.Text(nextactions.Candidate{Subject: s.subject, Evidence: s.ev}, fixedNames{}); got != golden[code].Text || got == "" {
			t.Errorf("%s text = %q, golden %q", code, got, golden[code].Text)
		}
	}

	r := l3Rig(t)
	ctx := context.Background()
	_ = ctx
	m3, m4, m5 := museByTitle(t, r, lt.L3Muse3Title), museByTitle(t, r, lt.L3Muse4Title), museByTitle(t, r, lt.L3Muse5Title)
	d1 := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song d1'`)
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead}}
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}

	t.Run("muse_unrendered fires for a Muse of drafts only, naming the first draft by angle", func(t *testing.T) {
		as := byCode(list(t, r, agent, nextactions.Filter{}), "muse_unrendered")
		if len(as) != 1 || *as[0].Subject.ID != m3 {
			t.Fatalf("muse_unrendered: %+v", subjects(as))
		}
		a := as[0]
		if a.Evidence["songs"] != 3 || a.Evidence["draft_id"] != d1 || a.Priority != 70 || a.Audience != nextactions.AudienceAgent {
			t.Errorf("evidence %v priority %d audience %s", a.Evidence, a.Priority, a.Audience)
		}
		want := fmt.Sprintf("Muse %s has 3 Song(s), none rendered — estimate the first (the estimate is free)", m3[28:])
		if a.Text != want {
			t.Errorf("text %q, want %q", a.Text, want)
		}
		if hs := byCode(list(t, r, human, nextactions.Filter{}), "muse_unrendered"); len(hs) != 0 {
			t.Error("muse_unrendered is for agents: a human got it")
		}
	})
	t.Run("muse_unjudged fires for rendered Muses nobody judged, for both audiences", func(t *testing.T) {
		for _, p := range []auth.Principal{agent, human} {
			as := byCode(list(t, r, p, nextactions.Filter{}), "muse_unjudged")
			if !slices.Contains(subjects(as), m4) || slices.Contains(subjects(as), m5) || slices.Contains(subjects(as), m3) {
				t.Fatalf("%s: muse_unjudged on %v (want M4, not M5 which has human stars, not M3 which has no take)", p.Kind, subjects(as))
			}
			for _, a := range as {
				if *a.Subject.ID == m4 && (a.Evidence["rendered"] != 1 || a.Text != fmt.Sprintf("Muse %s has 1 rendered Song(s) nobody has judged — ask the human to listen, or add a critique (add_comment)", m4[28:])) {
					t.Errorf("M4: %q %v", a.Text, a.Evidence)
				}
			}
		}
		// rating its one take makes the Song judged and the item goes away on the next call
		take := str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song u1'`)
		exec(t, r, `INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,'human',4,'human','human:fixture','fixture',?,?)`, take, "2026-10-06T10:00:00Z", "2026-10-06T10:00:00Z")
		if slices.Contains(subjects(byCode(list(t, r, agent, nextactions.Filter{}), "muse_unjudged")), m4) {
			t.Error("a rated Muse is still unjudged")
		}
	})
	t.Run("record_proposed_waiting is for humans and names the proposing agent", func(t *testing.T) {
		if got := byCode(list(t, r, human, nextactions.Filter{}), "record_proposed_waiting"); len(got) != 0 {
			t.Fatalf("no Record is proposed yet: %v", subjects(got))
		}
		rec := str(t, r, `SELECT id FROM records WHERE status = 'draft' AND hidden_at IS NULL ORDER BY id LIMIT 1`)
		title := str(t, r, `SELECT title FROM records WHERE id = ?`, rec)
		exec(t, r, `UPDATE records SET status='proposed', proposal_note='n', proposed_by='agent:tok', proposed_at='2026-10-06T10:00:00Z' WHERE id = ?`, rec)
		exec(t, r, `INSERT INTO record_changes(id,record_id,seq,at,kind,actor,actor_kind,actor_name)
			VALUES('01900000-0000-7000-8000-00000000fa01', ?, (SELECT COALESCE(MAX(seq),0)+1 FROM record_changes WHERE record_id = ?), '2026-10-06T10:00:00Z','proposed','agent:tok','agent','Synthetic Agent')`, rec, rec)
		got := byCode(list(t, r, human, nextactions.Filter{}), "record_proposed_waiting")
		want := fmt.Sprintf(`Record "%s" was proposed by Synthetic Agent: review it and publish`, title)
		if len(got) != 1 || *got[0].Subject.ID != rec || got[0].Text != want || got[0].Priority != 90 || got[0].Hint != nil {
			t.Fatalf("%+v want text %q", got, want)
		}
		if got := byCode(list(t, r, agent, nextactions.Filter{}), "record_proposed_waiting"); len(got) != 0 {
			t.Error("an agent got a human-only rule")
		}
		exec(t, r, `UPDATE records SET hidden_at='2026-10-06T11:00:00Z', hidden_by='human:fixture' WHERE id = ?`, rec)
		if got := byCode(list(t, r, human, nextactions.Filter{}), "record_proposed_waiting"); len(got) != 0 {
			t.Error("a hidden Record is still waiting")
		}
	})
	rulesTableB(t)
	t.Run("taxonomy_proposals_open counts open proposals only", func(t *testing.T) {
		if got := byCode(list(t, r, human, nextactions.Filter{}), "taxonomy_proposals_open"); len(got) != 0 {
			t.Fatalf("no proposal yet: %+v", got)
		}
		ins := func(id, state, digest string) {
			decided := ""
			if state != "open" {
				decided = `, decided_at='2026-10-06T11:00:00Z', decided_by='human:fixture'`
			}
			exec(t, r, `INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
				VALUES(?, 'open', 1, 'r', '[{"term":"t","facet":"mood","value":"chill","op":"add"}]', ?, '{}', 'agent:tok', 'agent', 'Synthetic Agent', '2026-10-06T10:00:00Z')`, id, digest)
			if decided != "" {
				exec(t, r, `UPDATE taxonomy_proposals SET state=?`+decided+` WHERE id = ?`, state, id)
			}
		}
		ins("p1", "open", strings.Repeat("a", 64))
		ins("p2", "open", strings.Repeat("b", 64))
		ins("p3", "rejected", strings.Repeat("c", 64))
		got := byCode(list(t, r, human, nextactions.Filter{}), "taxonomy_proposals_open")
		if len(got) != 1 || got[0].Subject.Kind != "taxonomy" || got[0].Subject.ID != nil || got[0].Evidence["open"] != 2 ||
			got[0].Text != "2 taxonomy proposal(s) wait for your decision" || got[0].Priority != 70 {
			t.Fatalf("%+v", got)
		}
		if got := byCode(list(t, r, agent, nextactions.Filter{}), "taxonomy_proposals_open"); len(got) != 0 {
			t.Error("an agent got a human-only rule")
		}
	})
}

// TestNextActions_Rules_DoNotFire: the conditions that keep muse_unrendered quiet: a queued job on one of the Muse's drafts, all
// its drafts archived, and a hidden Muse.
func TestNextActions_Rules_DoNotFire(t *testing.T) {
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead}}
	for name, mutate := range map[string]func(t *testing.T, r *servertest.Rig, m3 string){
		"queued job": func(t *testing.T, r *servertest.Rig, _ string) {
			draft := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song d2'`)
			exec(t, r, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
					expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
				SELECT 'job-na1', batch_id, 99, kind, engine, backend, recipe_id, ?, 'from_draft', '{}', 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd, reserved_micro_usd,
				       'queued', next_attempt_at, queued_at, created_at, updated_at FROM jobs LIMIT 1`, draft)
		},
		"drafts archived": func(t *testing.T, r *servertest.Rig, _ string) {
			exec(t, r, `UPDATE recipe_drafts SET status='archived' WHERE id IN (SELECT draft_id FROM songs WHERE title IN ('Synthetic Song d1','Synthetic Song d2','Synthetic Song d3'))`)
		},
		"hidden muse": func(t *testing.T, r *servertest.Rig, m3 string) {
			exec(t, r, `UPDATE muses SET hidden_at='2026-10-06T11:00:00Z', hidden_by='human:fixture' WHERE id = ?`, m3)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := l3Rig(t)
			m3 := museByTitle(t, r, lt.L3Muse3Title)
			if got := subjects(byCode(list(t, r, agent, nextactions.Filter{}), "muse_unrendered")); !slices.Equal(got, []string{m3}) {
				t.Fatalf("baseline: %v", got)
			}
			mutate(t, r, m3)
			if got := byCode(list(t, r, agent, nextactions.Filter{}), "muse_unrendered"); len(got) != 0 {
				t.Errorf("still fires: %+v", got)
			}
		})
	}
}

// TestNextActions_HiddenMuseSilent: a hidden Muse is never named: no item about it, and its id appears nowhere in the page.
func TestNextActions_HiddenMuseSilent(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
	m3, m4 := museByTitle(t, r, lt.L3Muse3Title), museByTitle(t, r, lt.L3Muse4Title)
	exec(t, r, `UPDATE muses SET hidden_at='2026-10-06T11:00:00Z', hidden_by='human:fixture' WHERE id IN (?, ?)`, m3, m4)
	raw, _ := json.Marshal(list(t, r, agent, nextactions.Filter{Limit: 50}))
	for _, id := range []string{m3, m4, m3[28:]} {
		if strings.Contains(string(raw), id) {
			t.Errorf("a hidden Muse id %s appears in %s", id, raw)
		}
	}
	for _, a := range list(t, r, agent, nextactions.Filter{Limit: 50}).Items {
		if a.Subject.ID != nil && (*a.Subject.ID == m3 || *a.Subject.ID == m4) {
			t.Errorf("an item about a hidden Muse: %+v", a)
		}
	}
}

// TestNextActions_IDVariant: until D-L3-16 is ticked an item text names a Muse as "Muse <last 8 characters of its id>" and
// carries no Muse title or artist, for List and for ForTake, in the text, the hint and the evidence.
func TestNextActions_IDVariant(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
	canaries := []string{lt.L3Muse3Title, lt.L3Muse3Artist, lt.L3Muse4Title, lt.L3Muse4Artist, lt.L3Muse5Title, lt.L3Muse5Artist, "Synthetic Agent"}
	rows, err := r.Store.R.Query(`SELECT title, COALESCE(artist, '') FROM muses`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title, artist string
		if err := rows.Scan(&title, &artist); err != nil {
			t.Fatal(err)
		}
		canaries = append(canaries, title)
		if artist != "" {
			canaries = append(canaries, artist)
		}
	}
	_ = rows.Close()
	pg := list(t, r, agent, nextactions.Filter{Limit: 50})
	u1 := str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song u1'`)
	fa, err := r.Services.NextActions.ForTake(context.Background(), agent, u1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(fa) != 2 || fa[0].Code != "muse_unjudged" || fa[1].Code != "muse_partial_angles" || !strings.HasPrefix(fa[0].Text, "Muse "+museByTitle(t, r, lt.L3Muse4Title)[28:]+" has") {
		t.Errorf("ForTake(u1) = %+v, want the muse_unjudged text of M4", fa)
	}
	var museTexts int
	for _, a := range slices.Concat(pg.Items, fa) {
		raw, _ := json.Marshal(a)
		for _, c := range canaries {
			if strings.Contains(string(raw), c) {
				t.Errorf("Muse text %q leaked into %s", c, raw)
			}
		}
		if a.Subject.Kind == "muse" {
			museTexts++
			if !strings.HasPrefix(a.Text, "Muse "+(*a.Subject.ID)[28:]) {
				t.Errorf("text %q does not start with the id tail of %s", a.Text, *a.Subject.ID)
			}
		}
	}
	if museTexts < 3 {
		t.Errorf("only %d Muse items: the fixture should give at least three", museTexts)
	}
}
