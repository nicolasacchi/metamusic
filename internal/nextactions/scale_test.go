package nextactions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// museWorld is a bare store with n live Muses that each own one draft Song, and a Service over it with the real Muse service.
func museWorld(t *testing.T, n int) (*nextactions.Service, *store.Store) {
	t.Helper()
	st := openStore(t)
	now := func() time.Time { return lt.FixedNow }
	k := 0
	ms := muses.New(st, now, func() string { k++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe30000+k) })
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}
	// the writer pool has one connection: create the Muses first, then hold one transaction for the drafts
	ids := make([]string, n)
	for i := range ids {
		m, err := ms.Create(context.Background(), human, muses.NewMuse{Kind: muses.KindReference, Title: fmt.Sprintf("Synthetic Scale Muse %04d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = m.ID
	}
	tx, err := st.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	at := store.FormatTime(lt.FixedNow)
	for i, museID := range ids {
		did := fmt.Sprintf("d%08d", i)
		for _, q := range []struct {
			q    string
			args []any
		}{
			{`INSERT INTO recipe_drafts(id,origin,group_key,label,request_id,request_json,status,created_at,updated_at) VALUES(?,'import_backlog',?,'v1',?,'{"id":"x"}','backlog',?,?)`, []any{did, "g" + did, "q" + did, at, at}},
			{`INSERT INTO songs(id,root_track_id,draft_id,muse_id,muse_source,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,NULL,?,?,'manual','Synthetic Draft','draft','system:provenance',?,'system:provenance',?)`, []any{"s" + did, did, museID, at, at}},
		} {
			if _, err := tx.Exec(q.q, q.args...); err != nil {
				t.Fatalf("%v\n%s", err, q.q)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nextactions.New(st, ms, nil, nil, now), st
}

// TestNextActions_PerCodeCap_Real: the real muse_unrendered rule over 150 Muses of drafts: five items, 145 more, total 150, in one
// statement that carries the total, not 150 rows.
func TestNextActions_PerCodeCap_Real(t *testing.T) {
	t.Parallel()
	s, _ := museWorld(t, 150)
	var statements int
	s.SetStatementHook(func(string) { statements++ })
	p, err := s.List(context.Background(), agentP, nextactions.Filter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 5 || p.Total != 150 || len(p.Groups) != 1 || p.Groups[0].Code != "muse_unrendered" || p.Groups[0].More != 145 {
		t.Fatalf("items %d, total %d, groups %+v", len(p.Items), p.Total, p.Groups)
	}
	for _, a := range p.Items {
		if a.Evidence["songs"] != 1 || a.Hint == nil {
			t.Errorf("%+v", a)
		}
	}
	if len(p.Items) > 1 && *p.Items[0].Subject.ID >= *p.Items[1].Subject.ID {
		t.Errorf("items are not ordered by subject id: %v", subjects(p.Items))
	}
}

// TestNextActions_StatementCount: a call issues a fixed number of statements, however many Muses and candidates exist: one per
// admitted rule, one title lookup per subject kind that has items, and none per row.
func TestNextActions_StatementCount(t *testing.T) {
	t.Parallel()
	count := func(n int, p auth.Principal) int {
		s, _ := museWorld(t, n)
		c := 0
		s.SetStatementHook(func(string) { c++ })
		if _, err := s.List(context.Background(), p, nextactions.Filter{Limit: 50}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, p := range []auth.Principal{agentP, humanP} {
		small, large := count(1, p), count(150, p)
		if small != large {
			t.Errorf("%s: %d statements for one Muse, %d for 150: a statement per row", p.Kind, small, large)
		}
		if small > len(nextactions.Rules)+2 {
			t.Errorf("%s: %d statements, want at most one per rule plus two title lookups", p.Kind, small)
		}
	}
}

// TestNextActions_NoMuseTableSQL: no statement a call issues, and no SQL literal in the package, names the muses tables; live Muses
// arrive as a json_each parameter from muses.Service.LiveIDs (C133).
func TestNextActions_NoMuseTableSQL(t *testing.T) {
	t.Parallel()
	s, _ := museWorld(t, 3)
	var seen []string
	s.SetStatementHook(func(q string) { seen = append(seen, q) })
	for _, p := range []auth.Principal{agentP, humanP} {
		if _, err := s.List(context.Background(), p, nextactions.Filter{}); err != nil {
			t.Fatal(err)
		}
	}
	re := regexp.MustCompile(`(?i)\bmuses(_fts)?\b`)
	for _, q := range seen {
		if re.MatchString(q) {
			t.Errorf("a statement names a muses table: %.120s", q)
		}
	}
	// the sources: every string literal of every non-test file of the package
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			if strings.Contains(strings.ToUpper(v), "SELECT ") {
				checked++
				if re.MatchString(v) {
					t.Errorf("%s: SQL names a muses table: %.80s", f, v)
				}
			}
			return true
		})
	}
	if checked < 5 {
		t.Errorf("only %d SQL literals found: the scan is vacuous", checked)
	}
}

// TestNextActions_NotPersisted: no item text ever reaches audit_log, idempotency or job_events, after List and ForTake and after
// the Reconciler and a Settle have run (C144.2).
func TestNextActions_NotPersisted(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
	pg := list(t, r, agent, nextactions.Filter{Limit: 50})
	u1 := str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song u1'`)
	fa, err := r.Services.NextActions.ForTake(context.Background(), agent, u1, 2)
	if err != nil {
		t.Fatal(err)
	}
	r.Settle(t)
	var texts []string
	for _, a := range slices.Concat(pg.Items, fa) {
		texts = append(texts, a.Text)
	}
	if len(texts) < 3 {
		t.Fatalf("only %d texts", len(texts))
	}
	for _, table := range []string{"audit_log", "idempotency", "job_events"} {
		rows, err := r.Store.R.Query(`SELECT * FROM ` + table)
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
			row, _ := json.Marshal(fmt.Sprint(vals...))
			for _, text := range texts {
				if strings.Contains(fmt.Sprint(vals...), text) || strings.Contains(string(row), strings.ReplaceAll(text, `"`, `\"`)) {
					t.Errorf("%s holds the next-action text %q", table, text)
				}
			}
		}
		_ = rows.Close()
	}
}

// TestPerf_NextActions: over 600 Songs and 200 Muses (the L3 scale fixture; it holds no Record) List(limit 20) for an agent, the
// widest audience, and ForTake stay inside the budgets of C146.4 (200 ms and 30 ms) on the best of five runs; serial on purpose,
// the budget is process CPU time.
func TestPerf_NextActions(t *testing.T) {
	listBudget := perftest.BudgetRace(t, 200*time.Millisecond, 40)
	takeBudget := perftest.BudgetRace(t, 30*time.Millisecond, 40)
	st := openStore(t)
	now := func() time.Time { return lt.FixedNow }
	lt.FixtureL3Scale(t, st, lt.FixedNow, 600, 200)
	s := nextactions.New(st, muses.New(st, now, nil), nil, nil, now)
	ctx := context.Background()
	best := func(f func()) time.Duration {
		f() // warm the page cache and the plans
		var fastest time.Duration
		for i := 0; i < 5; i++ {
			sw := perftest.Start()
			f()
			if el := sw.Elapsed(); i == 0 || el < fastest {
				fastest = el
			}
		}
		return fastest
	}
	var total int
	if el := best(func() {
		p, err := s.List(ctx, agentP, nextactions.Filter{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		total = p.Total
	}); el > listBudget {
		t.Errorf("List(limit 20) over 600 Songs and 200 Muses took %v, budget %v", el, listBudget)
	}
	if total == 0 {
		t.Error("the scale library has no next action: the measurement is vacuous")
	}
	var take string // a take of a Song that has a Muse
	if err := st.R.QueryRow(`SELECT root_track_id FROM songs WHERE root_track_id IS NOT NULL AND muse_id IS NOT NULL ORDER BY id LIMIT 1`).Scan(&take); err != nil {
		t.Fatal(err)
	}
	if el := best(func() {
		if _, err := s.ForTake(ctx, agentP, take, 2); err != nil {
			t.Fatal(err)
		}
	}); el > takeBudget {
		t.Errorf("ForTake took %v, budget %v", el, takeBudget)
	}
}
