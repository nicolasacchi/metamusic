package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// seedCallback writes the rows a Song PR child brings, straight through SQL (the take hook is covered in internal/callbacks): a
// patch on base addressing a fix comment of base, a job that rendered it and one fix callback on child. It returns the callback and
// comment ids.
func seedCallback(t *testing.T, r *servertest.Rig, base, child string) (callback, comment string) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := r.Store.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	h := r.Human(t)
	comment = obj(t, h.OK(t, "POST", "/api/tracks/"+base+"/comments", map[string]any{"body": "the hook is late", "kind": "fix", "at_second": 6.0}, "Idempotency-Key", "k-cb-1"))["id"].(string)
	now := lt.FixedNow
	seed := lt.SeedJob(t, r.Store, now, lt.JobSeed{Name: "apicb", Principal: "agent:apicb"})
	patch, job := "01900000-0000-7000-8000-00000000cb01", "01900000-0000-7000-8000-00000000cb02"
	callback = "01900000-0000-7000-8000-00000000cb03"
	exec(`INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, rationale, principal_kind, principal_id, principal_name, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, patch, base, "h", "lyric_edit", "[]", "{}", "{}", "{}", "stub", "agent", "agent:apicb", "apicb", store.FormatTime(now))
	exec(`INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, patch, comment)
	exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,source_track_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,
			expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at,patch_id)
		SELECT ?, batch_id, 5, kind, engine, backend, recipe_id, source_track_id, 'lyric_edit', mutation_json, 'T', expected_audio_s, expected_s, timeout_s, expected_micro_usd,
		       reserved_micro_usd, 'queued', next_attempt_at, queued_at, created_at, updated_at, ? FROM jobs WHERE id = ?`, job, patch, seed.JobID)
	exec(`UPDATE comments SET state = 'addressed', state_changed_at = ?, state_changed_by = 'system:job' WHERE id = ?`, store.FormatTime(now), comment)
	exec(`INSERT INTO callbacks(id, child_track_id, comment_id, source_track_id, kind, patch_id, job_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		callback, child, comment, base, "fix", patch, job, store.FormatTime(now))
	return callback, comment
}

// TestCallbacks_REST walks the three operations over HTTP: list and get as agent and human, a critic verdict that moves nothing,
// the human verdict that resolves the comment (201), its replay (200), the conflicts and the refusals.
func TestCallbacks_REST(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true})
	cb, comment := seedCallback(t, r, lt.TrackLuna, lt.TrackTwinB)
	h := r.Human(t)
	reader, _ := r.Agent(t, auth.ScopeRead)
	writer, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)

	pg := obj(t, reader.OK(t, "GET", "/api/callbacks?state=pending&kind=fix&child_track_id="+lt.TrackTwinB, nil))
	items := pg["items"].([]any)
	if pg["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("list: %v", pg)
	}
	c := obj(t, items[0])
	if c["id"] != cb || c["state"] != "pending" || c["relation"] != "self" || obj(t, c["comment"])["state"] != "addressed" || obj(t, c["child"])["id"] != lt.TrackTwinB ||
		c["my_verdict"] != nil || c["human_verdict"] != nil || len(c["critic_verdicts"].([]any)) != 0 {
		t.Errorf("callback view: %v", c)
	}
	if got := obj(t, reader.OK(t, "GET", "/api/callbacks/"+cb, nil)); got["id"] != cb {
		t.Errorf("get: %v", got)
	}
	if resp := reader.Do(t, "GET", "/api/callbacks/nope", nil); resp.Status != 404 {
		t.Errorf("unknown: %d %s", resp.Status, resp.Body)
	}
	if resp := reader.Do(t, "GET", "/api/callbacks?limit=0", nil); resp.Status != 422 {
		t.Errorf("limit 0: %d", resp.Status)
	}
	if resp := reader.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "fixed"}); resp.Status != 403 {
		t.Errorf("read-only agent submit: %d", resp.Status)
	}
	if resp := writer.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "kept"}); resp.Status != 422 {
		t.Errorf("keep verdict on a fix callback: %d %s", resp.Status, resp.Body)
	}

	// the agent's critic verdict: 201, no state change
	resp := writer.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "worse", "note": "still late"})
	if resp.Status != 201 {
		t.Fatalf("critic verdict: %d %s", resp.Status, resp.Body)
	}
	res := obj(t, resp.JSON(t))
	if res["replayed"] != false || obj(t, res["verdict"])["source"] != "critic" || res["comment_state"] != nil {
		t.Errorf("critic result: %v", res)
	}
	if got := obj(t, h.OK(t, "GET", "/api/callbacks/"+cb, nil)); got["state"] != "pending" || len(got["critic_verdicts"].([]any)) != 0 {
		t.Errorf("the human sees a critic before answering: %v", got)
	}
	if resp := writer.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "worse", "note": "still late"}); resp.Status != 200 || obj(t, resp.JSON(t))["replayed"] != true {
		t.Errorf("critic replay: %d %s", resp.Status, resp.Body)
	}
	if resp := writer.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "fixed"}); resp.Status != 409 || obj(t, resp.JSON(t))["code"] != "CALLBACK_ALREADY_JUDGED" {
		t.Errorf("critic conflict: %d %s", resp.Status, resp.Body)
	}

	// the human verdict resolves the comment
	resp = h.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "fixed", "heard_at_s": 29.5})
	if resp.Status != 201 {
		t.Fatalf("human verdict: %d %s", resp.Status, resp.Body)
	}
	res = obj(t, resp.JSON(t))
	if res["comment_state"] != "resolved" || obj(t, res["verdict"])["state_before"] != "addressed" || obj(t, res["verdict"])["state_after"] != "resolved" {
		t.Errorf("human result: %v", res)
	}
	if got := cbStr(t, r, `SELECT state FROM comments WHERE id = ?`, comment); got != "resolved" {
		t.Errorf("comment state %s", got)
	}
	if resp := h.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "fixed", "heard_at_s": 29.5}); resp.Status != 200 || obj(t, resp.JSON(t))["replayed"] != true {
		t.Errorf("human replay: %d %s", resp.Status, resp.Body)
	}
	if resp := h.Do(t, "POST", "/api/callbacks/"+cb+"/verdicts", map[string]any{"verdict": "worse"}); resp.Status != 409 {
		t.Errorf("human conflict: %d", resp.Status)
	}
	got := obj(t, h.OK(t, "GET", "/api/callbacks/"+cb, nil))
	if got["state"] != "answered" || len(got["critic_verdicts"].([]any)) != 1 || obj(t, got["my_verdict"])["verdict"] != "fixed" {
		t.Errorf("after answering: %v", got)
	}
	// and an agent reads the comment through list_comments with the new state
	cs := obj(t, reader.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/comments", nil))["items"].([]any)
	for _, x := range cs {
		if m := obj(t, x); m["id"] == comment && m["state"] != "resolved" {
			t.Errorf("list_comments shows %v", m["state"])
		}
	}
}

func cbStr(t *testing.T, r *servertest.Rig, q string, args ...any) string {
	t.Helper()
	var s string
	if err := r.Store.R.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}
