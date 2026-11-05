package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// The ingest bridge per engine (C156, C164 ingest half). Fixtures are synthetic: an ACE-Step request, a synthetic FLAC.

const aceWeightRepo = "ACE-Step/Ace-Step1.5"

// aceEngines returns the registry engine whose verified features enable the given tasks (base models baked in when a
// base task is among them).
func aceEngines(tasks ...string) engine.Registry {
	reg := registry.Default(registry.Options{ACEStep: true})
	f := acestep.EmbeddedFeatures()
	if len(tasks) > 0 {
		f.Tasks = append([]string{"text2music"}, tasks...)
		f.CanaryCommit = "c0ffee"
		f.ModelsMode = acestep.ModelsBundleBaseBaked
	}
	reg[acestep.Name] = acestep.Engine{Verified: f}
	return reg
}

// newACERig is a bridge rig whose runner and bridge both know the engines of reg.
func newACERig(t *testing.T, heavy bool, reg engine.Registry, mods ...func(*BridgeOptions)) *bridgeRig {
	t.Helper()
	if heavy {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
	}
	t.Parallel()
	r := newRigAt(t, filepath.Join(t.TempDir(), "s.db"), t.TempDir(), true, func(_ *rig, o *RunnerOptions) { o.Engines, o.Inputs = reg, fakeMinter{} })
	return attachBridge(t, r, heavy, append([]func(*BridgeOptions){func(o *BridgeOptions) { o.Engines = reg }}, mods...)...)
}

// aceRequest returns the canonical ACE-Step request built from a valid base with over applied.
func aceRequest(t *testing.T, over map[string]any) string {
	t.Helper()
	m := map[string]any{"id": "syn-ace", "task": "text2music", "caption": "warm pop, female vocal", "lyrics": "[Verse]\nla la la",
		"instrumental": false, "vocal_language": "en", "duration_ds": 100, "seed": 7, "thinking": false, "bpm": 96}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	raw, _ := json.Marshal(m)
	_, canon, is := acestep.Parse(raw)
	for _, i := range is {
		if i.Severity == "error" {
			t.Fatalf("synthetic request: %s %s", i.Code, i.Message)
		}
	}
	return string(canon)
}

var aceRecipeN atomic.Int64

// aceRecipe inserts an acestep15 recipe and returns its id.
func (b *bridgeRig) aceRecipe(req string) string {
	b.t.Helper()
	id := fmt.Sprintf("01900000-0000-7000-8000-0000000e%04d", aceRecipeN.Add(1))
	var r struct {
		ID      string `json:"id"`
		Caption string `json:"caption"`
		Lyrics  string `json:"lyrics"`
		Seed    int64  `json:"seed"`
	}
	if err := json.Unmarshal([]byte(req), &r); err != nil {
		b.t.Fatal(err)
	}
	b.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'acestep15',?,?,?,?,?,'off',?,?,'agent:t1',?)`, id, r.ID, req, r.Caption, r.Lyrics, r.Seed, "hash-"+id, "core-"+id, store.FormatTime(t0))
	return id
}

// seedACE seeds a job of the acestep15 engine over recipe and returns its id.
func (b *bridgeRig) seedACE(recipe string) string {
	b.t.Helper()
	s := b.seed(batchSpec{recipe: recipe, operator: "compose_new", mutation: `{"operator":"compose_new","params":{"mode":"compose_new"},"principal_id":"agent:t1"}`})
	// the shared seed helper makes yue2 rows; this throwaway database drops the freeze triggers to relabel them
	b.exec(`DROP TRIGGER jobs_frozen`)
	b.exec(`DROP TRIGGER batches_frozen`)
	b.exec(`UPDATE jobs SET engine = 'acestep15' WHERE id = ?`, s.Jobs[0])
	b.exec(`UPDATE batches SET engine = 'acestep15' WHERE id = ?`, s.Batch)
	return s.Jobs[0]
}

// aceDeliver uploads the ACE-Step artifact set of spec and returns the worker output (truncated null, engine acestep15).
func (b *bridgeRig) aceDeliver(spec provider.TakeSpec, flac []byte, mutate func(*provider.Output)) provider.Output {
	b.t.Helper()
	out := provider.Output{Contract: provider.Contract, TakeID: spec.JobID, AttemptID: spec.AttemptID, Status: "ok",
		Artifacts: map[string]provider.ArtifactDigest{},
		Env: provider.OutputEnv{Engine: "acestep15", PkgVersion: "1.5.0", CodeCommit: "0000000000000000000000000000000000000004", Decoder: "ACE-Step/Ace-Step1.5:vae",
			GPUName: "NVIDIA GeForce RTX 4090", CUDA: "12.8", Torch: "2.10.0", WorkerBuild: "test", Python: "3.11",
			Weights: map[string]string{aceWeightRepo: "0123456789abcdef0123456789abcdef01234567"}}}
	n := json.Number("4")
	out.AudioSeconds = &n
	g, s, l := json.Number("3.5"), json.Number("0.5"), json.Number("1.0")
	out.Timings = map[string]*json.Number{"generate_s": &g, "save_s": &s, "load_s": &l}
	if mutate != nil {
		mutate(&out)
	}
	bodies := map[string][]byte{"audio.flac": flac, "request.json": []byte(`{"id":"x"}`), "result.json": []byte(`{}`)}
	for _, name := range sortedKeys(bodies) {
		if _, ok := spec.Uploads[name]; !ok {
			b.t.Fatalf("the ACE-Step spec has no upload target %s", name)
		}
		if code := b.put(spec.Uploads[name].URL, bodies[name]); code != 201 {
			b.t.Fatalf("upload %s: %d", name, code)
		}
		out.Artifacts[name] = provider.ArtifactDigest{SHA256: sha256hex(bodies[name]), Bytes: int64(len(bodies[name]))}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		b.t.Fatal(err)
	}
	if code := b.put(spec.Uploads[provider.ResultArtifact].URL, raw); code != 201 {
		b.t.Fatalf("upload mm-result: %d", code)
	}
	return out
}

// runACE drives one ACE-Step job through the real bridge to a terminal state.
func (b *bridgeRig) runACE(job string, flac []byte, mutate func(*provider.Output)) provider.Output {
	b.t.Helper()
	spec := b.specOf(job)
	out := b.aceDeliver(spec, flac, mutate)
	b.setStatus(b.refOf(job, 1), completed(out, 5000))
	for range 12 {
		b.step()
		if IsTerminal(b.state(job)) {
			break
		}
		b.advance(3e9)
	}
	return out
}

func TestIngest_ACEStep_TakeShape(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	const rtfDump = `SELECT COALESCE(group_concat(gpu_class || ':' || rtf_milli || ':' || samples, ','), '') FROM (SELECT * FROM rtf_stats ORDER BY gpu_class)`
	rtfBefore := b.str(rtfDump)
	b.runACE(job, b.flac(31), nil)
	b.wantState(job, StateCompleted)
	j := b.job(job)
	if j.Outcome != "clean" || j.TrackID == "" {
		t.Fatalf("job %+v events %v", j, b.events(job))
	}
	track := j.TrackID
	if got := b.str(`SELECT COALESCE(truncated_abc,'null') || '|' || COALESCE(truncated_semantic,'null') FROM tracks WHERE id = ?`, track); got != "null|null" {
		t.Errorf("track truncation %q, want unknown", got)
	}
	if got := b.str(`SELECT COALESCE(truncated_abc,'null') || '|' || COALESCE(truncated_semantic,'null') FROM jobs WHERE id = ?`, job); got != "null|null" {
		t.Errorf("job truncation %q, want unknown", got)
	}
	// C164: the declared tempo is the request's bpm, although the caption holds no tempo term
	if got := b.str(`SELECT bpm_declared FROM tracks WHERE id = ?`, track); got != "96" {
		t.Errorf("bpm_declared %q, want 96", got)
	}
	// yue2's rtf_stats is untouched; the engine's own table learned the turbo class
	if got := b.str(rtfDump); got != rtfBefore {
		t.Errorf("rtf_stats changed after an ACE-Step take: %q -> %q", rtfBefore, got)
	}
	if got := b.str(`SELECT engine || '|' || task_class || '|' || gpu_class || '|' || samples || '|' || updated_by FROM engine_rtf`); got != "acestep15|turbo|RTX 4090|1|job:"+job {
		t.Errorf("engine_rtf row %q", got)
	}
	// the environment fingerprint
	env := b.str(`SELECT env_id FROM tracks WHERE id = ?`, track)
	if got := b.str(`SELECT engine || '|' || backend || '|' || decoder FROM env_fingerprints WHERE env_id = ?`, env); got != "acestep15|fake|ACE-Step/Ace-Step1.5:vae" {
		t.Errorf("env row %q", got)
	}
	// the required set is the engine's, nothing of yue2's
	for _, n := range []string{"request.json", "result.json", "mm-result.json"} {
		if got := b.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = ?`, track, n); got != 1 {
			t.Errorf("artifact %s: %d rows", n, got)
		}
	}
	for _, n := range []string{"config.json", "score.abc"} {
		if got := b.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = ?`, track, n); got != 0 {
			t.Errorf("artifact %s must not exist for an ACE-Step take", n)
		}
	}
	// the master is frozen
	path := b.str(`SELECT master_path FROM tracks WHERE id = ?`, track)
	st, err := os.Stat(filepath.Join(b.dataDir, path))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o444 {
		t.Errorf("master mode %v, want 0444", st.Mode().Perm())
	}
	if got := b.str(`SELECT operator FROM mutations WHERE child_track_id = ?`, track); got != "compose_new" {
		t.Errorf("mutation operator %q", got)
	}
}

// TestIngest_Yue2PathUnchanged pins that a yue2 take still teaches rtf_stats, still reports truncation and writes no engine_rtf
// row, through a bridge that also knows the ACE-Step engine.
func TestIngest_Yue2PathUnchanged(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seed(batchSpec{recipe: b.newRecipe("pop, dream, 120 bpm")}).Jobs[0]
	b.run1(job, b.flac(32), [2]bool{false, true}, nil)
	b.wantState(job, StateCompleted)
	j := b.job(job)
	if j.Outcome != "truncated" {
		t.Errorf("outcome %q", j.Outcome)
	}
	if got := b.count(`SELECT samples FROM rtf_stats WHERE gpu_class = 'A5000'`); got != 1 {
		t.Errorf("rtf samples %d", got)
	}
	if n := b.count(`SELECT COUNT(*) FROM engine_rtf`); n != 0 {
		t.Errorf("engine_rtf has %d rows after a yue2 take", n)
	}
	if got := b.str(`SELECT bpm_declared FROM tracks WHERE id = ?`, j.TrackID); got != "120" {
		t.Errorf("yue2 bpm_declared %q, want the style term's 120", got)
	}
}

func TestIngest_TruncatedPerEngine(t *testing.T) {
	good := func() map[string][]byte {
		return map[string][]byte{"audio.flac": []byte("not really a flac"), "request.json": []byte(`{}`), "result.json": []byte(`{}`), "mm-result.json": []byte(`{}`)}
	}
	for _, c := range []struct {
		name      string
		yue       bool
		truncated map[string]bool
		wantCV    bool // a contract violation; otherwise the call gets past verify (and fails on the fake audio)
	}{
		{name: "acestep null", truncated: nil},
		{name: "acestep object", truncated: map[string]bool{"abc": false, "semantic": false}, wantCV: true},
		{name: "acestep empty object", truncated: map[string]bool{}, wantCV: true},
		{name: "yue2 null", yue: true, truncated: nil, wantCV: true},
		{name: "yue2 object", yue: true, truncated: map[string]bool{"abc": false, "semantic": false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newACERig(t, false, aceEngines())
			recipe := testRecipe
			bodies := good()
			if c.yue {
				bodies["config.json"] = []byte(`{}`)
				bodies["result.json"] = resultJSON(false, false)
			} else {
				recipe = b.aceRecipe(aceRequest(t, nil))
			}
			files, out := b.received(bodies)
			out.TakeID, out.Truncated = "job-x", c.truncated
			if !c.yue {
				out.Env.Engine = "acestep15"
			}
			_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: recipe}, AttemptRow{ID: "att-x"}, files, out)
			var cv *ContractViolation
			isCV := errors.As(err, &cv) && cv.Code == "WORKER_CONTRACT"
			if isCV != c.wantCV {
				t.Fatalf("err = %v, want contract violation %v", err, c.wantCV)
			}
		})
	}
}

func TestIngest_ACEStep_ContractViolations(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(files *[]upload.Received, out *provider.Output)
		want   string
	}{
		{name: "another engine's environment", mutate: func(_ *[]upload.Received, out *provider.Output) { out.Env.Engine = "yue2" }, want: "WORKER_CONTRACT"},
		{name: "request.json not reported", mutate: func(_ *[]upload.Received, out *provider.Output) { delete(out.Artifacts, "request.json") }, want: "ARTIFACT_MISSING"},
		{name: "result.json differs", mutate: func(_ *[]upload.Received, out *provider.Output) {
			d := out.Artifacts["result.json"]
			d.Bytes++
			out.Artifacts["result.json"] = d
		}, want: "ARTIFACT_MISMATCH"},
		{name: "no audio", mutate: func(f *[]upload.Received, out *provider.Output) {
			delete(out.Artifacts, "audio.flac")
			*f = slicesDelete(*f, "audio.flac")
		}, want: "ARTIFACT_MISSING"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newACERig(t, false, aceEngines())
			recipe := b.aceRecipe(aceRequest(t, nil))
			files, out := b.received(map[string][]byte{"audio.flac": []byte("not really a flac"), "request.json": []byte(`{}`), "result.json": []byte(`{}`), "mm-result.json": []byte(`{}`)})
			out.TakeID, out.Truncated = "job-x", nil
			out.Env.Engine = "acestep15"
			c.mutate(&files, &out)
			_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: recipe}, AttemptRow{ID: "att-x"}, files, out)
			var cv *ContractViolation
			if !errors.As(err, &cv) || cv.Code != c.want {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			if strings.Contains(cv.Text, "http") {
				t.Errorf("text %q", cv.Text)
			}
		})
	}
}

func slicesDelete(fs []upload.Received, artifact string) []upload.Received {
	var out []upload.Received
	for _, f := range fs {
		if f.Artifact != artifact {
			out = append(out, f)
		}
	}
	return out
}

func TestIngest_ACEStep_UnregisteredEngineFails(t *testing.T) {
	b := newACERig(t, false, registry.Default(registry.Options{})) // no acestep15 in the registry
	recipe := b.aceRecipe(aceRequest(t, nil))
	files, out := b.received(map[string][]byte{"audio.flac": []byte("x"), "request.json": []byte(`{}`), "result.json": []byte(`{}`), "mm-result.json": []byte(`{}`)})
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: recipe}, AttemptRow{ID: "att-x"}, files, out)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v", err)
	}
	if n := b.count(`SELECT COUNT(*) FROM tracks`); n != 0 {
		t.Errorf("%d tracks written", n)
	}
}

func srcOf(ds int) map[string]any {
	return map[string]any{"track_id": "trk-1", "master_sha256": strings.Repeat("a", 64), "duration_ds": ds}
}

// TestIngest_RecordsEngineRTF pins the learning rule (C152, C156): a take teaches its class only when it is a full-length
// render of an enabled task of that class, and the duration is net of the worker's load.
func TestIngest_RecordsEngineRTF(t *testing.T) {
	for i, c := range []struct {
		name  string
		tasks []string // enabled besides text2music
		over  map[string]any
		want  string // "" = nothing learned, else task_class
	}{
		{name: "text2music teaches turbo", over: nil, want: "turbo"},
		{name: "repaint never teaches", tasks: []string{"repaint"}, over: map[string]any{"task": "repaint", "duration_ds": nil, "thinking": false, "src": srcOf(1000), "repaint_from_ds": 100, "repaint_to_ds": 300}},
		{name: "extend never teaches", tasks: []string{"extend"}, over: map[string]any{"task": "extend", "duration_ds": nil, "thinking": false, "src": srcOf(2431), "extend_ds": 300}},
		{name: "complete teaches base when enabled", tasks: []string{"complete"}, over: map[string]any{"task": "complete", "duration_ds": nil, "thinking": false, "lyrics": "", "instrumental": true, "src": srcOf(1000), "tracks": []string{"drums"}}, want: "base"},
		{name: "extract teaches base when enabled", tasks: []string{"extract"}, over: map[string]any{"task": "extract", "duration_ds": nil, "thinking": false, "lyrics": "", "instrumental": true, "src": srcOf(1000), "tracks": []string{"vocals"}}, want: "base"},
		{name: "complete not enabled teaches nothing", over: map[string]any{"task": "complete", "duration_ds": nil, "thinking": false, "lyrics": "", "instrumental": true, "src": srcOf(1000), "tracks": []string{"drums"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newACERig(t, true, aceEngines(c.tasks...))
			job := b.seedACE(b.aceRecipe(aceRequest(t, c.over)))
			b.runACE(job, b.flac(40+i), nil)
			b.wantState(job, StateCompleted)
			rows := b.str(`SELECT COALESCE(group_concat(task_class || ':' || samples || ':' || rtf_milli, ','), '') FROM engine_rtf`)
			if c.want == "" {
				if rows != "" {
					t.Fatalf("engine_rtf = %q, want nothing learned", rows)
				}
				return
			}
			// generate 3.5 s + save 0.5 s - load 1.0 s = 3.0 s of work for 4 s of audio
			if want := fmt.Sprintf("%s:1:%d", c.want, spendObserved(3000, 4000)); rows != want {
				t.Fatalf("engine_rtf = %q, want %q", rows, want)
			}
		})
	}
}

func TestIngest_ACEStep_NoLearningWithoutTimings(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.runACE(job, b.flac(60), func(o *provider.Output) { o.Timings = nil })
	b.wantState(job, StateCompleted)
	if n := b.count(`SELECT COUNT(*) FROM engine_rtf`); n != 0 {
		t.Errorf("engine_rtf has %d rows for a take without timings", n)
	}
}

// TestIngest_DurationMismatchIsEventOnly: a length off the plan is a job event, never a failure.
func TestIngest_DurationMismatchIsEventOnly(t *testing.T) {
	b := newACERig(t, true, aceEngines())
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil))) // plans 10.0 s; the synthetic render is 4 s
	b.runACE(job, b.flac(61), nil)
	b.wantState(job, StateCompleted)
	if !b.hasEvent(job, EvDurationMismatch) {
		t.Fatalf("no %s event: %v", EvDurationMismatch, b.events(job))
	}
	if got := b.str(`SELECT detail_json FROM job_events WHERE job_id = ? AND event = ?`, job, EvDurationMismatch); !strings.Contains(got, `"expected_s":10`) || strings.Contains(got, "http") {
		t.Errorf("event detail %q", got)
	}
}

func spendObserved(e2eMS, audioMS int64) int { return spend.ObservedRTF(e2eMS, audioMS) }

// fakeMinter mints a placeholder link for every input a request names; the real one has its own tests.
type fakeMinter struct{}

func (fakeMinter) MintInputs(_ context.Context, request json.RawMessage, _ time.Time) (map[string]provider.InputRef, error) {
	refs, ok := parseInputRefs(request)
	if !ok {
		return nil, nil
	}
	out := map[string]provider.InputRef{}
	link := func(sha string) provider.InputRef {
		return provider.InputRef{URL: testPublicURL + "/media/" + sha + ".flac?exp=1&sig=x", SHA256: sha, Bytes: 1000}
	}
	if refs.Src != nil {
		out[provider.InputSrcAudio] = link(refs.Src.MasterSHA256)
	}
	if refs.Ref != nil {
		out[provider.InputReferenceAudio] = link(refs.Ref.ClipSHA256)
	}
	return out, nil
}

// TestTakeHook_ACEStep: M5's take hook (C255) fires for an ACE-Step take exactly as for a yue2 one, inside the take
// transaction and once after the commit; a failing hook still rolls the whole take back (next test).
func TestTakeHook_ACEStep(t *testing.T) {
	h := &recHook{}
	b := newACERig(t, true, aceEngines(), func(o *BridgeOptions) { o.TakeHook = h })
	h.b = b
	job := b.seedACE(b.aceRecipe(aceRequest(t, nil)))
	b.runACE(job, b.flac(70), nil)
	b.wantState(job, StateCompleted)
	track := b.job(job).TrackID
	if len(h.tx) != 1 || len(h.commit) != 1 || h.tx[0].TrackID != track || h.commit[0].JobID != job {
		t.Fatalf("hook calls: tx %+v commit %+v", h.tx, h.commit)
	}
	if h.seenInTx != 1 || h.seenAfter != 1 {
		t.Errorf("tracks visible: in the transaction %d, after the commit %d", h.seenInTx, h.seenAfter)
	}
}

func TestTakeHook_ACEStep_ErrorRollsBack(t *testing.T) {
	fh := &recHook{txErr: errors.New("hook boom")}
	c := newACERig(t, true, aceEngines(), func(o *BridgeOptions) { o.TakeHook = fh })
	fh.b = c
	job2 := c.seedACE(c.aceRecipe(aceRequest(t, nil)))
	jr, ar, files, out := c.captureACE(job2, c.flac(71))
	_, err := c.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	var ing *IngestFailedError
	if !errors.As(err, &ing) || ing.Code != "DB_INSERT" {
		t.Fatalf("err = %v, want DB_INSERT", err)
	}
	if n := c.count(`SELECT COUNT(*) FROM tracks`) + c.count(`SELECT COUNT(*) FROM engine_rtf`); n != 0 {
		t.Errorf("a failing hook left %d rows (track or engine_rtf)", n)
	}
}

// captureACE is capture for the ACE-Step artifact set.
func (b *bridgeRig) captureACE(job string, flac []byte) (JobRow, AttemptRow, []upload.Received, provider.Output) {
	b.t.Helper()
	stub := &stubIngest{st: b.st, block: make(chan struct{}), entered: make(chan struct{}, 1)}
	b.run.o.Ingest = stub
	b.t.Cleanup(func() {
		stub.mu.Lock()
		stub.err = errors.New("released by the test")
		stub.mu.Unlock()
		close(stub.block)
		_ = b.run.Drain(context.Background())
		b.run.o.Ingest = b.bridge
	})
	spec := b.specOf(job)
	out := b.aceDeliver(spec, flac, nil)
	b.setStatus(b.refOf(job, 1), completed(out, 5000))
	for range 8 {
		if _, err := b.run.Step(b.t.Context()); err != nil {
			b.t.Fatal(err)
		}
		select {
		case <-stub.entered:
		case <-time.After(50 * time.Millisecond):
			b.advance(3e9)
			continue
		}
		break
	}
	stub.mu.Lock()
	jr, ar, files, o := stub.lastJob, stub.lastAtt, append([]upload.Received(nil), stub.last...), stub.lastOut
	stub.mu.Unlock()
	if jr.ID == "" {
		b.t.Fatal("the job never reached the ingester")
	}
	return jr, ar, files, o
}
