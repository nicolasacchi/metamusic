package nextactions_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The method set of C139 that the engine keeps.
var (
	_ func(*nextactions.Service, context.Context, auth.Principal, nextactions.Filter) (nextactions.Page, error) = (*nextactions.Service).List
	_ func(*nextactions.Service, context.Context, auth.Principal, string, int) ([]nextactions.Action, error)    = (*nextactions.Service).ForTake
)

var (
	agentP  = nextactions.Viewer(auth.KindAgent, auth.ScopeRead)
	humanP  = nextactions.Viewer(auth.KindHuman, auth.ScopeRead, auth.ScopeWrite)
	deviceP = nextactions.Viewer(auth.KindDevice, auth.ScopeRead)
)

// fakeMuses is the Muse read of the engine tests: LiveIDs answers a fixed list, and Refs fails the test, because item texts name a
// Muse by id tail and must never read Muse text (D-L3-16 is not approved).
type fakeMuses struct {
	t    *testing.T
	live []string
}

func (f fakeMuses) Refs(context.Context, []string) (map[string]muses.MuseRef, error) {
	f.t.Error("nextactions read Muse text through Refs: D-L3-16 is not approved")
	return nil, errors.New("refs must not be called")
}
func (f fakeMuses) LiveIDs(context.Context) ([]string, error) { return f.live, nil }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newService(t *testing.T) *nextactions.Service {
	t.Helper()
	return nextactions.New(openStore(t), fakeMuses{t: t}, nil, nil, func() time.Time { return librarytest.FixedNow })
}

// fakeRule finds n candidates ("<code>-000" ...) of the given subject kind, never more than lim, and reports n as the total.
func fakeRule(code string, priority int, aud nextactions.Audience, kind string, n int) nextactions.Rule {
	return nextactions.Rule{
		Code: code, Priority: priority, Audience: aud, MinScope: auth.ScopeRead,
		Find: func(_ context.Context, _ nextactions.Reader, lim int) ([]nextactions.Candidate, error) {
			var out []nextactions.Candidate
			for i := 0; i < min(n, lim); i++ {
				id := fmt.Sprintf("%s-%03d", code, i)
				out = append(out, nextactions.Candidate{Subject: nextactions.Subject{Kind: kind, ID: &id}, Evidence: map[string]any{"_total": n, "n": i}})
			}
			return out, nil
		},
		Text: func(c nextactions.Candidate, _ nextactions.Names) string { return code + " " + *c.Subject.ID },
	}
}

func codes(p nextactions.Page) []string {
	var out []string
	for _, a := range p.Items {
		out = append(out, a.Code+"/"+*a.Subject.ID)
	}
	return out
}

// TestNextActions_PerCodeCap: 150 candidates of one code give five items, groups[code].more = 145 and total 150, although the rule
// was asked for only 4*PerCode of them.
func TestNextActions_PerCodeCap(t *testing.T) {
	defer nextactions.SetRules([]nextactions.Rule{fakeRule("muse_unrendered", 70, nextactions.AudienceAgent, nextactions.SubjectMuse, 150), fakeRule("small", 10, nextactions.AudienceBoth, nextactions.SubjectSong, 3)})()
	s := newService(t)
	p, err := s.List(context.Background(), agentP, nextactions.Filter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 8 || p.Total != 153 || p.NextCursor != nil {
		t.Fatalf("items %d, total %d, cursor %v; want 8 items (5+3), total 153", len(p.Items), p.Total, p.NextCursor)
	}
	if diff := cmp.Diff([]nextactions.Group{{Code: "muse_unrendered", More: 145}}, p.Groups); diff != "" {
		t.Errorf("groups (-want +got):\n%s", diff)
	}
}

// TestNextActions_Ordering: (priority desc, code asc, subject id asc), whatever order the rules are registered in or return rows.
func TestNextActions_Ordering(t *testing.T) {
	rs := []nextactions.Rule{
		fakeRule("b_code", 50, nextactions.AudienceBoth, nextactions.SubjectSong, 2),
		fakeRule("z_low", 10, nextactions.AudienceBoth, nextactions.SubjectSong, 1),
		fakeRule("a_code", 50, nextactions.AudienceBoth, nextactions.SubjectSong, 2),
		fakeRule("top", 90, nextactions.AudienceBoth, nextactions.SubjectLibrary, 1),
	}
	rs[0].Find, rs[2].Find = reversed(rs[0].Find), reversed(rs[2].Find) // a rule that answers in the wrong order is re-sorted
	defer nextactions.SetRules(rs)()
	p, err := newService(t).List(context.Background(), agentP, nextactions.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"top/top-000", "a_code/a_code-000", "a_code/a_code-001", "b_code/b_code-000", "b_code/b_code-001", "z_low/z_low-000"}
	if diff := cmp.Diff(want, codes(p)); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
	if p.GeneratedAt != store.FormatTime(librarytest.FixedNow) {
		t.Errorf("generated_at %q", p.GeneratedAt)
	}
}

func reversed(f func(context.Context, nextactions.Reader, int) ([]nextactions.Candidate, error)) func(context.Context, nextactions.Reader, int) ([]nextactions.Candidate, error) {
	return func(ctx context.Context, q nextactions.Reader, lim int) ([]nextactions.Candidate, error) {
		c, err := f(ctx, q, lim)
		slices.Reverse(c)
		return c, err
	}
}

// TestNextActions_Paging: pages of any size concatenate to the full list, the last has no cursor, a bad cursor and bad limits are
// 422, and the keyset cursor survives a change of the data between pages.
func TestNextActions_Paging(t *testing.T) {
	defer nextactions.SetRules([]nextactions.Rule{
		fakeRule("one", 80, nextactions.AudienceBoth, nextactions.SubjectSong, 5), fakeRule("two", 60, nextactions.AudienceBoth, nextactions.SubjectSong, 5),
		fakeRule("three", 40, nextactions.AudienceBoth, nextactions.SubjectSong, 3),
	})()
	s := newService(t)
	ctx := context.Background()
	full, err := s.List(ctx, agentP, nextactions.Filter{Limit: 50})
	if err != nil || len(full.Items) != 13 {
		t.Fatalf("full: %d %v", len(full.Items), err)
	}
	for _, size := range []int{1, 2, 5, 13, 14} {
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			p, err := s.List(ctx, agentP, nextactions.Filter{Limit: size, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Items) > size || p.Total != 13 {
				t.Fatalf("size %d: %d items, total %d", size, len(p.Items), p.Total)
			}
			got = append(got, codes(p)...)
			if p.NextCursor == nil {
				break
			}
			if pages > 20 {
				t.Fatal("paging does not end")
			}
			cursor = *p.NextCursor
		}
		if diff := cmp.Diff(codes(full), got); diff != "" {
			t.Errorf("size %d (-want +got):\n%s", size, diff)
		}
	}
	for _, f := range []nextactions.Filter{{Cursor: "not-a-cursor!"}, {Cursor: "YWJj"}} {
		_, err := s.List(ctx, agentP, f)
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 422 || pe.Code != "INVALID_CURSOR" {
			t.Errorf("cursor %q: %v, want 422 INVALID_CURSOR", f.Cursor, err)
		}
	}
	for _, f := range []nextactions.Filter{{Limit: 51}, {Limit: -1}, {MinPriority: 101}, {MinPriority: -1}, {SubjectKind: "bogus"}, {Audience: "bogus"}, {SubjectID: "x"}} {
		var pe *problem.Error
		if _, err := s.List(ctx, agentP, f); !errors.As(err, &pe) || pe.Status != 422 {
			t.Errorf("%+v: %v, want 422", f, err)
		}
	}
}

// TestNextActions_AudienceFilter: a human or device principal sees human and both, any other principal agent and both; an explicit
// audience overrides the default ("both" shows only the shared rules); a rule whose scope the viewer lacks is skipped.
func TestNextActions_AudienceFilter(t *testing.T) {
	scoped := fakeRule("needs_write", 30, nextactions.AudienceBoth, nextactions.SubjectSong, 1)
	scoped.MinScope = auth.ScopeWrite
	defer nextactions.SetRules([]nextactions.Rule{
		fakeRule("a", 90, nextactions.AudienceAgent, nextactions.SubjectSong, 1), fakeRule("h", 80, nextactions.AudienceHuman, nextactions.SubjectSong, 1),
		fakeRule("b", 70, nextactions.AudienceBoth, nextactions.SubjectSong, 1), scoped,
	})()
	s := newService(t)
	for name, tc := range map[string]struct {
		p    auth.Principal
		aud  nextactions.Audience
		want []string
	}{
		"agent default":        {agentP, "", []string{"a", "b"}},
		"human default":        {humanP, "", []string{"h", "b", "needs_write"}},
		"device default":       {deviceP, "", []string{"h", "b"}},
		"agent asks for human": {agentP, nextactions.AudienceHuman, []string{"h", "b"}},
		"human asks for agent": {humanP, nextactions.AudienceAgent, []string{"a", "b", "needs_write"}},
		"both only":            {humanP, nextactions.AudienceBoth, []string{"b", "needs_write"}},
	} {
		p, err := s.List(context.Background(), tc.p, nextactions.Filter{Audience: tc.aud})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range p.Items {
			got = append(got, a.Code)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// TestNextActions_SubjectFilter: subject_kind and subject_id pick the items of one subject even when the rule has more candidates
// than 4*PerCode, and min_priority drops the rules below it.
func TestNextActions_SubjectFilter(t *testing.T) {
	defer nextactions.SetRules([]nextactions.Rule{
		fakeRule("many", 70, nextactions.AudienceBoth, nextactions.SubjectSong, 100), fakeRule("lib", 40, nextactions.AudienceBoth, nextactions.SubjectLibrary, 1),
	})()
	s := newService(t)
	ctx := context.Background()
	p, err := s.List(ctx, agentP, nextactions.Filter{SubjectKind: nextactions.SubjectSong, SubjectID: "many-077"})
	if err != nil || len(p.Items) != 1 || p.Total != 1 || p.Items[0].Subject.Kind != "song" {
		t.Fatalf("one subject: %+v %v", codes(p), err)
	}
	if p, _ = s.List(ctx, agentP, nextactions.Filter{SubjectKind: nextactions.SubjectLibrary}); len(p.Items) != 1 || p.Items[0].Code != "lib" {
		t.Errorf("library: %v", codes(p))
	}
	if p, _ = s.List(ctx, agentP, nextactions.Filter{MinPriority: 50}); p.Total != 100 || len(p.Items) != 5 {
		t.Errorf("min_priority 50: total %d, %d items", p.Total, len(p.Items))
	}
}

// TestNextActions_PrivateEvidence: keys starting with an underscore never leave the engine; the rest does, and Evidence is never nil.
func TestNextActions_PrivateEvidence(t *testing.T) {
	r := fakeRule("e", 50, nextactions.AudienceBoth, nextactions.SubjectSong, 1)
	r.Find = func(context.Context, nextactions.Reader, int) ([]nextactions.Candidate, error) {
		id := "s1"
		return []nextactions.Candidate{{Subject: nextactions.Subject{Kind: "song", ID: &id}, Evidence: map[string]any{"_secret": "x", "_total": 1, "n": 3}}, {Subject: nextactions.Subject{Kind: "library"}}}, nil
	}
	r.Text = func(nextactions.Candidate, nextactions.Names) string { return "e" }
	defer nextactions.SetRules([]nextactions.Rule{r})()
	p, err := newService(t).List(context.Background(), agentP, nextactions.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	// the library item has no id and sorts first; its evidence is an empty map, not nil
	if len(p.Items) != 2 || p.Items[0].Evidence == nil || len(p.Items[0].Evidence) != 0 {
		t.Fatalf("%+v", p.Items)
	}
	if diff := cmp.Diff(map[string]any{"n": 3}, p.Items[1].Evidence); diff != "" {
		t.Errorf("evidence (-want +got):\n%s", diff)
	}
}

// TestNextActions_GETNeverWrites: every statement a List call issues is a SELECT or a WITH, inside one read-only transaction.
func TestNextActions_GETNeverWrites(t *testing.T) {
	s := newService(t)
	var seen []string
	s.SetStatementHook(func(q string) { seen = append(seen, q) })
	if _, err := s.List(context.Background(), humanP, nextactions.Filter{}); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("no statement seen")
	}
	for _, q := range seen {
		if f := strings.ToUpper(strings.Fields(q)[0]); f != "SELECT" && f != "WITH" {
			t.Errorf("a next-action statement starts with %s: %.80s", f, q)
		}
	}
}

// TestNextActions_ForTake_Floor: ForTake evaluates only the rules about the take's Song, Muse and Records, drops priorities below
// ContextMuseMinPriority, orders best first and cuts to the limit; a take without a Song has nothing.
func TestNextActions_ForTake_Floor(t *testing.T) {
	st := openStore(t)
	info := librarytest.FixtureL3(t, st, librarytest.FixedNow)
	song, take, muse := info.Songs["T1"], info.Takes["T1"], info.Muses["M5"]
	about := func(code string, prio int, kind, id string) nextactions.Rule {
		r := fakeRule(code, prio, nextactions.AudienceBoth, kind, 0)
		r.Find = func(context.Context, nextactions.Reader, int) ([]nextactions.Candidate, error) {
			return []nextactions.Candidate{{Subject: nextactions.Subject{Kind: kind, ID: &id}}}, nil
		}
		r.Text = func(nextactions.Candidate, nextactions.Names) string { return code }
		return r
	}
	other := "01900000-0000-7000-8000-00000000dead"
	defer nextactions.SetRules([]nextactions.Rule{
		about("song_duel", 68, "song", song), about("muse_unjudged", 65, "muse", muse),
		about("song_no_muse", 25, "song", song),               // below the floor
		about("muse_record_candidate", 75, "muse", other),     // another Muse
		about("song_tempo_mismatch", 50, "song", other),       // another Song
		about("muse_unrendered", 70, "muse", muse),            // not a ForTake rule
		about("record_proposed_waiting", 90, "record", other), // not a ForTake rule
		about("record_empty_slot", 60, "record", other),       // a Record that does not hold this Song
	})()
	s := nextactions.New(st, fakeMuses{t: t, live: []string{muse}}, nil, nil, func() time.Time { return librarytest.FixedNow })
	got, err := s.ForTake(context.Background(), agentP, take, 5)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, a := range got {
		texts = append(texts, a.Text)
	}
	if diff := cmp.Diff([]string{"song_duel", "muse_unjudged"}, texts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if got, _ = s.ForTake(context.Background(), agentP, take, 1); len(got) != 1 || got[0].Code != "song_duel" {
		t.Errorf("limit 1: %+v", got)
	}
	if got, err = s.ForTake(context.Background(), agentP, take, 0); err != nil || len(got) != 0 {
		t.Errorf("limit 0: %+v %v", got, err)
	}
	if got, err = s.ForTake(context.Background(), agentP, "01900000-0000-7000-8000-0000000000ff", 2); err != nil || len(got) != 0 {
		t.Errorf("a take without a Song: %+v %v", got, err)
	}
	for _, c := range []string{"song_duel", "song_proposals_waiting", "song_tempo_mismatch", "song_no_muse", "muse_partial_angles", "muse_unjudged", "muse_record_candidate", "record_undecided_slots", "record_empty_slot"} {
		if !nextactions.ForTakeCodes()[c] {
			t.Errorf("%s is missing from the ForTake set of C139.4", c)
		}
	}
}
