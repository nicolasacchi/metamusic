package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func naRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true})
}

type naPage struct {
	Items []struct {
		Code     string `json:"code"`
		Priority int    `json:"priority"`
		Audience string `json:"audience"`
		Subject  struct {
			Kind string
			ID   *string
		} `json:"subject"`
		Text string `json:"text"`
		Hint *struct {
			Op   string
			Args map[string]any
		} `json:"hint"`
		Evidence map[string]any `json:"evidence"`
	} `json:"items"`
	Groups []struct {
		Code string
		More int
	} `json:"groups"`
	Total       int     `json:"total"`
	NextCursor  *string `json:"next_cursor"`
	GeneratedAt string  `json:"generated_at"`
}

func getNA(t *testing.T, c *servertest.Client, query string) (int, naPage, map[string]any) {
	t.Helper()
	resp := c.Do(t, "GET", "/api/next-actions"+query, nil)
	var page naPage
	var problem map[string]any
	if resp.Status == 200 {
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			t.Fatalf("%v: %s", err, resp.Body)
		}
	} else {
		_ = json.Unmarshal(resp.Body, &problem)
	}
	return resp.Status, page, problem
}

// TestListNextActions_REST: an agent gets the Muse-aware items of its audience with hints, the human gets its own; the shape is
// the frozen one (non-null lists, evidence maps, an ISO timestamp), the Muse is named by id prefix and a Muse title never appears.
func TestListNextActions_REST(t *testing.T) {
	t.Parallel()
	r := naRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	status, page, _ := getNA(t, agent, "?limit=50")
	if status != 200 || len(page.Items) < 3 || page.Total < 3 || page.NextCursor != nil || !strings.HasSuffix(page.GeneratedAt, "Z") {
		t.Fatalf("agent page: %d %+v", status, page)
	}
	codes := map[string]bool{}
	for _, it := range page.Items {
		codes[it.Code] = true
		if it.Evidence == nil || it.Audience == "human" {
			t.Errorf("%s: evidence %v audience %s", it.Code, it.Evidence, it.Audience)
		}
		if strings.Contains(it.Text, "Synthetic") {
			t.Errorf("text %q carries Muse text", it.Text)
		}
		if it.Hint != nil && it.Hint.Op == "estimate_generation" {
			t.Errorf("a read-only agent got %s", it.Hint.Op)
		}
	}
	if !codes["muse_unrendered"] || !codes["muse_unjudged"] || codes["record_proposed_waiting"] || codes["taxonomy_proposals_open"] {
		t.Errorf("agent codes %v", codes)
	}
	gen, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	if _, p, _ := getNA(t, gen, "?subject_kind=muse&limit=50"); !slices.ContainsFunc(p.Items, func(i struct {
		Code     string `json:"code"`
		Priority int    `json:"priority"`
		Audience string `json:"audience"`
		Subject  struct {
			Kind string
			ID   *string
		} `json:"subject"`
		Text string `json:"text"`
		Hint *struct {
			Op   string
			Args map[string]any
		} `json:"hint"`
		Evidence map[string]any `json:"evidence"`
	}) bool {
		return i.Hint != nil && i.Hint.Op == "estimate_generation"
	}) {
		t.Error("an agent with generate scope got no estimate hint")
	}
	human := r.Human(t)
	status, hp, _ := getNA(t, human, "")
	if status != 200 {
		t.Fatalf("human: %d", status)
	}
	for _, it := range hp.Items {
		if it.Audience == "agent" {
			t.Errorf("a human got the agent rule %s", it.Code)
		}
	}
	// anonymous callers are refused
	resp, err := http.Get(r.URL + "/api/next-actions")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
}

// TestListNextActions_Validation: the schema and the engine refuse bad input with 422 and stable codes, and the cursor pages the
// same list.
func TestListNextActions_Validation(t *testing.T) {
	t.Parallel()
	r := naRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	for _, q := range []string{"?limit=0", "?limit=51", "?min_priority=101", "?min_priority=-1", "?audience=robot", "?subject_kind=bogus", "?subject_id=x"} {
		if status, _, p := getNA(t, agent, q); status != 422 {
			t.Errorf("%s: %d %v, want 422", q, status, p)
		}
	}
	status, _, p := getNA(t, agent, "?cursor="+url.QueryEscape("not-a-cursor!"))
	if status != 422 || p["code"] != "INVALID_CURSOR" {
		t.Errorf("bad cursor: %d %v", status, p)
	}
	_, full, _ := getNA(t, agent, "?limit=50")
	var got []string
	q := "?limit=1"
	for i := 0; i < 20; i++ {
		_, pg, _ := getNA(t, agent, q)
		for _, it := range pg.Items {
			got = append(got, it.Code+"/"+*it.Subject.ID)
		}
		if pg.NextCursor == nil {
			break
		}
		q = "?limit=1&cursor=" + url.QueryEscape(*pg.NextCursor)
	}
	var want []string
	for _, it := range full.Items {
		want = append(want, it.Code+"/"+*it.Subject.ID)
	}
	if !slices.Equal(got, want) || len(want) < 3 {
		t.Errorf("pages %v, full %v", got, want)
	}
	// a Muse filter narrows to that Muse and min_priority drops the lower rules
	m4 := lt.L3Muse4Title
	var id string
	if err := r.Store.R.QueryRow(`SELECT id FROM muses WHERE title = ?`, m4).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, pg, _ := getNA(t, agent, "?subject_kind=muse&subject_id="+id); len(pg.Items) != 1 || *pg.Items[0].Subject.ID != id || pg.Items[0].Code != "muse_unjudged" {
		t.Errorf("muse filter: %+v", pg.Items)
	}
	if _, pg, _ := getNA(t, agent, "?min_priority=70"); slices.ContainsFunc(pg.Items, func(i struct {
		Code     string `json:"code"`
		Priority int    `json:"priority"`
		Audience string `json:"audience"`
		Subject  struct {
			Kind string
			ID   *string
		} `json:"subject"`
		Text string `json:"text"`
		Hint *struct {
			Op   string
			Args map[string]any
		} `json:"hint"`
		Evidence map[string]any `json:"evidence"`
	}) bool {
		return i.Priority < 70
	}) {
		t.Error("min_priority 70 returned a lower item")
	}
}

// TestNextActions_GETNeverWrites_REST: three calls leave idempotency, job events and the provenance queue as they were; the only
// new rows are the three audit rows of the calls themselves (operation id, hashed arguments, no text).
func TestNextActions_GETNeverWrites_REST(t *testing.T) {
	t.Parallel()
	r := naRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	count := func() (n [4]int) {
		for i, q := range []string{`SELECT COUNT(*) FROM audit_log`, `SELECT COUNT(*) FROM idempotency`, `SELECT COUNT(*) FROM job_events`, `SELECT COUNT(*) FROM provenance_dirty`} {
			if err := r.Store.R.QueryRow(q).Scan(&n[i]); err != nil {
				t.Fatal(err)
			}
		}
		return n
	}
	before := count()
	var text string
	for i := 0; i < 3; i++ {
		status, p, _ := getNA(t, agent, "?limit=50")
		if status != 200 || len(p.Items) == 0 {
			t.Fatal(status)
		}
		text = p.Items[0].Text
	}
	after := count()
	if after[0] != before[0]+3 || after[1] != before[1] || after[2] != before[2] || after[3] != before[3] {
		t.Errorf("rows changed: %v -> %v, want only +3 audit rows", before, after)
	}
	var ops int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'list_next_actions'`).Scan(&ops); err != nil || ops != 3 {
		t.Errorf("audit rows of the op: %d %v", ops, err)
	}
	rows, err := r.Store.R.Query(`SELECT * FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
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
		if strings.Contains(fmt.Sprint(vals...), text) {
			t.Errorf("an audit row holds the next-action text %q", text)
		}
	}
}
