package servertest_test

import (
	"context"
	"github.com/nicolasacchi/metamusic/internal/store"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

type auditRow struct{ ID, At, Op, Transport string }

func auditRows(t *testing.T, r *servertest.Rig) []auditRow {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT id, at, op, transport FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.ID, &a.At, &a.Op, &a.Transport); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestServertest_DeterministicRigs(t *testing.T) {
	t.Parallel()
	run := func() ([]auditRow, any) {
		r := servertest.New(t, servertest.Options{Fixture: true})
		c, _ := r.Agent(t, auth.ScopeRead)
		c.OK(t, "GET", "/api/ping", nil)
		c.OK(t, "GET", "/api/me", nil)
		sys := c.OK(t, "GET", "/api/system", nil)
		r.Clock.Advance(90 * time.Second)
		return auditRows(t, r), sys
	}
	a, sysA := run()
	b, sysB := run()
	if len(a) != 3 {
		t.Fatalf("audit rows %+v", a)
	}
	if d := cmp.Diff(a, b); d != "" {
		t.Errorf("rigs differ in ids or timestamps (-a +b):\n%s", d)
	}
	if d := cmp.Diff(sysA, sysB); d != "" {
		t.Errorf("system info differs:\n%s", d)
	}
	if a[0].At != "2026-10-06T12:00:00Z" {
		t.Errorf("rows must carry the fixed rig clock, got %s", a[0].At)
	}
}

func TestServertest_ClockAdvanceMovesEveryStore(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	c, _ := r.Agent(t, auth.ScopeRead)
	before := c.OK(t, "GET", "/api/system", nil).(map[string]any)["uptime_s"].(float64)
	r.Clock.Advance(90 * time.Second)
	after := c.OK(t, "GET", "/api/system", nil).(map[string]any)["uptime_s"].(float64)
	if after-before != 90 {
		t.Errorf("uptime moved by %v, want 90", after-before)
	}
	c.OK(t, "GET", "/api/ping", nil)
	rows := auditRows(t, r)
	if got := rows[len(rows)-1].At; got != "2026-10-06T12:01:30Z" {
		t.Errorf("audit clock = %s", got)
	}
	// Token bookkeeping reads the same clock.
	infos, err := r.Services.Tokens.List(context.Background())
	if err != nil || len(infos) != 1 {
		t.Fatalf("tokens: %v %v", infos, err)
	}
}

func TestServertest_Faces(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	h := r.Human(t)
	if who := h.OK(t, "GET", "/api/me", nil).(map[string]any); who["kind"] != "human" {
		t.Errorf("human: %v", who)
	}
	m := r.MCP(t, auth.ScopeRead)
	res, err := m.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("mcp whoami: %v %+v", err, res)
	}
	st := r.Stdio(t, auth.ScopeRead)
	res, err = st.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("stdio whoami: %v %+v", err, res)
	}
}

func TestServertest_LocalHuman(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	if who := h.OK(t, "GET", "/api/me", nil).(map[string]any); who["kind"] != "human" || who["via"] != "local" {
		t.Errorf("local human: %v", who)
	}
}

func TestServertest_ClientRecordsResponses(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	c, _ := r.Agent(t, auth.ScopeRead)
	c.OK(t, "GET", "/api/ping", nil)
	if resp := c.Do(t, "GET", "/api/nope", nil); resp.Status != 404 {
		t.Errorf("status %d", resp.Status)
	}
	got := c.Responses()
	if len(got) != 2 || got[0].Path != "/api/ping" || got[1].Status != 404 || len(got[0].Body) == 0 {
		t.Errorf("responses %+v", got)
	}
}

func TestServertest_Canary(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Canary: true})
	if r.Ntfy == nil || !strings.HasPrefix(r.Ntfy.URL, "http://") {
		t.Fatalf("canary rig needs a fake ntfy server: %+v", r.Ntfy)
	}
	if who := r.Human(t).OK(t, "GET", "/api/me", nil).(map[string]any); who["name"] != "canary-human" {
		t.Errorf("canary human: %v", who)
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	in := map[string]any{"id": "x", "n": []any{map[string]any{"client": "c", "k": 1.0}}}
	want := map[string]any{"id": "<masked>", "n": []any{map[string]any{"client": "<masked>", "k": 1.0}}}
	if d := cmp.Diff(want, servertest.Normalize(in, "id", "client")); d != "" {
		t.Error(d)
	}
	if in["id"] != "x" {
		t.Error("Normalize must not modify its input")
	}
}

// TestServertest_ArchiveOption: a rig over an imported synthetic archive lists its tracks
// and drafts, and the archive's template stays untouched by writes through the rig.
func TestServertest_ArchiveOption(t *testing.T) {
	t.Parallel()
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	r := servertest.New(t, servertest.Options{Archive: &tpl})
	h := r.Human(t)
	tracks := h.OK(t, "GET", "/api/tracks?limit=100", nil).(map[string]any)
	drafts := h.OK(t, "GET", "/api/drafts?limit=100", nil).(map[string]any)
	if tracks["total"].(float64) < 1 || drafts["total"].(float64) < 1 {
		t.Fatalf("empty archive rig: tracks=%v drafts=%v", tracks["total"], drafts["total"])
	}
	id := tracks["items"].([]any)[0].(map[string]any)["id"].(string)
	h.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 3})
	r2 := servertest.New(t, servertest.Options{Archive: &tpl})
	var n int
	if err := r2.Store.R.QueryRow(`SELECT COUNT(*) FROM ratings`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a second rig sees %d ratings (%v)", n, err)
	}
}

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	code := m.Run()
	archivetest.Cleanup()
	os.Exit(code)
}
