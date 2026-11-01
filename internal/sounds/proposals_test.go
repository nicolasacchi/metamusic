package sounds_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

var deviceP = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"}

func agentN(n int) auth.Principal {
	return auth.Principal{Kind: auth.KindAgent, ID: fmt.Sprintf("tok-%d", n), Name: fmt.Sprintf("agent-%d", n), Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
}

func wantProblem(t *testing.T, name string, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Errorf("%s: %v, want %d %s", name, err, status, code)
		return nil
	}
	return pe
}

func TestPropose_Rules(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	many := make([]sounds.Mapping, 51)
	for i := range many {
		many[i] = me("term"+strings.Repeat("a", i+1), "mood", "chill", "add")
	}
	for _, c := range []struct {
		name      string
		rationale string
		ms        []sounds.Mapping
		status    int
		code      string
	}{
		{"empty rationale", "", []sounds.Mapping{me("zouk", "genre_family", "world", "add")}, 422, "VALIDATION_FAILED"},
		{"blank rationale", " \t ", []sounds.Mapping{me("zouk", "genre_family", "world", "add")}, 422, "VALIDATION_FAILED"},
		{"rationale too long", strings.Repeat("r", 501), []sounds.Mapping{me("zouk", "genre_family", "world", "add")}, 422, "VALIDATION_FAILED"},
		{"no mappings", "why", nil, 422, "TOO_MANY_MAPPINGS"},
		{"51 mappings", "why", many, 422, "TOO_MANY_MAPPINGS"},
		{"term without a letter", "why", []sounds.Mapping{me("1234 - 5", "mood", "chill", "add")}, 422, "INVALID_TERM"},
		{"tempo_band", "why", []sounds.Mapping{me("fast one", "tempo_band", "fast", "add")}, 422, "FACET_NOT_MAPPABLE"},
		{"unknown facet", "why", []sounds.Mapping{me("x y", "colour", "red", "add")}, 422, "INVALID_FACET"},
		{"bad value", "why", []sounds.Mapping{me("long ago", "era", "30s", "add")}, 422, "INVALID_VALUE"},
		{"bad op", "why", []sounds.Mapping{me("odd", "mood", "chill", "toggle")}, 422, "INVALID_OP"},
		{"add and remove together", "why", []sounds.Mapping{me("zouk", "genre_family", "world", "add"), me("Zouk", "genre_family", "world", "remove")}, 422, "CONFLICTING_MAPPINGS"},
		{"duplicate mapping", "why", []sounds.Mapping{me("zouk", "genre_family", "world", "add"), me("ZOUK", "genre_family", "world", "add")}, 422, "DUPLICATE_MAPPING"},
		{"nothing to propose", "why", []sounds.Mapping{me("Reggae", "genre_family", "reggae", "add")}, 422, "NOTHING_TO_PROPOSE"},
		{"stale remove", "why", []sounds.Mapping{me("nonsense words", "mood", "chill", "remove")}, 422, "MAPPING_NOT_EFFECTIVE"},
	} {
		_, err := w.sv.Propose(ctx, agentP, c.rationale, c.ms)
		wantProblem(t, c.name, err, c.status, c.code)
	}
	if n := w.count(`SELECT COUNT(*) FROM taxonomy_proposals`); n != 0 {
		t.Errorf("refused proposals left %d rows", n)
	}
	// An add that is already effective is dropped; the rest is stored folded, in call order, with the snapshot.
	d := w.draft("skank, 90bpm")
	w.settle()
	p, err := w.sv.Propose(ctx, agentP, "  skank is reggae  ", []sounds.Mapping{me("Reggae", "genre_family", "reggae", "add"), me(" SKANK ", "genre_family", "reggae", "add"), me("Zouk Love", "genre_family", "world", "add")})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "open" || p.Reused || p.BaseVersion != 1 || p.Rationale != "skank is reggae" || p.AppliedVersion != nil || p.DecidedAt != nil {
		t.Errorf("proposal %+v", p)
	}
	if want := []sounds.Mapping{me("skank", "genre_family", "reggae", "add"), me("zouk love", "genre_family", "world", "add")}; !slices.Equal(p.Mappings, want) {
		t.Errorf("mappings %+v, want %+v", p.Mappings, want)
	}
	if p.ProposedBy != (auth.PrincipalRef{Kind: "agent", ID: "agent:tok-1", Name: "agent-one"}) {
		t.Errorf("proposed by %+v", p.ProposedBy)
	}
	if p.Impact.TaxonomyVersion != 1 || p.Impact.SongsScanned != 1 || len(p.Impact.Gain) != 1 || p.Impact.Gain[0] != (sounds.FacetCount{Facet: "genre_family", Value: "reggae", Songs: 1}) {
		t.Errorf("impact %+v (draft %s)", p.Impact, d)
	}
	if got := w.str(`SELECT state || '/' || base_version || '/' || length(mappings_digest) || '/' || principal_id || '/' || principal_kind || '/' || principal_name FROM taxonomy_proposals WHERE id = ?`, p.ID); got != "open/1/64/agent:tok-1/agent/agent-one" {
		t.Errorf("row %q", got)
	}
	if w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != 1 {
		t.Errorf("a proposal wrote a taxonomy version")
	}
	// The stored row reads back equal.
	page, err := w.sv.ListProposals(ctx, sounds.ProposalFilter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %+v %v", page, err)
	}
	if got := page.Items[0]; got.ID != p.ID || !slices.Equal(got.Mappings, p.Mappings) || got.Impact.SongsScanned != 1 || got.CreatedAt != p.CreatedAt || got.ProposedBy != p.ProposedBy {
		t.Errorf("read back %+v, want %+v", got, p)
	}
}

func TestPropose_Reuse(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	a := []sounds.Mapping{me("zouk", "genre_family", "world", "add"), me("kizomba", "genre_family", "world", "add")}
	first, err := w.sv.Propose(ctx, agentP, "one", a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.sv.Propose(ctx, agentP, "another rationale", []sounds.Mapping{me("Kizomba", "genre_family", "world", "add"), me("zouk", "genre_family", "world", "add")})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.Reused || again.Rationale != "one" || first.Reused {
		t.Errorf("reuse: first %+v again %+v", first, again)
	}
	if n := w.count(`SELECT COUNT(*) FROM taxonomy_proposals`); n != 1 {
		t.Errorf("%d rows after a reused proposal", n)
	}
	other, err := w.sv.Propose(ctx, agentN(2), "same set, other agent", a)
	if err != nil || other.ID == first.ID || other.Reused {
		t.Errorf("another principal got %+v %v", other, err)
	}
	// A decided proposal is not reused.
	w.exec(`UPDATE taxonomy_proposals SET state = 'rejected', decided_at = ?, decided_by = 'human:op' WHERE id = ?`, "2026-10-06T12:00:00.000Z", first.ID)
	third, err := w.sv.Propose(ctx, agentP, "again", a)
	if err != nil || third.ID == first.ID || third.Reused {
		t.Errorf("after rejection: %+v %v", third, err)
	}
}

func TestPropose_Limits(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	n := 0
	propose := func(p auth.Principal) (sounds.Proposal, error) {
		n++
		return w.sv.Propose(ctx, p, "why", []sounds.Mapping{me(fmt.Sprintf("limit term %c%c", 'a'+n/26, 'a'+n%26), "mood", "chill", "add")})
	}
	var first sounds.Proposal
	for i := 0; i < sounds.ProposalMaxOpenPerPrincipal; i++ {
		p, err := propose(agentP)
		if err != nil {
			t.Fatalf("proposal %d: %v", i+1, err)
		}
		if i == 0 {
			first = p
		}
	}
	_, err := propose(agentP)
	if pe := wantProblem(t, "11th open", err, 409, "TAXONOMY_PROPOSAL_LIMIT"); pe != nil && pe.Details["scope"] != "principal" {
		t.Errorf("scope %v", pe.Details["scope"])
	}
	// A reused proposal is not a new row, so it is allowed at the limit.
	if r, err := w.sv.Propose(ctx, agentP, "why", first.Mappings); err != nil || !r.Reused {
		t.Errorf("reuse at the limit: %+v %v", r, err)
	}
	for a := 2; a <= 5; a++ {
		for i := 0; i < sounds.ProposalMaxOpenPerPrincipal; i++ {
			if _, err := propose(agentN(a)); err != nil {
				t.Fatalf("agent %d proposal %d: %v", a, i+1, err)
			}
		}
	}
	if got := w.count(`SELECT COUNT(*) FROM taxonomy_proposals WHERE state = 'open'`); got != sounds.ProposalMaxOpen {
		t.Fatalf("%d open", got)
	}
	_, err = propose(agentN(6))
	if pe := wantProblem(t, "51st overall", err, 409, "TAXONOMY_PROPOSAL_LIMIT"); pe != nil && pe.Details["scope"] != "total" {
		t.Errorf("scope %v", pe.Details["scope"])
	}
	// Deciding one frees a slot for everyone.
	w.exec(`UPDATE taxonomy_proposals SET state = 'rejected', decided_at = ?, decided_by = 'human:op' WHERE id = ?`, "2026-10-06T12:00:00.000Z", first.ID)
	if _, err := propose(agentN(6)); err != nil {
		t.Errorf("after a decision: %v", err)
	}
	_, err = propose(agentP)
	wantProblem(t, "library full again", err, 409, "TAXONOMY_PROPOSAL_LIMIT")
}

func TestPropose_HumanRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	for _, p := range []auth.Principal{humanP, deviceP} {
		_, err := w.sv.Propose(context.Background(), p, "why", []sounds.Mapping{me("zouk", "genre_family", "world", "add")})
		if pe := wantProblem(t, string(p.Kind), err, 403, "HUMAN_ONLY"); pe != nil && pe.Details["hint"] != "use map_sound_term" {
			t.Errorf("hint %v", pe.Details)
		}
	}
	if n := w.count(`SELECT COUNT(*) FROM taxonomy_proposals`); n != 0 {
		t.Errorf("%d rows", n)
	}
}

// facetPairs reads every (song, facet, value) of song_sounds.
func facetPairs(w *world) map[[3]string]bool {
	w.t.Helper()
	rows, err := w.st.R.Query(`SELECT song_id, facet, value FROM song_sounds`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[[3]string]bool{}
	for rows.Next() {
		var k [3]string
		if err := rows.Scan(&k[0], &k[1], &k[2]); err != nil {
			w.t.Fatal(err)
		}
		out[k] = true
	}
	return out
}

func unmappedCounts(w *world) map[string]int {
	w.t.Helper()
	rows, err := w.st.R.Query(`SELECT u.term, COUNT(*) FROM song_unmapped_terms u JOIN songs s ON s.id = u.song_id WHERE s.retired_at IS NULL GROUP BY u.term`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			w.t.Fatal(err)
		}
		out[t] = n
	}
	return out
}

func TestImpact_EqualsRecompute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	// Songs that gain, lose, keep and ignore: styles with an unmapped term, a mapped one, a language part, a Pick take.
	w.draft("skank, 90bpm")
	w.draft("skank, zzquux, warm")
	w.draft("jazz, mellow")
	w.draft("reggae, warm") // loses reggae: nothing else maps it
	t1 := w.track(w.recipe("english, reggae, dub, 80 bpm, wobble 3", "[Verse 1]\nla la"), trackOpts{})
	w.tempo(t1, 80)
	w.track(w.recipe("italian, dance pop, 125 bpm, zzquux", "[Verse 1]\nla"), trackOpts{})
	w.draft("")
	w.settle()
	w.pick(w.songOfTrack(t1), t1)
	w.settle()
	ms := []sounds.Mapping{
		me("skank", "genre_family", "reggae", "add"),
		me("zzquux", "mood", "chill", "add"),
		me("reggae", "genre_family", "reggae", "remove"),
		me("wobble 3", "mood", "dark", "add"),
		me("mellow", "mood", "chill", "add"),
		me("jazz", "genre_family", "jazz", "add"), // already effective: no change
	}
	versions := w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`)
	imp, err := w.sv.Impact(ctx, ms)
	if err != nil {
		t.Fatal(err)
	}
	if w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != versions || w.count(`SELECT COUNT(*) FROM taxonomy_proposals`) != 0 {
		t.Fatal("Impact wrote")
	}
	if imp.Truncated || imp.SongsScanned != w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL`) || imp.TaxonomyVersion != versions {
		t.Errorf("impact header %+v", imp)
	}
	beforePairs, beforeUn := facetPairs(w), unmappedCounts(w)
	if _, err := w.sv.Map(ctx, agentP, sounds.MapRequest{Mappings: ms}); err != nil {
		t.Fatal(err)
	}
	afterPairs, afterUn := facetPairs(w), unmappedCounts(w)
	gain, loss := map[[2]string]int{}, map[[2]string]int{}
	for k := range afterPairs {
		if !beforePairs[k] && k[1] != "tempo_band" {
			gain[[2]string{k[1], k[2]}]++
		}
	}
	for k := range beforePairs {
		if !afterPairs[k] && k[1] != "tempo_band" {
			loss[[2]string{k[1], k[2]}]++
		}
	}
	toCounts := func(m map[[2]string]int) []sounds.FacetCount {
		out := []sounds.FacetCount{}
		for k, n := range m {
			out = append(out, sounds.FacetCount{Facet: k[0], Value: k[1], Songs: n})
		}
		slices.SortFunc(out, func(a, b sounds.FacetCount) int {
			if a.Songs != b.Songs {
				return b.Songs - a.Songs
			}
			return strings.Compare(a.Facet+"/"+a.Value, b.Facet+"/"+b.Value)
		})
		return out
	}
	if want := toCounts(gain); !slices.Equal(imp.Gain, want) || len(want) == 0 {
		t.Errorf("gain predicted %+v, recompute changed %+v", imp.Gain, want)
	}
	if want := toCounts(loss); !slices.Equal(imp.Loss, want) || len(want) == 0 {
		t.Errorf("loss predicted %+v, recompute changed %+v", imp.Loss, want)
	}
	cleared := []sounds.TermCount{}
	for term, n := range beforeUn {
		if d := n - afterUn[term]; d > 0 {
			cleared = append(cleared, sounds.TermCount{Term: term, Songs: d})
		}
	}
	slices.SortFunc(cleared, func(a, b sounds.TermCount) int {
		if a.Songs != b.Songs {
			return b.Songs - a.Songs
		}
		return strings.Compare(a.Term, b.Term)
	})
	if !slices.Equal(imp.UnmappedCleared, cleared) || len(cleared) == 0 {
		t.Errorf("unmapped cleared predicted %+v, recompute cleared %+v", imp.UnmappedCleared, cleared)
	}
	// Mappings that change nothing predict nothing.
	none, err := w.sv.Impact(ctx, []sounds.Mapping{me("skank", "genre_family", "reggae", "add")})
	if err != nil || len(none.Gain)+len(none.Loss)+len(none.UnmappedCleared) != 0 || none.Gain == nil || none.Loss == nil || none.UnmappedCleared == nil {
		t.Errorf("no-op impact %+v %v", none, err)
	}
}

// draftSongs inserts n live Songs that each have a backlog draft with a one-term style (cheap to scan).
func draftSongs(t *testing.T, w *world, n int) {
	t.Helper()
	const cte = `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?) `
	for _, q := range []string{
		cte + `INSERT INTO recipe_drafts(id,origin,request_id,request_json,status,created_at,updated_at)
			SELECT printf('d%08d', i), 'import_backlog', printf('q%08d', i), json_object('style', CASE i % 3 WHEN 0 THEN 'skank' WHEN 1 THEN 'rock' ELSE 'zzquux' END, 'lyrics', 'x'), 'backlog', '2026-10-06T12:00:00Z', '2026-10-06T12:00:00Z' FROM s`,
		cte + `INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at)
			SELECT printf('s%08d', i), printf('d%08d', i), 'Synthetic', 'draft', 'system:provenance', printf('2026-10-06T12:00:00.%03dZ', i % 1000), 'system:provenance', '2026-10-06T12:00:00Z' FROM s`,
	} {
		if _, err := w.st.W.Exec(q, n); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func TestImpact_Truncated(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("20 001 Songs: make test runs it")
	}
	w := newWorld(t, true)
	draftSongs(t, w, sounds.ImpactMaxSongs+1)
	imp, err := w.sv.Impact(context.Background(), []sounds.Mapping{me("skank", "genre_family", "reggae", "add")})
	if err != nil {
		t.Fatal(err)
	}
	if !imp.Truncated || imp.SongsScanned != sounds.ImpactMaxSongs {
		t.Errorf("header %+v", imp)
	}
	if len(imp.Gain) != 1 || imp.Gain[0].Songs < sounds.ImpactMaxSongs/3-1 || imp.Gain[0].Songs > sounds.ImpactMaxSongs/3+1 {
		t.Errorf("gain %+v", imp.Gain)
	}
	w2 := newWorld(t, true)
	draftSongs(t, w2, 10)
	if imp, err := w2.sv.Impact(context.Background(), []sounds.Mapping{me("skank", "genre_family", "reggae", "add")}); err != nil || imp.Truncated || imp.SongsScanned != 10 {
		t.Errorf("small library %+v %v", imp, err)
	}
}

func TestProposals_ListFilters(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	w.draft("skank, 90bpm")
	w.settle()
	var ids []string
	for i := 0; i < 12; i++ {
		p, err := w.sv.Propose(ctx, agentN(1+i/10), "why", []sounds.Mapping{me(fmt.Sprintf("list term %c", 'a'+i), "mood", "chill", "add")})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	decide := func(id, state string) {
		if state == "accepted" {
			w.exec(`UPDATE taxonomy_proposals SET state = 'accepted', decided_at = ?, decided_by = 'human:op', applied_version = 1, accepted_json = '[0]', skipped_json = '[]' WHERE id = ?`, "2026-10-06T12:00:00.000Z", id)
			return
		}
		w.exec(`UPDATE taxonomy_proposals SET state = ?, decided_at = ?, decided_by = 'human:op' WHERE id = ?`, state, "2026-10-06T12:00:00.000Z", id)
	}
	decide(ids[0], "rejected")
	decide(ids[1], "accepted")
	decide(ids[2], "expired")
	all, err := w.sv.ListProposals(ctx, sounds.ProposalFilter{})
	if err != nil || all.Total != 12 || len(all.Items) != 12 || all.NextCursor != nil {
		t.Fatalf("all: total %d items %d %v", all.Total, len(all.Items), err)
	}
	for i, p := range all.Items { // newest first
		if p.ID != ids[11-i] {
			t.Fatalf("item %d is %s, want %s", i, p.ID, ids[11-i])
		}
	}
	got := func(f sounds.ProposalFilter) sounds.ProposalPage {
		t.Helper()
		pg, err := w.sv.ListProposals(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return pg
	}
	if pg := got(sounds.ProposalFilter{States: []string{"open"}}); pg.Total != 9 || len(pg.Items) != 9 {
		t.Errorf("open: %d/%d", pg.Total, len(pg.Items))
	}
	if pg := got(sounds.ProposalFilter{States: []string{"rejected", "expired"}}); pg.Total != 2 || len(pg.Items) != 2 {
		t.Errorf("rejected+expired: %d/%d", pg.Total, len(pg.Items))
	}
	acc := got(sounds.ProposalFilter{States: []string{"accepted"}})
	if len(acc.Items) != 1 || acc.Items[0].AppliedVersion == nil || *acc.Items[0].AppliedVersion != 1 || string(acc.Items[0].Accepted) != "[0]" || string(acc.Items[0].Skipped) != "[]" ||
		acc.Items[0].DecidedBy == nil || *acc.Items[0].DecidedBy != "human:op" || acc.Items[0].DecidedAt == nil {
		t.Errorf("accepted item %+v", acc.Items)
	}
	// Paging: 5 + 5 + 2, every id once, the total constant.
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		pg := got(sounds.ProposalFilter{Limit: 5, Cursor: cursor})
		if pg.Total != 12 {
			t.Errorf("total %d", pg.Total)
		}
		for _, p := range pg.Items {
			seen = append(seen, p.ID)
		}
		if pg.NextCursor == nil {
			if pages != 2 {
				t.Errorf("%d pages", pages+1)
			}
			break
		}
		cursor = *pg.NextCursor
	}
	slices.Reverse(ids)
	if !slices.Equal(seen, ids) {
		t.Errorf("paged %v, want %v", seen, ids)
	}
	for name, f := range map[string]sounds.ProposalFilter{
		"bad cursor": {Cursor: "!!"}, "limit 101": {Limit: 101}, "limit -1": {Limit: -1}, "bad state": {States: []string{"nope"}},
	} {
		_, err := w.sv.ListProposals(ctx, f)
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 422 {
			t.Errorf("%s: %v", name, err)
		}
	}
	// include_impact recomputes at most 10 open proposals against the current taxonomy; the rest keep the snapshot.
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	fresh := got(sounds.ProposalFilter{States: []string{"open"}, IncludeImpact: true})
	if len(fresh.Items) != 9 {
		t.Fatalf("%d open", len(fresh.Items))
	}
	for i, p := range fresh.Items {
		if p.Impact.TaxonomyVersion != 2 {
			t.Errorf("item %d impact version %d, want recomputed (2)", i, p.Impact.TaxonomyVersion)
		}
	}
	// Decided proposals keep their snapshot even without the state filter.
	more := got(sounds.ProposalFilter{IncludeImpact: true})
	recomputed := 0
	for _, p := range more.Items {
		if p.Impact.TaxonomyVersion == 2 {
			recomputed++
			if p.State != "open" {
				t.Errorf("a decided proposal was recomputed: %s", p.State)
			}
		}
	}
	if recomputed != 9 {
		t.Errorf("%d recomputed, want the 9 open ones", recomputed)
	}
	if snap := got(sounds.ProposalFilter{States: []string{"open"}}); snap.Items[0].Impact.TaxonomyVersion != 1 {
		t.Errorf("the snapshot changed: %+v", snap.Items[0].Impact)
	}
}

func TestProposals_ListImpactCap(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if _, err := w.sv.Propose(ctx, agentN(1+i/10), "why", []sounds.Mapping{me(fmt.Sprintf("cap term %c", 'a'+i), "mood", "chill", "add")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	pg, err := w.sv.ListProposals(ctx, sounds.ProposalFilter{IncludeImpact: true})
	if err != nil {
		t.Fatal(err)
	}
	fresh := 0
	for _, p := range pg.Items {
		if p.Impact.TaxonomyVersion == 2 {
			fresh++
		}
	}
	if fresh != sounds.ImpactRecomputeMax || len(pg.Items) != 12 {
		t.Errorf("%d of %d recomputed, want %d", fresh, len(pg.Items), sounds.ImpactRecomputeMax)
	}
}

func TestProposals_Canaries(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	const rat, term = "CANARY-RATIONALE-9c41", "canaryterm9c41"
	w.draft(term + ", 90bpm")
	w.settle()
	p, err := w.sv.Propose(ctx, agentP, rat+" because", []sounds.Mapping{me(term, "mood", "dark", "add")})
	if err != nil {
		t.Fatal(err)
	}
	// Refusals never echo agent text.
	for name, c := range map[string]struct {
		rationale string
		ms        []sounds.Mapping
	}{
		"bad facet":   {rat, []sounds.Mapping{me(term, "colour", "red", "add")}},
		"duplicate":   {rat, []sounds.Mapping{me(term, "mood", "dark", "add"), me(term, "mood", "dark", "add")}},
		"stale":       {rat, []sounds.Mapping{me(term+"x", "mood", "dark", "remove")}},
		"too long":    {strings.Repeat(rat, 50), []sounds.Mapping{me(term, "mood", "dark", "add")}},
		"nothing":     {rat, []sounds.Mapping{me("reggae", "genre_family", "reggae", "add")}},
		"conflicting": {rat, []sounds.Mapping{me(term, "mood", "dark", "add"), me(term, "mood", "dark", "remove")}},
	} {
		_, err := w.sv.Propose(ctx, agentP, c.rationale, c.ms)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		var pe *problem.Error
		if !errors.As(err, &pe) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		b, _ := json.Marshal(pe.Details)
		if text := err.Error() + string(b) + pe.Message; strings.Contains(strings.ToLower(text), "canary") {
			t.Errorf("%s: the error carries agent text: %s", name, text)
		}
	}
	// The agent text lives in taxonomy_proposals only (and in the draft's own style and its unmapped cache).
	tables, err := w.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	hits := map[string]int{}
	for _, n := range names {
		rows, err := w.st.R.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				s := ""
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				}
				if strings.Contains(strings.ToLower(s), "canary") {
					hits[n]++
				}
			}
		}
		rows.Close()
	}
	for n := range hits {
		switch {
		case n == "taxonomy_proposals" || n == "recipe_drafts" || n == "song_unmapped_terms" || strings.HasPrefix(n, "drafts_fts"):
		default:
			t.Errorf("agent text reached table %s", n)
		}
	}
	if hits["taxonomy_proposals"] == 0 {
		t.Error("the proposal row does not hold its rationale")
	}
	page, err := w.sv.ListProposals(ctx, sounds.ProposalFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != p.ID || !strings.Contains(page.Items[0].Rationale, rat) || page.Items[0].Mappings[0].Term != term {
		t.Errorf("list does not return the agent text: %+v %v", page, err)
	}
}
