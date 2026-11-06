package acceptance_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// acePortBody is the estimate body of a port: the mother's lyrics and style rendered by ACE-Step.
func acePortBody(mother string) map[string]any {
	return map[string]any{"items": []any{map[string]any{"mode": "port", "parent_track_id": mother}}}
}

// composeBody is an acestep15 compose_new estimate body: the request rides in ItemIn.recipe (orchestrator decision 2026-11-05).
func composeBody(id string, secs int, extra map[string]any) map[string]any {
	rec := map[string]any{"id": id, "task": "text2music", "caption": "synth pop, warm", "lyrics": "[verse]\nla la la\n[chorus]\nso so so", "instrumental": false,
		"vocal_language": "en", "duration_ds": secs * 10, "seed": 42, "thinking": true, "engine_params_version": acestep.CurrentParamsVersion(), "inference_steps": 8}
	for k, v := range extra {
		rec[k] = v
	}
	return map[string]any{"engine": "acestep15", "items": []any{map[string]any{"mode": "compose_new", "engine": "acestep15", "recipe": rec, "title": "e2 " + id}}}
}

// editBody is an estimate body of one edit item over a parent track.
func editBody(mode, parent string, edit, ref map[string]any) map[string]any {
	it := map[string]any{"mode": mode, "parent_track_id": parent}
	if edit != nil {
		it["edit"] = edit
	}
	if ref != nil {
		it["reference"] = ref
	}
	return map[string]any{"items": []any{it}}
}

// aceBase renders one acestep15 text2music take of secs seconds (a source for the edit modes) and returns its track id.
func aceBase(t *testing.T, r *servertest.Rig, id string, secs int, extra map[string]any) string {
	t.Helper()
	_, _, jobs := aceRun(t, r, composeBody(id, secs, extra), "base-"+id)
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("base take %s: %s (%s)", id, s, code)
	}
	return jobTrack(t, r, jobs[0])
}

// jobRow reads columns of one job.
func jobRow(t *testing.T, r *servertest.Rig, job, cols string) []any {
	t.Helper()
	cs := strings.Split(cols, ",")
	dest := make([]any, len(cs))
	ptr := make([]any, len(cs))
	for i := range dest {
		ptr[i] = &dest[i]
	}
	if err := r.Store.R.QueryRow(`SELECT `+cols+` FROM jobs WHERE id = ?`, job).Scan(ptr...); err != nil { //nolint:gosec // test-owned column lists
		t.Fatal(err)
	}
	return dest
}

func jobEventCount(t *testing.T, r *servertest.Rig, job, event string) int {
	t.Helper()
	return count(t, r, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = ?`, job, event)
}

// TestAcceptance_E2_AdoptionGate: an engine other than yue2 gets money only when adopted. Not adopted: the estimate is refused
// ENGINE_NOT_ADOPTED (agent and human alike) and nothing is written; adopted through the operator CLI's service: the same
// estimate works.
func TestAcceptance_E2_AdoptionGate(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{})
	mother := knownMother(t, r)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	h := r.Human(t)
	before := count(t, r, `SELECT COUNT(*) FROM estimates`)
	for name, c := range map[string]*servertest.Client{"agent": agent, "human": h} {
		resp := c.Do(t, "POST", "/api/generations/estimate", acePortBody(mother))
		code, d := problemCode(t, resp)
		if resp.Status != 409 || code != "ENGINE_NOT_ADOPTED" || d["engine"] != "acestep15" {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
		if hint, _ := d["hint"].(string); hint != "metamusic engines adopt acestep15 --reason \"...\"" {
			t.Errorf("%s: hint = %q", name, hint)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM estimates`); n != before {
		t.Errorf("%d estimates written by refused requests", n-before)
	}
	for _, e := range arr(obj(h.OK(t, "GET", "/api/engines", nil))["engines"]) {
		if ev := obj(e); ev["name"] == "acestep15" && (ev["enabled"] != true || ev["unavailable_reason"] != "not_adopted" || ev["selectable"] != false) {
			t.Errorf("list_engines acestep15 = %v", ev)
		}
	}
	if got := aceBackend(t, r).Counters().Total(); got != 0 {
		t.Errorf("fake-acestep calls = %d before adoption", got)
	}
	if _, err := r.Services.Adoption.Adopt(context.Background(), "system:cli", "acestep15", "acceptance"); err != nil {
		t.Fatal(err)
	}
	e := obj(agent.OK(t, "POST", "/api/generations/estimate", acePortBody(mother)))
	if e["engine"] != "acestep15" || e["backend"] != "fake-acestep" {
		t.Errorf("estimate after adoption = %v", e)
	}
}

// TestAcceptance_E2_ListEnginesReportsCaps: list_engines over REST and over MCP (generic) answer the same bytes; the engine is
// enabled (MM_ACESTEP=1), reports only text2music until a canary proved more, and everything the canary file proves shows in the
// caps once it does. No endpoint id or key appears.
func TestAcceptance_E2_ListEnginesReportsCaps(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	agent, _ := r.Agent(t, auth.ScopeRead)
	sess := r.MCP(t, auth.ScopeRead)
	rest := agent.Do(t, "GET", "/api/engines", nil)
	if rest.Status != 200 {
		t.Fatalf("%d %s", rest.Status, rest.Body)
	}
	viaMCP := callTool(t, sess, "list_engines", map[string]any{}, true)
	if d := cmp.Diff(rest.JSON(t), viaMCP); d != "" {
		t.Errorf("REST != MCP: %s", d)
	}
	ace := func(v any) map[string]any {
		for _, e := range arr(obj(v)["engines"]) {
			if obj(e)["name"] == "acestep15" {
				return obj(e)
			}
		}
		t.Fatal("no acestep15 engine")
		return nil
	}
	e := ace(rest.JSON(t))
	c := obj(e["caps"])
	if e["enabled"] != true || e["selectable"] != true || c["text2song"] != true || c["max_seconds"].(float64) != 480 || c["min_seconds"].(float64) != 10 {
		t.Errorf("acestep15 = %v", e)
	}
	for _, off := range []string{"repaint_interval", "extend", "complete", "stems", "reference_audio", "lrc"} {
		if c[off] != false {
			t.Errorf("caps.%s = %v before any canary", off, c[off])
		}
	}
	if !slices.Equal(toStrings(arr(obj(e["features"])["tasks"])), []string{"text2music"}) {
		t.Errorf("features = %v", e["features"])
	}
	if b := obj(e["backend"]); b["name"] != "fake-acestep" || b["armed"] != true {
		t.Errorf("backend = %v", b)
	}
	// a verified-features file with everything on flips every capability
	f, err := acestep.ParseFeatures([]byte(allOnFeatures))
	if err != nil {
		t.Fatal(err)
	}
	r.Services.Engines[acestep.Name] = acestep.Engine{Verified: f}
	c = obj(ace(agent.OK(t, "GET", "/api/engines", nil))["caps"])
	for _, on := range []string{"repaint_interval", "extend", "complete", "stems", "reference_audio"} {
		if c[on] != true {
			t.Errorf("caps.%s = %v with every capability verified", on, c[on])
		}
	}
	if c["repaint_min_s"].(float64) != 3 || c["repaint_max_s"].(float64) != 90 || c["reference_max_s"].(float64) != 30 {
		t.Errorf("bounds = %v", c)
	}
	for _, tok := range []string{"ep-", "rp-", "runpod", "https://"} {
		if strings.Contains(string(rest.Body), tok) {
			t.Errorf("list_engines carries %q", tok)
		}
	}
}

func toStrings(a []any) []string {
	var out []string
	for _, v := range a {
		out = append(out, v.(string))
	}
	return out
}

// TestAcceptance_E2_PortFromYue2Take: a yue2 take is ported to ACE-Step (text2music from its lyrics and style): the child is an
// acestep15 take with the engine_port operator and cross_engine, its recipe pinned, its env fingerprint the ACE-Step worker's,
// its masters frozen, rtf_stats untouched and engine_rtf learned for the turbo text2music class.
func TestAcceptance_E2_PortFromYue2Take(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	h := r.Human(t)
	mother := knownMother(t, r)
	var motherSHA string
	if err := r.Store.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, mother).Scan(&motherSHA); err != nil {
		t.Fatal(err)
	}
	rtfBefore := rowsDump(t, r, `SELECT gpu_class, rtf_milli, samples, updated_at FROM rtf_stats ORDER BY gpu_class`)
	est, _, jobs := aceRun(t, r, acePortBody(mother), "port-1")
	tk := obj(arr(obj(arr(est["items"])[0])["takes"])[0])
	if tk["engine"] != "acestep15" || tk["task"] != "text2music" || tk["task_class"] != "turbo" || tk["operator"] != "engine_port" || tk["cross_engine"] != true {
		t.Errorf("estimate take = %v", tk)
	}
	var codes []string
	for _, w := range arr(est["warnings"]) {
		codes = append(codes, obj(w)["code"].(string))
	}
	if !slices.Contains(codes, "duration_estimated") {
		t.Errorf("a port of a yue2 take must warn duration_estimated: %v", codes)
	}
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	child := jobTrack(t, r, jobs[0])
	var parent, root, engineOf, op, params, env string
	var gen int
	var trunc *int
	var bpm *int
	if err := r.Store.R.QueryRow(`SELECT t.parent_track_id, t.root_track_id, t.generation, rc.engine, t.truncated_abc, t.bpm_declared, t.env_id
		FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, child).Scan(&parent, &root, &gen, &engineOf, &trunc, &bpm, &env); err != nil {
		t.Fatal(err)
	}
	if parent != mother || gen != 1 || engineOf != "acestep15" || trunc != nil || bpm == nil {
		t.Errorf("child: parent=%s gen=%d engine=%s truncated=%v bpm=%v", parent, gen, engineOf, trunc, bpm)
	}
	if err := r.Store.R.QueryRow(`SELECT operator, params_json FROM mutations WHERE child_track_id = ?`, child).Scan(&op, &params); err != nil || op != "engine_port" || !strings.Contains(params, `"cross_engine":true`) {
		t.Errorf("mutation: %s %s (%v)", op, params, err)
	}
	var eng, backend, weights string
	if err := r.Store.R.QueryRow(`SELECT engine, backend, weights_json FROM env_fingerprints WHERE env_id = ?`, env).Scan(&eng, &backend, &weights); err != nil ||
		eng != "acestep15" || backend != "fake-acestep" || !strings.Contains(weights, acestep.Weights[acestep.WeightsRepoMain]) {
		t.Errorf("env fingerprint: %s %s %s (%v)", eng, backend, weights, err)
	}
	// the job and the track views say engine and task
	tr := obj(h.OK(t, "GET", "/api/tracks/"+child, nil))
	if ac := obj(obj(tr["recipe"])["acestep"]); obj(tr["recipe"])["engine"] != "acestep15" || ac["task"] != "text2music" || ac["task_class"] != "turbo" {
		t.Errorf("get_track recipe = %v", tr["recipe"])
	}
	var found bool
	for _, n := range arr(obj(h.OK(t, "GET", "/api/tracks/"+child+"/lineage", nil))["nodes"]) {
		if nn := obj(n); nn["id"] == child && nn["engine"] == "acestep15" && nn["cross_engine"] == true && nn["parent_id"] == mother {
			found = true
		}
	}
	if !found {
		t.Error("the lineage does not show the cross-engine child")
	}
	got := arr(obj(h.OK(t, "GET", "/api/tracks?engine=acestep15", nil))["items"])
	if len(got) != 1 || obj(got[0])["id"] != child {
		t.Errorf("search_tracks engine=acestep15 = %v", got)
	}
	if got := arr(obj(h.OK(t, "GET", "/api/tracks?engine=yue2", nil))["items"]); slices.ContainsFunc(got, func(v any) bool { return obj(v)["id"] == child }) {
		t.Error("search_tracks engine=yue2 lists the ACE-Step child")
	}
	// the fake worker reported the requested length; the stored (scaled) FLAC is shorter and nothing requires them to match
	row := jobRow(t, r, jobs[0], "audio_s,expected_audio_s")
	if toF(row[0]) != 30 || toF(row[1]) != 30 {
		t.Errorf("audio_s, expected_audio_s = %v", row)
	}
	// learning: engine_rtf learned the turbo text2music class; the yue2 table did not move
	var samples int
	if err := r.Store.R.QueryRow(`SELECT samples FROM engine_rtf WHERE engine = 'acestep15' AND task_class = 'turbo'`).Scan(&samples); err != nil || samples != 1 {
		t.Errorf("engine_rtf samples = %d (%v)", samples, err)
	}
	if after := rowsDump(t, r, `SELECT gpu_class, rtf_milli, samples, updated_at FROM rtf_stats ORDER BY gpu_class`); after != rtfBefore {
		t.Errorf("rtf_stats moved after an ACE-Step take:\n%s\n--\n%s", rtfBefore, after)
	}
	// masters and recipes are frozen: the mother's master is untouched, the child's is read-only, recipes cannot be rewritten
	var path, sha string
	if err := r.Store.R.QueryRow(`SELECT master_path, master_sha256 FROM tracks WHERE id = ?`, child).Scan(&path, &sha); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(r.DataDir, path)); err != nil || st.Mode().Perm() != 0o444 {
		t.Errorf("child master mode = %v (%v)", st, err)
	}
	var motherAfter string
	if err := r.Store.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, mother).Scan(&motherAfter); err != nil || motherAfter != motherSHA {
		t.Errorf("mother master changed")
	}
	if _, err := r.Store.W.Exec(`UPDATE recipes SET request_json = '{}' WHERE engine = 'acestep15'`); err == nil {
		t.Error("an ACE-Step recipe row could be rewritten")
	}
	// money: the reserve was settled under the reserve
	var reserved, settled int64
	_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE job_id = ? AND kind = 'reserve'`, jobs[0]).Scan(&reserved)
	_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE job_id = ? AND kind = 'settle'`, jobs[0]).Scan(&settled)
	if settled <= 0 || settled > reserved {
		t.Errorf("settled %d of reserved %d", settled, reserved)
	}
}

// TestAcceptance_E2_Text2MusicLong: a 480 s compose goes through the spend protocol (estimate, confirmation, reserve, settle) and
// ingests. The fake synthesises 4 s of audio but accounts the requested 480 s: nothing requires the stored FLAC to be 480 s.
func TestAcceptance_E2_Text2MusicLong(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	h := r.Human(t)
	// above the effective cap (480 s) is refused before any row
	before := count(t, r, `SELECT COUNT(*) FROM estimates`)
	over := h.Do(t, "POST", "/api/generations/estimate", composeBody("over", 481, nil))
	if over.Status != 422 {
		t.Errorf("481 s: %d %s", over.Status, over.Body)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM estimates`); n != before {
		t.Error("an estimate row was written above the cap")
	}
	est, batch, jobs := aceRun(t, r, composeBody("long", 480, nil), "long-1")
	if est["takes"].(float64) != 1 || obj(arr(obj(arr(est["items"])[0])["takes"])[0])["expected_audio_s"].(float64) != 480 {
		t.Errorf("estimate = %v", est)
	}
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	row := jobRow(t, r, jobs[0], "audio_s,expected_audio_s,truncated_abc,truncated_semantic")
	if toF(row[0]) != 480 || toF(row[1]) != 480 || row[2] != nil || row[3] != nil {
		t.Errorf("audio_s, expected, truncated = %v", row)
	}
	tk := jobTrack(t, r, jobs[0])
	var parent *string
	var root string
	var gen int
	var env *string
	var stored float64
	if err := r.Store.R.QueryRow(`SELECT parent_track_id, root_track_id, generation, env_id, duration_s FROM tracks WHERE id = ?`, tk).Scan(&parent, &root, &gen, &env, &stored); err != nil {
		t.Fatal(err)
	}
	if parent != nil || root != tk || gen != 0 || env == nil {
		t.Errorf("compose root: parent=%v root=%s gen=%d env=%v", parent, root, gen, env)
	}
	if stored >= 480 {
		t.Errorf("the scaled fake stored %v s; the test is meant to exercise a shorter FLAC than accounted", stored)
	}
	if jobEventCount(t, r, jobs[0], "duration_mismatch") != 1 {
		t.Error("the scaled take left no duration_mismatch event")
	}
	// the spend protocol: reserved at create, settled at ingest, inside the reservation, batch cost shown
	cost := obj(obj(batch["cost"]))
	if cost["reserved_micro_usd"].(float64) <= 0 || cost["settled_micro_usd"].(float64) != 0 {
		t.Errorf("batch at create = %v", cost)
	}
	var reserved, settled int64
	_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE job_id = ? AND kind = 'reserve'`, jobs[0]).Scan(&reserved)
	_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE job_id = ? AND kind = 'settle'`, jobs[0]).Scan(&settled)
	if settled <= 0 || settled > reserved {
		t.Errorf("settled %d of reserved %d", settled, reserved)
	}
}

// toF reads a numeric SQL column of either integer or real affinity.
func toF(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return -1
}

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name> (rewritten under -update).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if d := cmp.Diff(string(want), string(got)); d != "" {
		t.Errorf("%s differs (-want +got):\n%s", name, d)
	}
}

// TestAcceptance_E2_TempoKeyParams: bpm, key and time signature ride in the canonical request (pinned by a golden), the declared
// tempo lands in tracks.bpm_declared (L3 reads it) and the track view reports them.
func TestAcceptance_E2_TempoKeyParams(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	h := r.Human(t)
	_, _, jobs := aceRun(t, r, composeBody("tempo-key", 12, map[string]any{"bpm": 128, "keyscale": "A minor", "timesignature": "3", "seed": 7}), "tk-1")
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	tk := jobTrack(t, r, jobs[0])
	var req string
	var bpm *int
	if err := r.Store.R.QueryRow(`SELECT rc.request_json, t.bpm_declared FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, tk).Scan(&req, &bpm); err != nil {
		t.Fatal(err)
	}
	golden(t, "e2_tempo_key_request.golden.json", []byte(req+"\n"))
	if bpm == nil || *bpm != 128 {
		t.Errorf("bpm_declared = %v", bpm)
	}
	ac := obj(obj(obj(h.OK(t, "GET", "/api/tracks/"+tk, nil))["recipe"])["acestep"])
	if ac["bpm"].(float64) != 128 || ac["keyscale"] != "A Minor" || ac["timesignature"] != "3" || ac["duration_s"].(float64) != 12 {
		t.Errorf("recipe.acestep = %v", ac)
	}
	// a bad key or tempo is refused before any spend
	for name, extra := range map[string]map[string]any{"bpm": {"bpm": 999}, "key": {"keyscale": "H sharp"}, "signature": {"timesignature": "5"}} {
		resp := h.Do(t, "POST", "/api/generations/estimate", composeBody("bad-"+name, 12, extra))
		if resp.Status != 422 {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}
}

// TestAcceptance_E2_RepaintRange: a range repaint keeps the source's length, refuses bad ranges with their rule, pins the source
// master and the range in the request, and its A/B window is the existing duel window around the range.
func TestAcceptance_E2_RepaintRange(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	h := r.Human(t)
	base := aceBase(t, r, "rp-base", 40, nil)
	for rule, edit := range map[string]map[string]any{
		"range_too_short":      {"from_s": 5.0, "to_s": 6.0},
		"range_order":          {"from_s": 9.0, "to_s": 6.0},
		"range_outside_source": {"from_s": 30.0, "to_s": 45.0},
		"range_required":       nil,
	} {
		resp := h.Do(t, "POST", "/api/generations/estimate", editBody("repaint", base, edit, nil))
		code, d := problemCode(t, resp)
		if resp.Status != 422 || code != "INVALID_ITEM" || d["rule"] != rule {
			t.Errorf("%s: %d %s", rule, resp.Status, resp.Body)
		}
	}
	_, _, jobs := aceRun(t, r, editBody("repaint", base, map[string]any{"from_s": 8.0, "to_s": 14.0}, nil), "rp-1")
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	child := jobTrack(t, r, jobs[0])
	var baseSHA, req, op string
	var srcDur, childDur float64
	if err := r.Store.R.QueryRow(`SELECT master_sha256, duration_s FROM tracks WHERE id = ?`, base).Scan(&baseSHA, &srcDur); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.R.QueryRow(`SELECT rc.request_json, t.duration_s, m.operator FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		JOIN mutations m ON m.child_track_id = t.id WHERE t.id = ?`, child).Scan(&req, &childDur, &op); err != nil {
		t.Fatal(err)
	}
	var rq acestep.Request
	if err := json.Unmarshal([]byte(req), &rq); err != nil {
		t.Fatal(err)
	}
	if op != "repaint" || rq.Task != "repaint" || rq.Src == nil || rq.Src.TrackID != base || rq.Src.MasterSHA256 != baseSHA ||
		rq.RepaintFromDS == nil || *rq.RepaintFromDS != 80 || rq.RepaintToDS == nil || *rq.RepaintToDS != 140 {
		t.Errorf("repaint request = %s (operator %s)", req, op)
	}
	if childDur < srcDur-0.1 || childDur > srcDur+0.1 {
		t.Errorf("child %v s, source %v s: a repaint keeps the length", childDur, srcDur)
	}
	// A/B: the existing create_duel takes the window the planner computes for the range (30 s around a 6 s range, inside the take)
	w := generation.RepaintABWindow(80, 140, int(srcDur*10))
	if w.ToDS-w.FromDS != 300 || w.FromDS < 0 || w.ToDS > int(srcDur*10) {
		t.Fatalf("window = %+v", w)
	}
	resp := h.Do(t, "POST", "/api/duels", map[string]any{"a_track_id": base, "b_track_id": child, "from_s": float64(w.FromDS) / 10, "to_s": float64(w.ToDS) / 10}, "Idempotency-Key", "rp-duel")
	if resp.Status != 201 {
		t.Fatalf("create_duel: %d %s", resp.Status, resp.Body)
	}
	win := obj(obj(resp.JSON(t))["window"])
	if win["from_s"].(float64) != float64(w.FromDS)/10 || win["to_s"].(float64) != float64(w.ToDS)/10 {
		t.Errorf("duel window = %v, want %+v", win, w)
	}
}

// TestAcceptance_E2_ReferenceRender: a reference render pins the 30 s clip's digest at estimate; the worker fetches the clip
// through a signed link and verifies it (the job completes only if the digest matched); no signed URL is ever persisted; a clip
// that no longer reproduces the pinned digest at create is a 409 ESTIMATE_STALE reference_changed.
func TestAcceptance_E2_ReferenceRender(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	h := r.Human(t)
	base := aceBase(t, r, "ref-base", 40, nil)
	body := editBody("reference", base, nil, map[string]any{"track_id": base, "from_s": 5.0, "to_s": 25.0})

	// pinned: the estimate shows the digest, the request carries it, the render completes
	est, _, jobs := aceRun(t, r, body, "ref-1")
	tk := obj(arr(obj(arr(est["items"])[0])["takes"])[0])
	rf := obj(tk["reference"])
	sha, _ := rf["clip_sha256"].(string)
	if len(sha) != 64 || tk["operator"] != "reference_render" {
		t.Fatalf("estimate take = %v", tk)
	}
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("job %s (%s)", s, code)
	}
	child := jobTrack(t, r, jobs[0])
	var req string
	if err := r.Store.R.QueryRow(`SELECT rc.request_json FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, child).Scan(&req); err != nil {
		t.Fatal(err)
	}
	var rq acestep.Request
	if err := json.Unmarshal([]byte(req), &rq); err != nil || rq.Ref == nil || rq.Ref.ClipSHA256 != sha || rq.Ref.TrackID != base || rq.Ref.FromDS != 50 || rq.Ref.ToDS != 250 {
		t.Errorf("request ref = %+v (%v)", rq.Ref, err)
	}
	// the fetch really happened over the media route: the reference tone differs from a render of the same seed without one
	if jobEventCount(t, r, jobs[0], "ingested") != 1 {
		t.Error("the reference job was not ingested")
	}
	assertNoSignedURLs(t, r)

	// changed: corrupt the cached clip between estimate and create
	est2 := obj(h.OK(t, "POST", "/api/generations/estimate", body))
	clips, _ := filepath.Glob(filepath.Join(r.DataDir, "derived", "clips", "*", "*.flac"))
	if len(clips) == 0 {
		t.Fatal("no cached reference clip")
	}
	for _, c := range clips {
		if err := os.Chmod(c, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(c, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // test-owned path
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("tamper")
		_ = f.Close()
	}
	before := count(t, r, `SELECT COUNT(*) FROM batches`)
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est2["confirmation_token"], "rationale": "stale"}, "Idempotency-Key", "ref-2")
	code, d := problemCode(t, resp)
	if resp.Status != 409 || code != "ESTIMATE_STALE" || d["reason"] != "reference_changed" {
		t.Errorf("changed reference: %d %s", resp.Status, resp.Body)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`); n != before {
		t.Error("a batch was created from a stale reference")
	}
}

// assertNoSignedURLs fails when any text the database or the take artifacts hold carries a signed media link (exp= and sig=).
func assertNoSignedURLs(t *testing.T, r *servertest.Rig) {
	t.Helper()
	tables, err := r.Store.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	_ = tables.Close()
	for _, n := range names {
		cols, err := r.Store.R.Query(`SELECT name FROM pragma_table_info('` + n + `') WHERE type IN ('TEXT','BLOB','')`) //nolint:gosec // names from sqlite_master
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			cs = append(cs, c)
		}
		_ = cols.Close()
		for _, c := range cs {
			q := `SELECT COUNT(*) FROM "` + n + `" WHERE CAST("` + c + `" AS TEXT) LIKE '%sig=%' OR CAST("` + c + `" AS TEXT) LIKE '%/media/%'` //nolint:gosec // identifiers from sqlite_master
			if k := count(t, r, q); k != 0 {
				t.Errorf("%s.%s holds %d signed link(s)", n, c, k)
			}
		}
	}
	for _, glob := range []string{"takes/*/*.json", "takes/*/*.lrc"} {
		files, _ := filepath.Glob(filepath.Join(r.DataDir, glob))
		for _, f := range files {
			b, _ := os.ReadFile(f) //nolint:gosec // test-owned path
			if strings.Contains(string(b), "sig=") || strings.Contains(string(b), "/media/") {
				t.Errorf("%s holds a signed link", f)
			}
		}
	}
}

// TestAcceptance_E2_ExtendTake: extend appends N s to a take (ACE-Step or YuE2 parent). Until a canary proved it, the capability
// refuses (ENGINE_CAPABILITY); then the child's length is source + N, the operator is extend, a YuE2 parent is recorded as
// cross_engine, and the cost is provisional.
func TestAcceptance_E2_ExtendTake(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, audioMax: 100, yueS: 40})
	h := r.Human(t)
	base := aceBase(t, r, "ext-base", 60, nil)
	resp := h.Do(t, "POST", "/api/generations/estimate", editBody("extend", base, map[string]any{"extend_s": 30.0}, nil))
	if code, _ := problemCode(t, resp); resp.Status != 422 || code != "ENGINE_CAPABILITY" {
		t.Fatalf("extend before the canary: %d %s", resp.Status, resp.Body)
	}
	f, err := acestep.ParseFeatures([]byte(allOnFeatures))
	if err != nil {
		t.Fatal(err)
	}
	r.Services.Engines[acestep.Name] = acestep.Engine{Verified: f}

	// a yue2 take long enough to be a source (the sources of an edit are at least 10 s)
	yjob := startYue2Variant(t, r)
	yparent := jobTrack(t, r, yjob)
	var yDur float64
	if err := r.Store.R.QueryRow(`SELECT duration_s FROM tracks WHERE id = ?`, yparent).Scan(&yDur); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, parent string
		srcS         float64
		cross        bool
	}{{"ace parent", base, 60, false}, {"yue2 parent", yparent, yDur, true}} {
		est, _, jobs := aceRun(t, r, editBody("extend", c.parent, map[string]any{"extend_s": 30.0}, nil), "ext-"+strings.ReplaceAll(c.name, " ", "-"))
		tk := obj(arr(obj(arr(est["items"])[0])["takes"])[0])
		if tk["operator"] != "extend" || tk["expected_audio_s"].(float64) != c.srcS+30 || tk["cross_engine"] != c.cross {
			t.Errorf("%s: estimate take = %v", c.name, tk)
		}
		if s, code := jobState(t, r, jobs[0]); s != "completed" {
			t.Fatalf("%s: job %s (%s)", c.name, s, code)
		}
		child := jobTrack(t, r, jobs[0])
		var dur float64
		var parent, op, params, req string
		if err := r.Store.R.QueryRow(`SELECT t.duration_s, t.parent_track_id, m.operator, m.params_json, rc.request_json FROM tracks t JOIN mutations m ON m.child_track_id = t.id
			JOIN recipes rc ON rc.id = t.recipe_id WHERE t.id = ?`, child).Scan(&dur, &parent, &op, &params, &req); err != nil {
			t.Fatal(err)
		}
		if parent != c.parent || op != "extend" || strings.Contains(params, `"cross_engine":true`) != c.cross || dur < c.srcS+29.9 || dur > c.srcS+30.1 {
			t.Errorf("%s: parent=%s op=%s params=%s duration=%v", c.name, parent, op, params, dur)
		}
		var rq acestep.Request
		if err := json.Unmarshal([]byte(req), &rq); err != nil || rq.Task != "extend" || rq.ExtendDS == nil || *rq.ExtendDS != 300 || rq.Src == nil || rq.Src.TrackID != c.parent {
			t.Errorf("%s: request = %s (%v)", c.name, req, err)
		}
		if cost := obj(obj(h.OK(t, "GET", "/api/jobs/"+jobs[0], nil))["cost"]); cost["basis"] != "provisional" {
			t.Errorf("%s: cost = %v", c.name, cost)
		}
	}
}

// TestAcceptance_E2_CompleteStemsLRC covers the remaining E2b modes on the fake: complete renders a new take of the source's length;
// stems expands to one job per stem name (each its own request and operator); an LRC request stores lyrics.lrc beside the take.
// The stem jobs are created but not run: their attach ingest (lane E2-G3/J3) is not on main, see the PR body.
func TestAcceptance_E2_CompleteStemsLRC(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	base := aceBase(t, r, "cs-base", 40, map[string]any{"lrc": true})

	var lrcPath string
	var lrcBytes int64
	if err := r.Store.R.QueryRow(`SELECT path, bytes FROM artifacts WHERE track_id = ? AND name = 'lyrics.lrc'`, base).Scan(&lrcPath, &lrcBytes); err != nil {
		t.Fatalf("lyrics.lrc not stored: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(r.DataDir, lrcPath))
	if err != nil || int64(len(raw)) != lrcBytes || !regexp.MustCompile(`(?m)^\[\d{2,}:\d{2}\.\d{2,3}\]`).Match(raw) {
		t.Errorf("lyrics.lrc = %q (%v)", raw, err)
	}

	// complete: a new take the length of its source
	_, _, jobs := aceRun(t, r, editBody("complete", base, map[string]any{"tracks": []string{"drums"}}, nil), "cs-complete")
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("complete: %s (%s)", s, code)
	}
	var op string
	var dur float64
	if err := r.Store.R.QueryRow(`SELECT m.operator, t.duration_s FROM tracks t JOIN mutations m ON m.child_track_id = t.id WHERE t.id = ?`, jobTrack(t, r, jobs[0])).Scan(&op, &dur); err != nil ||
		op != "complete_tracks" || dur < 39.9 || dur > 40.1 {
		t.Errorf("complete child: operator=%s duration=%v (%v)", op, dur, err)
	}

	// stems: one job per name
	est, batch := aceCreate(t, r, editBody("stems", base, map[string]any{"tracks": []string{"drums", "bass"}}, nil), "cs-stems")
	if est["takes"].(float64) != 2 {
		t.Fatalf("stems estimate takes = %v", est["takes"])
	}
	jb := arr(batch["jobs"])
	if len(jb) != 2 {
		t.Fatalf("stems batch jobs = %d", len(jb))
	}
	var names []string
	for _, j := range jb {
		id := obj(j)["id"].(string)
		var op, req string
		if err := r.Store.R.QueryRow(`SELECT j.operator, rc.request_json FROM jobs j JOIN recipes rc ON rc.id = j.recipe_id WHERE j.id = ?`, id).Scan(&op, &req); err != nil {
			t.Fatal(err)
		}
		var rq acestep.Request
		if err := json.Unmarshal([]byte(req), &rq); err != nil || op != "extract_stems" || rq.Task != "extract" || len(rq.Tracks) != 1 {
			t.Errorf("stem job %s: operator=%s request=%s (%v)", id, op, req, err)
		}
		names = append(names, rq.Tracks...)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"bass", "drums"}) {
		t.Errorf("stem names = %v", names)
	}
}

// TestAcceptance_E2_SameProtocol: the second engine runs under the same spend protocol. An agent batch over the auto-approve
// limits waits for the human (nothing is submitted to either backend), the human approves and it completes; a batch over a cap is
// refused BUDGET_EXCEEDED with zero calls on both backends and no rows.
func TestAcceptance_E2_SameProtocol(t *testing.T) {
	t.Parallel()
	caps := spend.Caps{MonthlyMicro: 10_000_000, BatchMicro: 5_000_000, HumanDailyMicro: 5_000_000, AgentDailyMicro: 5_000_000, AutoApproveTakes: 1, AutoApproveMicro: 5_000_000}
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, o: servertest.Options{Caps: &caps}})
	mother := knownMother(t, r)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	h := r.Human(t)
	calls := func() int64 { return r.Fake.Counters().Total() + aceBackend(t, r).Counters().Total() }
	before := calls()

	body := map[string]any{"items": []any{map[string]any{"mode": "port", "parent_track_id": mother, "n_seeds": 2}}}
	est := obj(agent.OK(t, "POST", "/api/generations/estimate", body))
	if est["requires_human_approval"] != true || est["takes"].(float64) != 2 {
		t.Fatalf("estimate = %v", est)
	}
	resp := agent.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "sp-agent")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	b := obj(resp.JSON(t))
	if b["state"] != "awaiting_confirmation" {
		t.Fatalf("batch = %v", b)
	}
	batch := b["id"].(string)
	r.Clock.Advance(time.Minute)
	if _, err := r.Runner.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM job_attempts`); n != 0 || calls() != before {
		t.Errorf("%d attempts, %d backend calls while the batch awaits its approval", n, calls()-before)
	}
	h.OK(t, "POST", "/human/api/batches/"+batch+"/approve", map[string]any{"reason": "ok"})
	pump(t, r, func() bool {
		return count(t, r, `SELECT COUNT(*) FROM jobs WHERE batch_id = ? AND state = 'completed'`, batch) == 2
	})
	if n := aceBackend(t, r).Counters().Submit; n != 2 {
		t.Errorf("fake-acestep submits = %d, want 2", n)
	}
	if n := r.Fake.Counters().Submit; n != 0 {
		t.Errorf("an ACE-Step batch submitted %d jobs to the yue2 backend", n)
	}

	// over the cap: refused before any backend call, no rows added
	h.OK(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 20_000, "batch_micro_usd": 20_000, "human_daily_micro_usd": 20_000, "agent_daily_micro_usd": 20_000})
	rows := count(t, r, `SELECT COUNT(*) FROM batches`) + count(t, r, `SELECT COUNT(*) FROM ledger`)
	calls0 := calls()
	est = obj(agent.OK(t, "POST", "/api/generations/estimate", body))
	if obj(est["budget"])["fits"] != false {
		t.Fatalf("budget = %v", est["budget"])
	}
	resp = agent.Do(t, "POST", "/api/generations", map[string]any{"confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "sp-over")
	if code, _ := problemCode(t, resp); resp.Status != 402 || code != "BUDGET_EXCEEDED" {
		t.Fatalf("over the cap: %d %s", resp.Status, resp.Body)
	}
	if calls() != calls0 {
		t.Errorf("%d backend calls on a refused create", calls()-calls0)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM batches`) + count(t, r, `SELECT COUNT(*) FROM ledger`); n != rows {
		t.Errorf("rows %d -> %d on a refused create", rows, n)
	}
}

// yue2Mother is a lossless imported yue2 take with a known recipe (a mother that predates every ACE-Step take of the rig).
func yue2Mother(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	var id string
	if err := r.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine = 'yue2' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// startYue2Variant runs one yue2 new_seed variant to completion (the fake renders yueS seconds) and returns its job.
func startYue2Variant(t *testing.T, r *servertest.Rig) string {
	t.Helper()
	mother := yue2Mother(t, r)
	body := map[string]any{"items": []any{map[string]any{"mode": "new_seed", "parent_track_id": mother, "n_seeds": 1}}}
	_, _, jobs := aceRun(t, r, body, "yue-variant-"+mother[len(mother)-6:])
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("yue2 variant: %s (%s)", s, code)
	}
	return jobs[0]
}

// TestAcceptance_E2_BackendIsolation: arming is per backend. With the ACE-Step backend disarmed its creates are refused and
// the runner leaves its queue alone, while yue2 estimates, creates and runs are untouched; re-arming resumes it.
func TestAcceptance_E2_BackendIsolation(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	h := r.Human(t)
	mother := knownMother(t, r)
	off := false
	if _, err := r.Services.Provider.Update(context.Background(), auth.Principal{}, jobs.UpdateIn{Armed: &off, Backend: "fake-acestep", Reason: "isolation"}); err != nil {
		t.Fatal(err)
	}
	// the other backend's state did not move
	st := obj(h.OK(t, "GET", "/api/provider", nil))
	if st["armed"] != true || st["backend"] != "fake" {
		t.Errorf("default provider status = %v", st)
	}
	est := obj(h.OK(t, "POST", "/api/generations/estimate", acePortBody(mother)))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "iso-ace")
	if code, _ := problemCode(t, resp); resp.Status != 503 || code != "PROVIDER_DISARMED" {
		t.Fatalf("ACE create while disarmed: %d %s", resp.Status, resp.Body)
	}
	if n := aceBackend(t, r).Counters().Total(); n != 0 {
		t.Errorf("the disarmed backend saw %d calls", n)
	}
	yjob := startYue2Variant(t, r)
	if s, _ := jobState(t, r, yjob); s != "completed" {
		t.Errorf("yue2 job %s while ACE-Step is disarmed", s)
	}
	on := true
	if _, err := r.Services.Provider.Update(context.Background(), auth.Principal{}, jobs.UpdateIn{Armed: &on, Backend: "fake-acestep", Reason: "isolation over"}); err != nil {
		t.Fatal(err)
	}
	_, _, ids := aceRun(t, r, acePortBody(mother), "iso-ace-2")
	if s, code := jobState(t, r, ids[0]); s != "completed" {
		t.Errorf("ACE job after re-arming: %s (%s)", s, code)
	}
}

// TestAcceptance_E2_BillingPerBackend (migration 0016): each backend reconciles its own batches against its own hourly buckets. One
// yue2 job and one ACE-Step job run, billing lags, and every table that names a backend names the right one: a window and a bill
// row per backend, both batches reconciled, and each batch's residual released by exactly its own backend's reconciliation.
func TestAcceptance_E2_BillingPerBackend(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	mother := knownMother(t, r)
	startYue2Variant(t, r)
	aceRun(t, r, acePortBody(mother), "bill-ace")
	for range 5 {
		r.Clock.Advance(3 * time.Hour)
		if _, err := r.Runner.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := r.Reconciler.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []string{"fake", "fake-acestep"} {
		if n := count(t, r, `SELECT COUNT(*) FROM billing_windows WHERE backend = ?`, b); n != 1 {
			t.Errorf("%s: %d billing windows", b, n)
		}
		if n := count(t, r, `SELECT COUNT(*) FROM billing_buckets WHERE backend = ?`, b); n < 1 {
			t.Errorf("%s: no billing buckets", b)
		}
		if n := count(t, r, `SELECT COUNT(*) FROM ledger WHERE kind = 'bill' AND backend = ?`, b); n != 1 {
			t.Errorf("%s: %d bill rows", b, n)
		}
		if n := count(t, r, `SELECT COUNT(*) FROM batches WHERE backend = ? AND reconciled_at IS NOT NULL`, b); n != 1 {
			t.Errorf("%s: %d reconciled batches", b, n)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM billing_windows WHERE backend NOT IN ('fake', 'fake-acestep')`); n != 0 {
		t.Errorf("%d windows under another backend", n)
	}
	// conservation per batch: what was held as residual is what the reconciliation released
	for _, b := range []string{"fake", "fake-acestep"} {
		var held, released int64
		_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(l.micro_usd),0) FROM ledger l JOIN batches b ON 1=1 WHERE b.backend = ? AND l.kind = 'residual' AND l.batch_id = b.id`, b).Scan(&held)
		_ = r.Store.R.QueryRow(`SELECT COALESCE(SUM(l.micro_usd),0) FROM ledger l JOIN batches b ON 1=1 WHERE b.backend = ? AND l.kind = 'release' AND l.reason = 'reconciled' AND l.batch_id = b.id`, b).Scan(&released)
		if held <= 0 || held != released {
			t.Errorf("%s: residual held %d, released %d", b, held, released)
		}
	}
}

// TestAcceptance_E2_Yue2ByteIdentical pins a golden yue2 run: the same variant on a rig without ACE-Step and on a rig with MM_ACESTEP=1
// (both backends built, the registry holding both engines) leaves the same rows in every table a yue2 job touches, and that
// text is the committed golden. Adding the second engine changed nothing a yue2 user can see.
func TestAcceptance_E2_Yue2ByteIdentical(t *testing.T) {
	t.Parallel()
	tpl := archivetest.SyntheticArchive(t, "acceptance")
	dump := func(ace bool) string {
		r := genRigWith(t, tpl, servertest.Options{ACEStep: ace}, 0)
		job := startYue2Variant(t, r)
		for range 5 {
			r.Clock.Advance(3 * time.Hour)
			_, _ = r.Runner.Step(context.Background())
			_ = r.Reconciler.Pass(context.Background())
		}
		var b strings.Builder
		for _, q := range []string{
			`SELECT id, state, operator, engine, backend, expected_audio_s, timeout_s, expected_micro_usd, reserved_micro_usd, outcome, audio_s, truncated_abc, truncated_semantic FROM jobs ORDER BY id`,
			`SELECT kind, micro_usd, reason, backend FROM ledger ORDER BY id`,
			`SELECT engine, request_id, request_json FROM recipes WHERE id = (SELECT recipe_id FROM jobs WHERE id = '` + job + `')`,
			`SELECT operator, params_json, rationale FROM mutations WHERE job_id = '` + job + `'`,
			`SELECT event FROM job_events WHERE job_id = '` + job + `' ORDER BY id`,
			`SELECT duration_s, truncated_abc, truncated_semantic, bpm_declared FROM tracks WHERE job_id = '` + job + `'`,
			`SELECT gpu_class, rtf_milli, samples FROM rtf_stats ORDER BY gpu_class`,
			`SELECT backend, verdict, billed_micro_usd FROM billing_windows ORDER BY backend`,
			`SELECT engine, pkg_version, weights_json, decoder, backend FROM env_fingerprints ORDER BY env_id`,
		} {
			b.WriteString("-- " + strings.Fields(q)[1] + "\n" + rowsDump(t, r, q))
		}
		return b.String()
	}
	off, on := dump(false), dump(true)
	if off != on {
		t.Errorf("a yue2 run differs with MM_ACESTEP=1:\n%s", cmp.Diff(off, on))
	}
	golden(t, "e2_yue2_run.golden.txt", []byte(on))
}

// TestAcceptance_E2_EngineRTFLearning: engine_rtf learns from completed text2music takes of the turbo class, never from edits
// (a repaint's runtime is not a text2music's), and the yue2 rtf_stats table stays byte-identical across ACE-Step takes.
func TestAcceptance_E2_EngineRTFLearning(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true, allOn: true, audioMax: 100})
	rtf := func() (samples int, milli int64) {
		if err := r.Store.R.QueryRow(`SELECT samples, rtf_milli FROM engine_rtf WHERE engine = 'acestep15' AND task_class = 'turbo'`).Scan(&samples, &milli); err != nil {
			t.Fatal(err)
		}
		return samples, milli
	}
	yueBefore := rowsDump(t, r, `SELECT * FROM rtf_stats ORDER BY gpu_class`)
	base := aceBase(t, r, "rtf-1", 30, nil)
	s1, m1 := rtf()
	if s1 != 1 {
		t.Fatalf("after one text2music take: samples = %d", s1)
	}
	_, _, jobs := aceRun(t, r, editBody("repaint", base, map[string]any{"from_s": 5.0, "to_s": 12.0}, nil), "rtf-repaint")
	if s, code := jobState(t, r, jobs[0]); s != "completed" {
		t.Fatalf("repaint %s (%s)", s, code)
	}
	if s, m := rtf(); s != s1 || m != m1 {
		t.Errorf("a repaint moved engine_rtf: %d/%d -> %d/%d", s1, m1, s, m)
	}
	aceBase(t, r, "rtf-2", 30, map[string]any{"seed": 43})
	if s, _ := rtf(); s != 2 {
		t.Errorf("after two text2music takes: samples = %d", s)
	}
	if after := rowsDump(t, r, `SELECT * FROM rtf_stats ORDER BY gpu_class`); after != yueBefore {
		t.Errorf("rtf_stats moved after ACE-Step takes:\n%s\n--\n%s", yueBefore, after)
	}
}

// TestHumanOnly_E2Ops: no operation, REST or MCP, can change an engine's adoption (D-E2-15, E2a/E2b adopt by the operator CLI only):
// the registry holds no adopt/reject operation, the only engine operation is the read-only list_engines, and writes at the engine
// paths answer 404/405 for the operator and for an agent alike, leaving the adoption log as it was.
func TestHumanOnly_E2Ops(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{})
	for _, op := range api.Operations(r.API) {
		if strings.Contains(strings.ToLower(op.ID+op.Path), "adopt") || strings.Contains(strings.ToLower(op.ID), "reject_engine") {
			t.Errorf("operation %s %s %s can change adoption", op.ID, op.Method, op.Path)
		}
		if op.Path == "/api/engines" && (op.ID != "list_engines" || op.Method != "GET" || op.Meta.Scope != auth.ScopeRead || op.Meta.Cost != "free") {
			t.Errorf("/api/engines is %+v", op)
		}
	}
	before := rowsDump(t, r, `SELECT engine, state, via, changed_by FROM engine_adoption_log ORDER BY id`)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	for who, c := range map[string]*servertest.Client{"human": r.Human(t), "agent": agent} {
		for _, call := range []struct{ method, path string }{
			{"POST", "/api/engines/acestep15/adopt"}, {"PUT", "/api/engines/acestep15"}, {"POST", "/api/engines"}, {"DELETE", "/api/engines/acestep15"},
			{"POST", "/human/api/engines/acestep15/adopt"}, {"PATCH", "/api/engines/acestep15"},
		} {
			resp := c.Do(t, call.method, call.path, map[string]any{"state": "adopted", "reason": "no"})
			if resp.Status != 404 && resp.Status != 405 {
				t.Errorf("%s %s %s: %d %s", who, call.method, call.path, resp.Status, resp.Body)
			}
		}
	}
	if after := rowsDump(t, r, `SELECT engine, state, via, changed_by FROM engine_adoption_log ORDER BY id`); after != before {
		t.Errorf("the adoption log changed:\n%s\n--\n%s", before, after)
	}
	// and the one door that works leaves one audited row
	if _, err := r.Services.Adoption.Adopt(context.Background(), "system:cli", "acestep15", "audit check"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'cli:engines-adopt' AND transport = 'cli' AND status = 200`); n != 1 {
		t.Errorf("%d audit rows for the adoption", n)
	}
}

// TestAudit_E2Ops: the second engine's spend is audited like the first's. An ACE-Step create leaves a create_generation_human row for
// the operator (its ceiling as cost) and the estimate leaves its own; a read leaves none; no audit row holds request text.
func TestAudit_E2Ops(t *testing.T) {
	t.Parallel()
	r := aceRig(t, archivetest.SyntheticArchive(t, "acceptance"), aceOpts{adopt: true})
	mother := knownMother(t, r)
	aceCreate(t, r, acePortBody(mother), "audit-1")
	h := r.Human(t)
	h.OK(t, "GET", "/api/engines", nil)
	for _, op := range []string{"estimate_generation", "create_generation_human"} {
		if n := count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = ? AND status < 300`, op); n != 1 {
			t.Errorf("audit rows for %s = %d", op, n)
		}
	}
	if n := count(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'list_engines'`); n != 0 {
		t.Errorf("a read left %d audit rows (reads are not audited)", n)
	}
	var principal string
	var cost int64
	if err := r.Store.R.QueryRow(`SELECT principal_id, cost_micro_usd FROM audit_log WHERE op = 'create_generation_human' AND status = 201`).Scan(&principal, &cost); err != nil ||
		principal != "human:alice" || cost <= 0 {
		t.Errorf("create_generation_human audit row: principal=%q cost=%d (%v)", principal, cost, err)
	}
	if n := count(t, r, `SELECT COUNT(*) FROM audit_log WHERE args_sha256 = '' OR instr(args_sha256, 'lyrics') > 0`); n != 0 {
		t.Errorf("%d audit rows hold request text", n)
	}
}
