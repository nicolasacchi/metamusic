package mcpgen_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	parityPatch    = "01900000-0000-7000-8000-00000000cc01"
	parityCallback = "01900000-0000-7000-8000-00000000cc03"
)

// seedPopulatedM5 gives a parity rig one Song PR child with a callback: a patch on Luna addressing Luna's chorus fix, a job that
// rendered it, the child (Twin B) and one fix callback, straight through SQL with fixed ids so every face sees the same rows.
func seedPopulatedM5(t *testing.T, r *servertest.Rig) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := r.Store.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := lt.FixedNow
	seed := lt.SeedJob(t, r.Store, now, lt.JobSeed{Name: "parity-m5", Principal: "agent:parity"})
	job := "01900000-0000-7000-8000-00000000cc02"
	exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, parityPatch, lt.TrackLuna, "h", "lyric_edit", "[]", "{}", "{}", "{}", "parity", "agent", "agent:parity", "parity", store.FormatTime(now))
	exec(`INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, parityPatch, lt.CommentLunaFix)
	exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 5, kind, engine, backend, recipe_id, source_track_id, 'lyric_edit', mutation_json, 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, job, parityPatch, seed.JobID)
	exec(`UPDATE comments SET state = 'addressed', state_changed_at = ?, state_changed_by = 'system:job' WHERE id = ?`, store.FormatTime(now), lt.CommentLunaFix)
	exec(`INSERT INTO callbacks(id, child_track_id, comment_id, source_track_id, kind, patch_id, job_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		parityCallback, lt.TrackTwinB, lt.CommentLunaFix, lt.TrackLuna, "fix", parityPatch, job, store.FormatTime(now))
}

// TestOps_M5_ParityMCP: the M5 operations answer the same on REST, MCP and stdio over a library that holds a Song PR with a callback
// (the shared parity script runs over an empty callback list and a 404; this is the populated half): the pending list, the callback
// and the patch with its tallies, the agent's critic verdict (201), its replay (200), a conflicting repeat (409), a keep verdict on
// a fix callback (422), and the reads after the verdict. None answers 501 and none is a dedicated tool.
func TestOps_M5_ParityMCP(t *testing.T) {
	scopes := []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	opts := parityRigOptions()
	ra, rb, rc := servertest.New(t, opts), servertest.New(t, opts), servertest.New(t, opts)
	for _, r := range []*servertest.Rig{ra, rb, rc} {
		r.Settle(t)
		seedPopulatedM5(t, r)
	}
	rest, _ := ra.Agent(t, scopes...)
	faces := map[string]*mcp.ClientSession{"mcp": rb.MCP(t, scopes...), "stdio": rc.Stdio(t, scopes...)}
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(ra.API) {
		ops[op.ID] = op
	}
	masks := []string{"id", "author", "created_at", "verdict_id"}
	steps := []struct {
		op     string
		args   map[string]any
		status int // 0 => 200
	}{
		{"list_callbacks", map[string]any{"state": []string{"pending"}, "limit": 10}, 0},
		{"list_callbacks", map[string]any{"kind": []string{"keep"}}, 0},
		{"get_callback", map[string]any{"id": parityCallback}, 0},
		{"get_patch", map[string]any{"id": parityPatch}, 0},
		{"list_patches", map[string]any{"track_id": lt.TrackLuna}, 0},
		{"submit_callback_verdict", map[string]any{"id": parityCallback, "verdict": "kept"}, 422},
		{"submit_callback_verdict", map[string]any{"id": parityCallback, "verdict": "not_yet", "note": "still late", "heard_at_s": 5.5}, 201},
		{"submit_callback_verdict", map[string]any{"id": parityCallback, "verdict": "not_yet", "note": "still late", "heard_at_s": 5.5}, 200},
		{"submit_callback_verdict", map[string]any{"id": parityCallback, "verdict": "fixed"}, 409},
		{"get_callback", map[string]any{"id": parityCallback}, 0},
		{"list_callbacks", map[string]any{"state": []string{"pending", "answered"}}, 0},
		{"get_patch", map[string]any{"id": parityPatch}, 0},
	}
	for i, s := range steps {
		op := ops[s.op]
		path, body, headers := restRequest(t, ra.API, op, s.args)
		if op.Meta.Idempotency == "create" {
			headers = append(headers, "Idempotency-Key", "parity-m5")
		}
		resp := rest.Do(t, op.Method, path, body, headers...)
		wantStatus := s.status
		if wantStatus == 0 {
			wantStatus = 200
		}
		if resp.Status != wantStatus {
			t.Fatalf("step %d %s: REST %d, want %d: %s", i, s.op, resp.Status, wantStatus, resp.Body)
		}
		want := servertest.Normalize(resp.JSON(t), masks...)
		for name, sess := range faces {
			res, tool := mcpRaw(t, sess, op, s.args, false)
			if tool != "call_operation" {
				t.Errorf("%s answered as the dedicated tool %s", s.op, tool)
			}
			failure := s.status >= 400
			if got := normalizeResult(t, res, op, tool, masks, failure); !cmp.Equal(want, got) {
				t.Errorf("step %d %s: REST vs %s (-rest +%s):\n%s", i, s.op, name, name, cmp.Diff(want, got))
			}
		}
	}
}
