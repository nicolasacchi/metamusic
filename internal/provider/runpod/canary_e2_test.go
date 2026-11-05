package runpod_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// aceRest is a scripted server for the ACE-Step canary: it answers like the real REST surface for the items the canary
// sends, with a deterministic render (the artifact digest is a hash of the request, so the same request gives the same
// bytes) and a provider status whose backends[] carries the acestep15 entry.
type aceRest struct {
	*httptest.Server
	mu        sync.Mutex
	estimates []map[string]any // the estimate bodies, in order
	jobs      map[string]map[string]any
	tracks    map[string]float64 // track id -> audio seconds
	nTrack    int
	nJob      int
	patches   []string
	waits     map[string]int
	ceiling   int64
	// knobs
	runtimeS     int
	tamper       func(item map[string]any, j map[string]any) // alters the finished job
	refuse       func(item map[string]any) (int, map[string]any)
	noBackend    bool
	trip         map[string]any // shown once a job exists (init-fail)
	workersMax   int
	jobsFinished int
	extraTimings map[string]any
}

func newAceRest(t *testing.T) *aceRest {
	t.Helper()
	s := &aceRest{jobs: map[string]map[string]any{}, tracks: map[string]float64{}, waits: map[string]int{}, ceiling: 100_000, runtimeS: 900, workersMax: 1}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *aceRest) backend(engine string, running int) map[string]any {
	p := baseProvider()
	p["engine"] = engine
	p["max_runtime_s"] = s.runtimeS
	p["workers"] = map[string]any{"running": running, "idle": 0, "initializing": 0}
	p["endpoint"] = map[string]any{"idle_timeout_s": 20, "workers_max": s.workersMax}
	if engine == "acestep15" && s.trip != nil && len(s.jobs) > 0 {
		p["trip"] = s.trip
	}
	return p
}

func (s *aceRest) providerBody() map[string]any {
	live := 1
	if s.jobsFinished == len(s.jobs) && len(s.jobs) > 0 && s.trip == nil {
		live = 0
	}
	if s.trip != nil && len(s.jobs) > 0 && s.waits["cancelled"] > 0 {
		live = 0
	}
	body := s.backend("yue2", 0) // the default object is the yue2 backend's, as in the real API
	bs := []any{s.backend("yue2", 0)}
	if !s.noBackend {
		bs = append(bs, s.backend("acestep15", live))
	}
	body["backends"] = bs
	return body
}

func (s *aceRest) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	out := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/provider":
		out(200, s.providerBody())
	case r.Method == http.MethodPost && p == "/api/generations/estimate":
		var b map[string]any
		_ = json.Unmarshal(body, &b)
		s.estimates = append(s.estimates, b)
		if s.refuse != nil {
			items, _ := b["items"].([]any)
			if st, e := s.refuse(items[0].(map[string]any)); st != 0 {
				out(st, e)
				return
			}
		}
		out(200, map[string]any{"confirmation_token": fmt.Sprintf("mmc_%052d", len(s.estimates)), "takes": 1, "requires_human_approval": false,
			"cost": map[string]any{"ceiling_micro_usd": s.ceiling}})
	case r.Method == http.MethodPost && p == "/api/generations":
		s.nJob++
		id := fmt.Sprintf("job-%d", s.nJob)
		item := s.estimates[len(s.estimates)-1]["items"].([]any)[0].(map[string]any)
		s.jobs[id] = item
		out(201, map[string]any{"id": "batch-" + id, "state": "active", "jobs": []any{map[string]any{"id": id}},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": 100_000, "reconciled": true}})
	case strings.HasPrefix(p, "/api/jobs/") && strings.HasSuffix(p, "/wait"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/jobs/"), "/wait")
		s.waits[id]++
		if s.trip != nil { // init-fail: the job never starts
			out(200, map[string]any{"job": jobView("queued", nil), "changed": true})
			return
		}
		if s.waits[id] == 1 {
			out(200, map[string]any{"job": jobView("generating", nil), "changed": true})
			return
		}
		j := s.finish(s.jobs[id])
		s.jobsFinished++
		out(200, map[string]any{"job": j, "changed": true})
	case strings.HasPrefix(p, "/api/jobs/") && strings.HasSuffix(p, "/cancel"):
		s.waits["cancelled"]++
		out(200, jobView("cancelling", nil))
	case strings.HasPrefix(p, "/api/tracks/"):
		s.patches = append(s.patches, strings.TrimPrefix(p, "/api/tracks/")+" "+string(body))
		out(200, map[string]any{})
	case strings.HasPrefix(p, "/api/batches/"):
		out(200, map[string]any{"id": "b", "state": "finished", "jobs": []any{},
			"cost": map[string]any{"reserved_micro_usd": 300_000, "settled_micro_usd": 100_000, "reconciled": true}})
	default:
		out(404, map[string]any{"code": "NOT_FOUND", "message": "no route " + r.Method + " " + p})
	}
}

func jobView(state string, extra map[string]any) map[string]any { return job(state, extra) }

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// finish renders the job of an item: lengths follow the item, bytes follow the content (never the request id).
func (s *aceRest) finish(item map[string]any) map[string]any {
	audio := 30.0
	var track any
	arts := func(names ...string) []any {
		var out []any
		for _, n := range names {
			out = append(out, map[string]any{"name": n, "bytes": 1000, "sha256": digest([]any{item["mode"], item["edit"], item["reference"], item["seeds"], contentOf(item), n}), "verified": true})
		}
		return out
	}
	names := []string{"audio.flac", "request.json", "result.json"}
	switch item["mode"] {
	case "compose_new":
		r, _ := item["recipe"].(map[string]any)
		audio = r["duration_ds"].(float64) / 10
		if l, _ := r["lrc"].(bool); l {
			names = append(names, "lyrics.lrc")
		}
	case "repaint", "reference", "complete":
		audio = s.tracks[item["parent_track_id"].(string)]
	case "extend":
		audio = s.tracks[item["parent_track_id"].(string)] + item["edit"].(map[string]any)["extend_s"].(float64)
	case "stems":
		names = []string{"stem-vocals.flac", "result.json"}
	}
	if item["mode"] != "stems" {
		s.nTrack++
		id := fmt.Sprintf("trk-%d", s.nTrack)
		s.tracks[id] = audio
		track = id
	}
	timings := map[string]any{"execution_ms": 12_000, "cold_start_s": 60}
	for k, v := range s.extraTimings {
		timings[k] = v
	}
	if lrc := slices.Contains(names, "lyrics.lrc"); lrc {
		timings["execution_ms"] = 30_000
	}
	j := job("completed", map[string]any{"outcome": "clean", "track_id": track, "audio_s": audio, "expected_audio_s": int(audio),
		"artifacts": arts(names...), "env": map[string]any{"gpu_class": "RTX A5000"}, "timings": timings})
	if s.tamper != nil {
		s.tamper(item, j)
	}
	return j
}

// contentOf is the item without its request id and title: two requests that differ only in them render the same bytes.
func contentOf(item map[string]any) any {
	r, ok := item["recipe"].(map[string]any)
	if !ok {
		return nil
	}
	c := map[string]any{}
	for k, v := range r {
		if k != "id" {
			c[k] = v
		}
	}
	return c
}

func aceOpts(s *aceRest, c runpod.CanaryCase) runpod.CanaryOptions {
	o := canaryOpts(&restStub{Server: s.Server}, c)
	o.Engine = runpod.EngineACEStep
	o.MaxUSD = 10_000_000
	o.Pools = []string{"AMPERE_24", "ADA_24"}
	return o
}

func runACE(t *testing.T, s *aceRest, c runpod.CanaryCase, mod ...func(*runpod.CanaryOptions)) *runpod.CanaryReport {
	t.Helper()
	o := aceOpts(s, c)
	for _, m := range mod {
		m(&o)
	}
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil {
		t.Fatalf("%s: %v", c, err)
	}
	return rep
}

func stageLabels(rep *runpod.CanaryReport) []string {
	var out []string
	for _, st := range rep.Stages {
		out = append(out, st.Label)
	}
	return out
}

// ---- tests ----------------------------------------------------------------------------------------------------

func TestCanary_RefusesWithoutMaxUSD_ACEStep(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	o := aceOpts(s, runpod.CaseT2M)
	o.MaxUSD = 0
	if _, err := runpod.RunCanary(context.Background(), o); !errors.Is(err, runpod.ErrMaxUSDRequired) {
		t.Fatalf("err %v", err)
	}
	if len(s.estimates) != 0 {
		t.Error("the canary reached the server before refusing")
	}
	// and a yue2 run keeps its default ceiling
	y := newRestStub(t, goodMain()...)
	y.providerAfter = baseProvider()
	y.provider["workers"] = map[string]any{"running": 1, "idle": 0, "initializing": 0}
	if rep, err := runpod.RunCanary(context.Background(), canaryOpts(y, runpod.CaseMain)); err != nil || rep.Exit != runpod.ExitPass {
		t.Fatalf("yue2 canary: %+v %v", rep, err)
	}
}

func TestCanary_ScriptAgainstFake_ACEStep(t *testing.T) {
	t.Parallel()
	stagesOf := map[runpod.CanaryCase][]string{
		runpod.CaseT2M:       {"t2m"},
		runpod.CaseT2MLong:   {"t2m-long"},
		runpod.CaseReference: {"source", "seed-a", "seed-b", "ref-a", "ref-b"},
		runpod.CaseRepaint:   {"source", "range", "source-long", "boundary"},
		runpod.CaseFirstPull: {"first-pull"},
	}
	for c, want := range stagesOf {
		t.Run(string(c), func(t *testing.T) {
			t.Parallel()
			s := newAceRest(t)
			rep := runACE(t, s, c)
			if rep.Exit != runpod.ExitPass || rep.Outcome != "pass" {
				t.Fatalf("rep %+v", rep)
			}
			if got := stageLabels(rep); !slices.Equal(got, want) {
				t.Errorf("stages %v, want %v", got, want)
			}
			if rep.Engine != "acestep15" || rep.Proposed == nil && c != runpod.CaseFirstPull {
				t.Errorf("engine %q proposed %+v", rep.Engine, rep.Proposed)
			}
			// every estimate names the engine; edit items carry the source track and no secrets leak
			for _, e := range s.estimates {
				if e["engine"] != "acestep15" {
					t.Errorf("estimate engine %v", e["engine"])
				}
			}
			out, _ := json.Marshal(rep)
			if strings.Contains(string(out), canary("mm-token")) || strings.Contains(string(out), "mmc_") {
				t.Errorf("the report leaks: %s", out)
			}
			// the takes the case made are discarded (a multi-stage case discards its sources too)
			if len(s.patches) != len(s.tracks) {
				t.Errorf("discarded %d tracks of %d: %v", len(s.patches), len(s.tracks), s.patches)
			}
		})
	}
}

func TestCanary_ProposedPatch_FlagsOnlyForPassingCases(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	rep := runACE(t, s, runpod.CaseReference)
	p := rep.Proposed
	if p.Features["reference"] != true || p.Features["reference_reproducible"] != true || p.Features["seed_reproducible"] != true {
		t.Errorf("features %+v", p.Features)
	}
	// a render that is not reproducible is recorded, and its flag is not proposed
	s2 := newAceRest(t)
	n := 0
	s2.tamper = func(item map[string]any, j map[string]any) {
		if item["mode"] == "reference" {
			n++
			for _, a := range j["artifacts"].([]any) {
				a.(map[string]any)["sha256"] = digest(n)
			}
		}
	}
	rep = runACE(t, s2, runpod.CaseReference)
	if rep.Exit != runpod.ExitPass {
		t.Fatalf("not reproducible is a finding, not a failure: %+v", rep)
	}
	if _, has := rep.Proposed.Features["reference_reproducible"]; has || rep.Measured["reference_reproducible"] != false {
		t.Errorf("features %+v measured %v", rep.Proposed.Features, rep.Measured["reference_reproducible"])
	}
	// a failing stage proposes nothing and discards what was rendered
	s3 := newAceRest(t)
	s3.tamper = func(item map[string]any, j map[string]any) {
		if item["mode"] == "repaint" {
			j["audio_s"] = 12.0
		}
	}
	rep = runACE(t, s3, runpod.CaseRepaint)
	if rep.Exit != runpod.ExitFail || rep.Proposed != nil {
		t.Fatalf("repaint with a wrong length: %+v", rep)
	}
	if len(s3.patches) < 2 {
		t.Errorf("tracks of the failed case not discarded: %v", s3.patches)
	}
	// repaint proposes the task only with the 90 s boundary run
	rep = runACE(t, newAceRest(t), runpod.CaseRepaint)
	if !slices.Equal(rep.Proposed.TasksAdd, []string{"repaint"}) {
		t.Errorf("tasks_add %v", rep.Proposed.TasksAdd)
	}
	s4 := newAceRest(t)
	s4.tracks["trk-src"] = 30
	rep = runACE(t, s4, runpod.CaseRepaint, func(o *runpod.CanaryOptions) { o.SourceTrack = "trk-src" })
	if rep.Exit != runpod.ExitPass || len(rep.Proposed.TasksAdd) != 0 || !slices.Equal(stageLabels(rep), []string{"range"}) {
		t.Errorf("with --source-track: %+v", rep)
	}
	for _, p := range s4.patches {
		if strings.HasPrefix(p, "trk-src ") {
			t.Error("the operator's source track was discarded")
		}
	}
}

func TestCanary_ReferenceTwiceCase_Fake(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	rep := runACE(t, s, runpod.CaseReference)
	var refs []map[string]any
	for _, e := range s.estimates {
		it := e["items"].([]any)[0].(map[string]any)
		if it["mode"] == "reference" {
			refs = append(refs, it)
		}
	}
	if len(refs) != 2 || fmt.Sprint(refs[0]) != fmt.Sprint(refs[1]) {
		t.Fatalf("the reference render is not requested twice with the same seed and window: %v", refs)
	}
	if rep.Measured["seed_reproducible"] != true || rep.Measured["reference_reproducible"] != true {
		t.Errorf("measured %v", rep.Measured)
	}
	rf := refs[0]["reference"].(map[string]any)
	if rf["from_s"].(float64) != 5 || rf["to_s"].(float64) != 25 {
		t.Errorf("the reference window is %v (a 20 s window)", rf)
	}
}

func TestCanary_P0bRecordedOutputs(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	s.extraTimings = map[string]any{"peak_vram_mb_dit": 9100, "peak_vram_mb_lm": 5200, "lm_gpu_memory_utilization_milli": 350}
	rep := runACE(t, s, runpod.CaseT2M)
	v, _ := rep.Measured["t2m.peak_vram_mb"].(map[string]int64)
	if v["dit"] != 9100 || v["lm"] != 5200 || rep.Measured["t2m.lm_gpu_memory_utilization_milli"] != int64(350) {
		t.Errorf("measured %v", rep.Measured)
	}
	rep = runACE(t, newAceRest(t), runpod.CaseT2M)
	if rep.Measured["t2m.peak_vram_mb"] != "not_reported_by_job_view" {
		t.Errorf("an unreported measurement must say so: %v", rep.Measured["t2m.peak_vram_mb"])
	}
}

func TestCanary_ACEStep_BackendMissing(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	s.noBackend = true
	_, err := runpod.RunCanary(context.Background(), aceOpts(s, runpod.CaseT2M))
	if err == nil || !strings.Contains(err.Error(), "no acestep15 backend") {
		t.Fatalf("err %v", err)
	}
}

func TestCanary_ACEStep_ServerGuardRefusalIsRefused(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	s.refuse = func(map[string]any) (int, map[string]any) {
		return 422, map[string]any{"code": "INVALID_ITEM", "message": "item 0 rule exceeds_max_runtime"}
	}
	rep := runACE(t, s, runpod.CaseT2MLong)
	if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "exceeds_max_runtime") {
		t.Fatalf("rep %+v", rep)
	}
	if s.nJob != 0 {
		t.Error("a batch was created")
	}
}

func TestCanary_ACEStep_CumulativeCeiling(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	s.ceiling = 400_000
	rep := runACE(t, s, runpod.CaseReference, func(o *runpod.CanaryOptions) { o.MaxUSD = 1_000_000 })
	if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "ceilings") {
		t.Fatalf("rep %+v", rep)
	}
	if s.nJob != 2 {
		t.Errorf("%d batches created before the sum passed --max-usd (want 2)", s.nJob)
	}
}

func TestCanary_ACEStep_InitFailCase_Fake(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	if _, err := runpod.RunCanary(context.Background(), aceOpts(s, runpod.CaseInitFail)); !errors.Is(err, runpod.ErrInitFailConfirm) {
		t.Fatalf("without the confirmation: %v", err)
	}
	if len(s.estimates) != 0 {
		t.Error("estimated without the confirmation")
	}
	s.trip = map[string]any{"id": "t1", "reason": "crash_loop"}
	rep := runACE(t, s, runpod.CaseInitFail, func(o *runpod.CanaryOptions) { o.InitFailConfirm = true })
	if rep.Exit != runpod.ExitPass || rep.Measured["init-fail.trip_reason"] != "crash_loop" || rep.Measured["init-fail.workers_after"] != 0 {
		t.Fatalf("rep %+v", rep)
	}
	if s.waits["cancelled"] == 0 {
		t.Error("the job was not cancelled")
	}
	// no trip within the window: the case fails
	s2 := newAceRest(t)
	s2.trip = map[string]any{"id": "t1", "reason": "stuck_running"}
	rep = runACE(t, s2, runpod.CaseInitFail, func(o *runpod.CanaryOptions) { o.InitFailConfirm = true })
	if rep.Exit != runpod.ExitFail {
		t.Fatalf("a different trip must not satisfy the case: %+v", rep)
	}
}

func TestCanary_ACEStep_ParseCase(t *testing.T) {
	t.Parallel()
	a := runpod.ACEStepCanaryCases("a")
	wantA := []runpod.CanaryCase{"t2m", "t2m-long", "reference", "repaint", "terminate", "timeout", "kill-switch", "init-fail", "first-pull"}
	if !slices.Equal(a, wantA) {
		t.Fatalf("order a=%v", a)
	}
	for _, c := range a {
		if got, err := runpod.ParseACEStepCase(string(c)); err != nil || got != c {
			t.Errorf("%q: %v", c, err)
		}
	}
	if _, err := runpod.ParseACEStepCase("main"); err == nil {
		t.Error("a yue2 case name was accepted for acestep15")
	}
}

func TestCanary_ACEStep_TerminateAndKillSwitchReuseM3(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	s.runtimeS = 900
	// kill-switch: needs the flag, then the endpoint must be seen deactivated
	if _, err := runpod.RunCanary(context.Background(), aceOpts(s, runpod.CaseKillSwitch)); !errors.Is(err, runpod.ErrKillSwitchConfirm) {
		t.Fatalf("err %v", err)
	}
	rep := runACE(t, s, runpod.CaseKillSwitch, func(o *runpod.CanaryOptions) { o.KillSwitchConfirm = true })
	if rep.Exit != runpod.ExitFail || !strings.Contains(rep.Reason, "nobody flipped the kill switch") {
		t.Fatalf("rep %+v", rep)
	}
	// timeout: needs a 30 s runtime on the server
	rep = runACE(t, newAceRest(t), runpod.CaseTimeout)
	if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "30s") {
		t.Fatalf("rep %+v", rep)
	}
}

func TestMeasured_CommittedFileValid(t *testing.T) {
	t.Parallel()
	if err := runpod.ValidateMeasured(runpod.EmbeddedMeasured); err != nil {
		t.Fatal(err)
	}
}

func TestCanary_ProposedPatch_Golden(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for _, c := range []runpod.CanaryCase{runpod.CaseT2M, runpod.CaseReference, runpod.CaseRepaint} {
		s := newAceRest(t)
		rep := runACE(t, s, c)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("%s: %+v", c, rep)
		}
		b, _ := json.MarshalIndent(map[string]any{"case": c, "proposed": rep.Proposed}, "", "  ")
		sb.Write(b)
		sb.WriteString("\n")
	}
	golden(t, "canary_proposed_patch.golden.json", sb.String())
}

func TestMeasured_MergeRedactedAndLatestWins(t *testing.T) {
	t.Parallel()
	s := newAceRest(t)
	rep := runACE(t, s, runpod.CaseT2M)
	raw, err := runpod.MergeMeasured(runpod.EmbeddedMeasured, rep, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := runpod.ValidateMeasured(raw); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	for _, secret := range []string{canary("mm-token"), "mmc_", "batch-", "job-", "http"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the measured file contains %q:\n%s", secret, raw)
		}
	}
	var f runpod.MeasuredFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Cases["t2m"].Outcome != "pass" || f.Cases["t2m"].RecordedOn != "2026-10-14" {
		t.Fatalf("file %+v err %v", f, err)
	}
	// a second case is added next to the first; a refused run records nothing
	rep2 := runACE(t, newAceRest(t), runpod.CaseT2MLong)
	raw, err = runpod.MergeMeasured(raw, rep2, t0)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &f)
	if len(f.Cases) != 2 {
		t.Errorf("cases %v", f.Cases)
	}
	refused := &runpod.CanaryReport{Case: runpod.CaseRepaint, Engine: "acestep15", Outcome: "refused"}
	if raw2, err := runpod.MergeMeasured(raw, refused, t0); err != nil || string(raw2) != string(raw) {
		t.Errorf("a refused run changed the file: %v", err)
	}
	if _, err := runpod.MergeMeasured(nil, &runpod.CanaryReport{Engine: ""}, t0); err == nil {
		t.Error("a yue2 report was recorded")
	}
}

// TestFakeDefaults_MatchCanary_E2: the fake-acestep defaults (cold start, runtime, RTF) must be the values the canary
// measures. The measurements arrive with E2-X4a; X4 removes this skip together with the committed numbers.
func TestFakeDefaults_MatchCanary_E2(t *testing.T) {
	t.Skip("E2-X4: needs the measured engine_rtf rows of the first canary")
}
