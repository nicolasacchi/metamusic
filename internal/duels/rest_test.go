package duels_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// spy counts the calls that reach the renderer a rig's duels use.
type spy struct {
	duels.ClipRenderer
	measures, ensures atomic.Int64
}

func (s *spy) Measure(ctx context.Context, sp clip.Spec) (clip.Measure, error) {
	s.measures.Add(1)
	return s.ClipRenderer.Measure(ctx, sp)
}

func (s *spy) Ensure(ctx context.Context, sp clip.Spec) (clip.Object, error) {
	s.ensures.Add(1)
	return s.ClipRenderer.Ensure(ctx, sp)
}

func rig(t *testing.T, s *spy) *servertest.Rig {
	t.Helper()
	m := servertest.M2bOptions{Fixture: true, FakeClips: true}
	if s != nil {
		m.WrapClips = func(in duels.ClipRenderer) duels.ClipRenderer { s.ClipRenderer = in; return s }
	}
	return servertest.New(t, servertest.Options{Fixture: true, M2b: m})
}

var rw = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func rowCount(t *testing.T, r *servertest.Rig, q string, a ...any) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(q, a...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func callOp(t *testing.T, s *mcp.ClientSession, op string, args map[string]any, generic bool) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	name, params := op, any(args)
	if generic {
		name, params = "call_operation", map[string]any{"operation_id": op, "arguments": args}
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: params})
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Logf("%s error: %s", op, tc.Text)
			}
		}
	}
	b, _ := json.Marshal(res.StructuredContent)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	return v, res
}

func createBody(extra map[string]any) map[string]any {
	b := map[string]any{"a_track_id": duelstest.TrackAlpha, "b_track_id": duelstest.TrackBeta}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestCreateDuel_ExplicitFalseHonoured(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, r *servertest.Rig, s *spy, body map[string]any, id string, wantBlind bool, wantMatch string, wantMeasures int64) {
		t.Helper()
		var swapped int
		var match string
		var blind bool
		if err := r.Store.R.QueryRow(`SELECT swapped, match, blind FROM duels WHERE id = ?`, id).Scan(&swapped, &match, &blind); err != nil {
			t.Fatal(err)
		}
		if blind != wantBlind || match != wantMatch || (!wantBlind && swapped != 0) {
			t.Errorf("%v: blind %v match %s swapped %d", body, blind, match, swapped)
		}
		if got := s.measures.Load(); got != wantMeasures {
			t.Errorf("%v: Measure calls = %d, want %d", body, got, wantMeasures)
		}
	}
	for _, via := range []string{"rest", "mcp"} {
		cases := []struct {
			name         string
			extra        map[string]any
			blind        bool
			match        string
			measures     int64
			revealedView bool
		}{
			{"blind false", map[string]any{"blind": false}, false, "matched", 2, true},
			{"match_loudness false", map[string]any{"match_loudness": false}, true, "off", 0, false},
			{"defaults", nil, true, "matched", 2, false},
		}
		for _, tc := range cases {
			t.Run(via+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				s := &spy{}
				r := rig(t, s)
				body := createBody(tc.extra)
				var out map[string]any
				if via == "rest" {
					c, _ := r.Agent(t, rw...)
					res := c.Do(t, "POST", "/api/duels", body, "Idempotency-Key", idemKey())
					if res.Status != 201 {
						t.Fatalf("status %d: %s", res.Status, res.Body)
					}
					out = asMap(res.JSON(t))
				} else {
					var res *mcp.CallToolResult
					out, res = callOp(t, r.MCP(t, rw...), "create_duel", body, true)
					if res.IsError {
						t.Fatalf("%+v", res.Content)
					}
				}
				check(t, r, s, body, out["id"].(string), tc.blind, tc.match, tc.measures)
				if got := out["revealed"] == true; got != tc.revealedView {
					t.Errorf("revealed = %v, want %v", got, tc.revealedView)
				}
				if out["blind"] != tc.blind || out["loudness_matched"] != (tc.match == "matched") {
					t.Errorf("view = %v", out)
				}
			})
		}
	}
}

func TestCreateDuel_ReplayDoesNotRender(t *testing.T) {
	t.Parallel()
	t.Run("rest", func(t *testing.T) {
		t.Parallel()
		s := &spy{}
		r := rig(t, s)
		c, _ := r.Agent(t, rw...)
		first := c.Do(t, "POST", "/api/duels", createBody(nil), "Idempotency-Key", "k1")
		second := c.Do(t, "POST", "/api/duels", createBody(nil), "Idempotency-Key", "k1")
		if first.Status != 201 || second.Status != 201 || string(first.Body) != string(second.Body) {
			t.Fatalf("first %d %s\nsecond %d %s", first.Status, first.Body, second.Status, second.Body)
		}
		if second.Header.Get("Idempotent-Replayed") != "true" || first.Header.Get("Idempotent-Replayed") != "" {
			t.Errorf("replay headers: %q / %q", first.Header.Get("Idempotent-Replayed"), second.Header.Get("Idempotent-Replayed"))
		}
		if n := rowCount(t, r, `SELECT count(*) FROM duels`); n != 5 {
			t.Errorf("duels = %d, want 4 + 1", n)
		}
		if e := s.ensures.Load(); e != 2 {
			t.Errorf("Ensure calls = %d, want one per slot", e)
		}
		other := c.Do(t, "POST", "/api/duels", createBody(nil), "Idempotency-Key", "k2")
		if other.Status != 201 || string(other.Body) == string(first.Body) || rowCount(t, r, `SELECT count(*) FROM duels`) != 6 {
			t.Errorf("a different key must create a second duel: %d %s", other.Status, other.Body)
		}
	})
	t.Run("mcp derived key", func(t *testing.T) {
		t.Parallel()
		s := &spy{}
		r := rig(t, s)
		sess := r.MCP(t, rw...)
		a, _ := callOp(t, sess, "create_duel", createBody(nil), true)
		b, _ := callOp(t, sess, "create_duel", createBody(nil), true)
		if a["id"] == nil || a["id"] != b["id"] {
			t.Errorf("ids %v / %v", a["id"], b["id"])
		}
		if n := rowCount(t, r, `SELECT count(*) FROM duels`); n != 5 {
			t.Errorf("duels = %d", n)
		}
		if e := s.ensures.Load(); e != 2 {
			t.Errorf("Ensure calls = %d", e)
		}
		c, _ := callOp(t, sess, "create_duel", createBody(map[string]any{"blind": false}), true)
		if c["id"] == a["id"] {
			t.Error("different arguments must create another duel")
		}
	})
}

func TestCreateDuel_RESTWithoutKey428(t *testing.T) {
	t.Parallel()
	s := &spy{}
	r := rig(t, s)
	c, _ := r.Agent(t, rw...)
	res := c.Do(t, "POST", "/api/duels", createBody(nil))
	if res.Status != 428 || asMap(res.JSON(t))["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	if n := rowCount(t, r, `SELECT count(*) FROM duels`); n != 4 || s.ensures.Load() != 0 || s.measures.Load() != 0 {
		t.Errorf("rows %d, renderer calls %d/%d", n, s.measures.Load(), s.ensures.Load())
	}
}

var keySeq atomic.Int64

func idemKey() string { return fmt.Sprintf("rest-test-%d", keySeq.Add(1)) }

// restCreate creates a blind duel over REST and returns its id.
func restCreate(t *testing.T, c *servertest.Client) string {
	t.Helper()
	res := c.Do(t, "POST", "/api/duels", createBody(nil), "Idempotency-Key", idemKey())
	if res.Status != 201 {
		t.Fatalf("create: %d %s", res.Status, res.Body)
	}
	return asMap(res.JSON(t))["id"].(string)
}

func restJudge(t *testing.T, c *servertest.Client, id, verdict string) *servertest.Response {
	t.Helper()
	return c.Do(t, "POST", "/api/duels/"+id+"/verdicts", map[string]any{"verdict": verdict})
}

type stored struct{ source, kind, principal string }

func storedVerdicts(t *testing.T, r *servertest.Rig, duel string) []stored {
	t.Helper()
	rows, err := r.Store.R.Query(`SELECT source, principal_kind, principal_id FROM duel_verdicts WHERE duel_id = ? ORDER BY created_at, id`, duel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []stored
	for rows.Next() {
		var x stored
		if err := rows.Scan(&x.source, &x.kind, &x.principal); err != nil {
			t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

func TestSubmitDuel_SourceByPrincipal(t *testing.T) {
	t.Parallel()
	r := rig(t, nil)
	h := r.Human(t)
	baseline := func() map[string]any { return asMap(h.OK(t, "GET", "/api/ladder", nil)) }()
	delete(asMap(baseline["counts"]), "verdicts_critic_excluded")
	one := func(duel string, want stored) {
		t.Helper()
		vs := storedVerdicts(t, r, duel)
		if len(vs) != 1 || vs[0].source != want.source || vs[0].kind != want.kind || (want.principal != "" && vs[0].principal != want.principal) {
			t.Errorf("stored = %+v, want %+v", vs, want)
		}
	}
	// proxy human
	d := restCreate(t, h)
	res := restJudge(t, h, d, "a")
	if res.Status != 201 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	one(d, stored{"human", "human", ""})
	// paired device
	dev := r.Device(t)
	d = restCreate(t, dev)
	if res = restJudge(t, dev, d, "b"); res.Status != 201 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	one(d, stored{"human", "device", ""})
	afterHuman := asMap(h.OK(t, "GET", "/api/ladder", nil))
	// PAT agent over REST, including a body that tries to pick the source
	agentC, _ := r.Agent(t, rw...)
	d = restCreate(t, agentC)
	res = agentC.Do(t, "POST", "/api/duels/"+d+"/verdicts", map[string]any{"verdict": "a", "source": "human"})
	if res.Status != 422 && res.Status != 201 {
		t.Fatalf("extra source field: %d %s", res.Status, res.Body)
	}
	if res.Status == 422 {
		if res = restJudge(t, agentC, d, "a"); res.Status != 201 {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
	}
	one(d, stored{"critic", "agent", ""})
	// PAT over MCP and mcp-stdio
	for name, sess := range map[string]*mcp.ClientSession{"mcp": r.MCP(t, rw...), "stdio": r.Stdio(t, rw...)} {
		d := restCreate(t, agentC)
		out, tr := callOp(t, sess, "submit_duel", map[string]any{"id": d, "verdict": "b"}, false)
		if tr.IsError || asMap(out["verdict"])["source"] != "critic" {
			t.Fatalf("%s: %+v %v", name, tr.Content, out)
		}
		one(d, stored{"critic", "agent", ""})
	}
	// critic verdicts left the human ladder alone
	final := asMap(h.OK(t, "GET", "/api/ladder", nil))
	delete(asMap(final["counts"]), "verdicts_critic_excluded")
	delete(asMap(afterHuman["counts"]), "verdicts_critic_excluded")
	if diff := cmp.Diff(afterHuman, final); diff != "" {
		t.Errorf("critics changed the human ladder:\n%s", diff)
	}
	_ = baseline
}

func TestSubmitDuel_NoteCreatedOnlyOn201(t *testing.T) {
	t.Parallel()
	r := rig(t, nil)
	h := r.Human(t)
	d := restCreate(t, h)
	first := restJudge(t, h, d, "a")
	second := restJudge(t, h, d, "a")
	if first.Status != 201 || second.Status != 200 || asMap(second.JSON(t))["replayed"] != true {
		t.Fatalf("first %d, second %d %s", first.Status, second.Status, second.Body)
	}
	vid := asMap(asMap(first.JSON(t))["verdict"])["id"].(string)
	rows, err := r.Store.R.Query(`SELECT status, created_ids_json FROM audit_log WHERE op = 'submit_duel' ORDER BY at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got [][2]string
	for rows.Next() {
		var st int
		var ids string
		if err := rows.Scan(&st, &ids); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{fmt.Sprint(st), ids})
	}
	want := [][2]string{{"201", `["` + vid + `"]`}, {"200", `[]`}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("audit rows (-want +got):\n%s", diff)
	}
}

func TestAcceptance_M2b_CriticVerdictsExcluded(t *testing.T) {
	t.Parallel()
	r := rig(t, nil)
	h, dev := r.Human(t), r.Device(t)
	tracks := []string{duelstest.TrackAlpha, duelstest.TrackBeta, lt.TrackTwinA, lt.TrackTwinB}
	create := func(c *servertest.Client, a, b string) string {
		res := c.Do(t, "POST", "/api/duels", map[string]any{"a_track_id": a, "b_track_id": b}, "Idempotency-Key", idemKey())
		if res.Status != 201 {
			t.Fatalf("create: %d %s", res.Status, res.Body)
		}
		return asMap(res.JSON(t))["id"].(string)
	}
	type judged struct{ id, verdict string }
	var all []judged
	n := 0
	for i := range tracks {
		for j := i + 1; j < len(tracks); j++ {
			id := create(h, tracks[i], tracks[j])
			v := []string{"a", "b"}[(i+2*j+n)%2]
			n++
			if res := restJudge(t, h, id, v); res.Status != 201 {
				t.Fatalf("%d %s", res.Status, res.Body)
			}
			all = append(all, judged{id, v})
		}
	}
	for _, p := range [][2]string{{tracks[0], tracks[2]}, {tracks[3], tracks[1]}} {
		id := create(dev, p[0], p[1])
		if res := restJudge(t, dev, id, "a"); res.Status != 201 {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
		all = append(all, judged{id, "a"})
	}
	if len(all) != 8 {
		t.Fatal(len(all))
	}
	ladder := func(q string) map[string]any { return asMap(h.OK(t, "GET", "/api/ladder"+q, nil)) }
	lq := "?scope=lineage&root_track_id=" + duelstest.TrackAlpha
	globalBefore, lineageBefore := ladder(""), ladder(lq)
	// the fixture already holds 1 counted human verdict (and 1 critic verdict)
	if c := asMap(globalBefore["counts"]); c["verdicts_human_counted"] != 9.0 || c["verdicts_critic_excluded"] != 1.0 {
		t.Fatalf("counts = %v", c)
	}
	contra := func(v string) string { return map[string]string{"a": "b", "b": "a"}[v] }
	pat, _ := r.Agent(t, rw...)
	mcpS, stdioS := r.MCP(t, rw...), r.Stdio(t, rw...)
	for i, d := range all {
		var res *servertest.Response
		switch {
		case i < 3:
			res = restJudge(t, pat, d.id, contra(d.verdict))
			if res.Status != 201 {
				t.Fatalf("pat: %d %s", res.Status, res.Body)
			}
		default:
			sess := mcpS
			if i >= 6 {
				sess = stdioS
			}
			out, tr := callOp(t, sess, "submit_duel", map[string]any{"id": d.id, "verdict": contra(d.verdict)}, false)
			if tr.IsError || asMap(out["verdict"])["source"] != "critic" {
				t.Fatalf("session %d: %+v", i, tr.Content)
			}
		}
	}
	for i := range 4 {
		id := create(pat, tracks[i%4], tracks[(i+1)%4])
		if res := restJudge(t, pat, id, "a"); res.Status != 201 {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
	}
	globalAfter, lineageAfter := ladder(""), ladder(lq)
	if c := asMap(globalAfter["counts"]); c["verdicts_critic_excluded"] != 13.0 {
		t.Errorf("critic excluded = %v, want 1 fixture + 12", c["verdicts_critic_excluded"])
	}
	for _, p := range [][2]map[string]any{{globalBefore, globalAfter}, {lineageBefore, lineageAfter}} {
		delete(asMap(p[0]["counts"]), "verdicts_critic_excluded")
		delete(asMap(p[1]["counts"]), "verdicts_critic_excluded")
		if diff := cmp.Diff(p[0], p[1]); diff != "" {
			t.Errorf("critic verdicts changed the ladder (-before +after):\n%s", diff)
		}
	}
	if got := rowCount(t, r, `SELECT count(*) FROM duel_verdicts WHERE source = 'critic' AND principal_kind = 'agent'`); got != 13 {
		t.Errorf("critic rows = %d", got)
	}
	// one more human verdict changes the ladder
	id := create(h, tracks[0], tracks[3])
	restJudge(t, h, id, "a")
	if cmp.Equal(globalAfter["items"], ladder("")["items"]) {
		t.Error("a human verdict must change the ladder")
	}
}
