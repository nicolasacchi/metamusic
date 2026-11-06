package acceptance_test

import (
	"testing"
	"time"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Lane M5-C1 owns this file: the scripted calls of the callback operations. A callback is born in the take transaction of a Song PR
// child, which the scripted session does not render, so seedM5Callback (called by the loop of every_ui_action_test.go before each
// scripted call; it acts only before list_callbacks) writes one stub patch, job and callback straight through SQL.
var m5CallbackID string

func seedM5Callback(t *testing.T, r *servertest.Rig, st *uiState, op string) {
	t.Helper()
	if op != "list_callbacks" {
		return
	}
	now := time.Now().UTC()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := r.Store.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	seed := lt.SeedJob(t, r.Store, now, lt.JobSeed{Name: "m5cb-script", Principal: "agent:script"})
	patch, job, cb := "01900000-0000-7000-8000-00000000ca01", "01900000-0000-7000-8000-00000000ca02", "01900000-0000-7000-8000-00000000ca03"
	exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, patch, st.mother, "h", "lyric_edit", "[]", "{}", "{}", "{}", "scripted stub", "agent", "agent:script", "script", store.FormatTime(now))
	exec(`INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, patch, st.comment)
	exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 5, kind, engine, backend, recipe_id, source_track_id, 'lyric_edit', mutation_json, 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, job, patch, seed.JobID)
	exec(`INSERT INTO callbacks(id, child_track_id, comment_id, source_track_id, kind, patch_id, job_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		cb, st.timed, st.comment, st.track, "fix", patch, job, store.FormatTime(now))
	m5CallbackID = cb
}

func init() {
	uiScript["list_callbacks"] = func(*uiState) map[string]any { return map[string]any{"state": []string{"pending"}, "limit": 10} }
	uiScript["get_callback"] = func(*uiState) map[string]any { return map[string]any{"id": m5CallbackID} }
	uiScript["submit_callback_verdict"] = func(*uiState) map[string]any {
		return map[string]any{"id": m5CallbackID, "verdict": "fixed"}
	}
	uiOrder = append(uiOrder, "list_callbacks", "get_callback", "submit_callback_verdict")
}
