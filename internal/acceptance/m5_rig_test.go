package acceptance_test

import (
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m5Loop is a generation rig over the synthetic acceptance archive (fake backend, real ingest, real ffmpeg) with one agent on the
// MCP face (read + write + generate) and the operator on REST: the cast of every M5 close-the-loop test. The mother is a known-recipe
// lossless take whose lyrics are "[Verse 1]" and "[Chorus]" with one line each, the shape the importer gives real recipes.
type m5Loop struct {
	*servertest.Rig
	h      *servertest.Client
	agent  *mcp.ClientSession
	mother string
	n      int // idempotency key counter
}

func newM5Loop(t *testing.T, o servertest.Options) *m5Loop {
	t.Helper()
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), o)
	return &m5Loop{Rig: r, h: r.Human(t), agent: r.MCP(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate), mother: knownMother(t, r)}
}

func (l *m5Loop) key(prefix string) string {
	l.n++
	return fmt.Sprintf("%s-%d", prefix, l.n)
}

// motherLyrics is the recipe lyrics of a track.
func (l *m5Loop) lyricsOf(t *testing.T, track string) string {
	t.Helper()
	return obj(obj(l.h.OK(t, "GET", "/api/tracks/"+track, nil))["recipe"])["lyrics"].(string)
}

// agentComment adds a comment as the agent (MCP tool add_comment).
func (l *m5Loop) agentComment(t *testing.T, track, kind, body string, at float64) string {
	t.Helper()
	args := map[string]any{"id": track, "kind": kind, "body": body}
	if at >= 0 {
		args["at_second"] = at
	}
	return obj(callTool(t, l.agent, "add_comment", args, false))["id"].(string)
}

// humanComment adds a comment as the operator (REST).
func (l *m5Loop) humanComment(t *testing.T, track, kind, body string, at float64) string {
	t.Helper()
	b := map[string]any{"body": body, "kind": kind}
	if at >= 0 {
		b["at_second"] = at
	}
	return obj(l.h.OK(t, "POST", "/api/tracks/"+track+"/comments", b, "Idempotency-Key", l.key("hc")))["id"].(string)
}

// propose is propose_patch on the agent's face: the chorus line of the mother replaced, the chorus named as the scope.
func (l *m5Loop) proposeChorus(t *testing.T, comments []string, text string) map[string]any {
	t.Helper()
	return obj(callTool(t, l.agent, "propose_patch", map[string]any{"base_track_id": l.mother, "addresses_comment_ids": comments, "rationale": "the hook lands late",
		"extra_sections": []int{1}, "ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": text}}}, true))
}

// renderAgent estimates and creates a song_pr item as the agent over MCP; it returns the estimate and, when the create answers, the
// batch (nil otherwise: the create result is returned as tool result for refusals).
func (l *m5Loop) estimateAgent(t *testing.T, patch string, n int) *mcp.CallToolResult {
	t.Helper()
	return rawTool(t, l.agent, "estimate_generation", map[string]any{"items": []any{map[string]any{"mode": "song_pr", "patch_id": patch, "n_seeds": n}}}, false)
}

func (l *m5Loop) createAgent(t *testing.T, est map[string]any) *mcp.CallToolResult {
	t.Helper()
	return rawTool(t, l.agent, "create_generation", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "close the loop"}, false)
}

// renderAgentOK runs a whole agent render and pumps the jobs to their end; it returns the children.
func (l *m5Loop) renderAgentOK(t *testing.T, patch string, n int) (batch string, children []string) {
	t.Helper()
	res := l.estimateAgent(t, patch, n)
	if res.IsError {
		t.Fatalf("estimate: %s", errText(res))
	}
	est := obj(decodeStructured(t, res))
	if est["requires_human_approval"] != false {
		t.Fatalf("the agent's estimate needs the operator: %v", est["approval_reasons"])
	}
	cres := l.createAgent(t, est)
	if cres.IsError {
		t.Fatalf("create: %s", errText(cres))
	}
	b := obj(decodeStructured(t, cres))
	return b["id"].(string), l.finish(t, b)
}

// renderHuman runs a render as the operator through the confirm flow.
func (l *m5Loop) renderHuman(t *testing.T, patch string, n int) (batch string, children []string) {
	t.Helper()
	est := obj(l.h.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "song_pr", "patch_id": patch, "n_seeds": n}}}))
	resp := l.h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "close the loop"}, "Idempotency-Key", l.key("hg"))
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	b := obj(resp.JSON(t))
	return b["id"].(string), l.finish(t, b)
}

// finish pumps the jobs of a batch to their end and returns the child tracks (in job order).
func (l *m5Loop) finish(t *testing.T, batch map[string]any) []string {
	t.Helper()
	var jobIDs []string
	for _, j := range arr(batch["jobs"]) {
		jobIDs = append(jobIDs, obj(j)["id"].(string))
	}
	pump(t, l.Rig, func() bool {
		for _, id := range jobIDs {
			if s, _ := jobState(t, l.Rig, id); !terminalJob(s) {
				return false
			}
		}
		return true
	})
	var children []string
	for _, id := range jobIDs {
		if s, code := jobState(t, l.Rig, id); s != "completed" {
			t.Fatalf("job %s ended %s (%s)", id, s, code)
		}
		children = append(children, obj(l.h.OK(t, "GET", "/api/jobs/"+id, nil))["track_id"].(string))
	}
	return children
}

// callbacksOf lists the callbacks of a child (as the operator), pending first.
func (l *m5Loop) callbacksOf(t *testing.T, child string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, it := range arr(obj(l.h.OK(t, "GET", "/api/callbacks?child_track_id="+child+"&limit=100", nil))["items"]) {
		out = append(out, obj(it))
	}
	return out
}

func callbackOf(t *testing.T, cbs []map[string]any, kind string) map[string]any {
	t.Helper()
	for _, c := range cbs {
		if c["kind"] == kind {
			return c
		}
	}
	t.Fatalf("no %s callback in %d callbacks", kind, len(cbs))
	return nil
}

// commentState reads a comment's state straight from the database.
func (l *m5Loop) commentState(t *testing.T, id string) (state, by string) {
	t.Helper()
	var by2 *string
	if err := l.Store.R.QueryRow(`SELECT state, state_changed_by FROM comments WHERE id = ?`, id).Scan(&state, &by2); err != nil {
		t.Fatal(err)
	}
	if by2 != nil {
		by = *by2
	}
	return state, by
}

func (l *m5Loop) verdict(t *testing.T, c *servertest.Client, cb, verdict string, extra map[string]any) *servertest.Response {
	t.Helper()
	body := map[string]any{"verdict": verdict}
	for k, v := range extra {
		body[k] = v
	}
	return c.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", body)
}

// nil2agent returns a REST client for the agent identity of the MCP session (a second token of the same cast: critic verdicts are
// by source, so any agent PAT with write scope is the critic).
func nil2agent(t *testing.T, l *m5Loop) *servertest.Client {
	t.Helper()
	c, _ := l.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	return c
}
