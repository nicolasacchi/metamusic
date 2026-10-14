package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

const (
	bridgeLyrics = "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away"
	bridgeABC    = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"
)

// bridgeRig is a runner rig whose Ingester is the real bridge over a real data directory.
type bridgeRig struct {
	*rig
	dataDir string
	tools   mediatool.Tools
	pipe    *ingest.Pipeline
	lib     *library.Service
	bridge  *IngestBridge
	recipes int
}

// newBridgeRig builds the rig; heavy needs the media tools (skipped under -short) and a real analyzer.
func newBridgeRig(t *testing.T, heavy bool, mods ...func(*BridgeOptions)) *bridgeRig {
	t.Helper()
	if heavy {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
	}
	t.Parallel()
	dataDir := t.TempDir()
	r := newRigAt(t, filepath.Join(t.TempDir(), "s.db"), dataDir, true)
	return attachBridge(t, r, heavy, mods...)
}

// attachBridge makes the real bridge the Ingester of an existing rig.
func attachBridge(t *testing.T, r *rig, heavy bool, mods ...func(*BridgeOptions)) *bridgeRig {
	t.Helper()
	var tools mediatool.Tools
	if heavy {
		tools = testtools.Tools()
	}
	dataDir := r.dataDir()
	b := &bridgeRig{rig: r, dataDir: dataDir, tools: tools}
	b.lib = library.New(r.st, r.data, r.clk.Now)
	var an *ffmpeg.FFmpeg
	if heavy {
		var err error
		if an, err = ffmpeg.New(t.Context(), tools); err != nil {
			t.Fatal(err)
		}
		b.pipe = ingest.New(r.data, dataDir, tools, an, r.clk.Now)
	} else {
		b.pipe = ingest.New(r.data, dataDir, tools, nil, r.clk.Now)
	}
	eo := ears.Options{Store: r.st, Library: b.lib, Data: r.data, DataDir: dataDir, Analyzer: earstest.NewTierA(), Now: r.clk.Now}
	o := BridgeOptions{Data: r.data, Pipeline: b.pipe, Library: b.lib, Ears: ears.New(eo), Spend: r.sp, Uploads: r.up, Store: r.st, Clock: r.clk, NewID: r.newID}
	for _, m := range mods {
		m(&o)
	}
	b.bridge = NewIngestBridge(o)
	r.run.o.Ingest = b.bridge
	return b
}

// seed seeds a batch whose jobs have no mother unless the spec names one: the operator is compose_new.
func (b *bridgeRig) seed(sp batchSpec) seeded {
	if sp.operator == "" && sp.source == "" {
		sp.operator = "compose_new"
	}
	return b.seedBatch(sp)
}

// newRecipe inserts a yue2 recipe with lyrics that align with bridgeABC and returns its id.
func (b *bridgeRig) newRecipe(style string) string {
	b.t.Helper()
	b.recipes++
	id := fmt.Sprintf("01900000-0000-7000-8000-0000000f%04d", b.recipes)
	req := fmt.Sprintf(`{"cot":"full","id":"syn-b%d","lyrics":%q,"seed":%d,"style":%q}`, b.recipes, bridgeLyrics, 100+b.recipes, style)
	b.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2',?,?,?,?,?,'full',?,?,'agent:t1',?)`, id, fmt.Sprintf("syn-b%d", b.recipes), req, style, bridgeLyrics, 100+b.recipes,
		"hash-"+id, "core-"+id, store.FormatTime(t0))
	return id
}

// flac renders a distinct synthetic FLAC (the YuE2 metadata shape).
func (b *bridgeRig) flac(variant int) []byte {
	b.t.Helper()
	dst := filepath.Join(b.t.TempDir(), "a.flac")
	if err := synth.FLACUncalibrated(b.t.Context(), b.tools, dst, synth.Tone{Seconds: 4, Variant: variant}, synth.StyleYuE2); err != nil {
		b.t.Fatal(err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		b.t.Fatal(err)
	}
	return raw
}

func resultJSON(abc, sem bool) []byte {
	return []byte(fmt.Sprintf(`{"truncated":{"abc":%t,"semantic":%t}}`, abc, sem))
}

// specOf steps the runner until the job is submitted and returns its TakeSpec.
func (b *bridgeRig) specOf(job string) provider.TakeSpec {
	b.t.Helper()
	for range 8 {
		if b.state(job) == StateStarting {
			break
		}
		b.step()
		b.advance(3e9)
	}
	b.wantState(job, StateStarting)
	for _, sp := range b.be.Specs() {
		if sp.JobID == job {
			return sp
		}
	}
	b.t.Fatalf("job %s was never submitted", job)
	return provider.TakeSpec{}
}

// run drives one seeded job through the real bridge to a terminal state and returns the worker output.
func (b *bridgeRig) run1(job string, flac []byte, trunc [2]bool, mutate func(*deliverOpts)) provider.Output {
	b.t.Helper()
	spec := b.specOf(job)
	o := deliverOpts{truncated: trunc, allOptional: true, e2e: 71, bodies: map[string][]byte{
		"audio.flac": flac, "result.json": resultJSON(trunc[0], trunc[1]), "score.abc": []byte(bridgeABC),
		"request.json": []byte(`{"id":"x"}`), "config.json": []byte(`{}`),
	}}
	if mutate != nil {
		mutate(&o)
	}
	out := b.deliver(spec, o)
	b.setStatus(b.refOf(job, 1), completed(out, 71000))
	for range 12 {
		b.step()
		if IsTerminal(b.state(job)) {
			break
		}
		b.advance(3e9)
	}
	return out
}

func TestIngestBridge_TakeTransaction(t *testing.T) {
	b := newBridgeRig(t, true)
	mother := b.newRecipe("pop, dream, 120 bpm")
	draftRecipe := b.newRecipe("pop, dream, 120 bpm")
	// a backlog draft the job fulfils
	b.exec(`INSERT INTO recipe_drafts(id, origin, source_recipe_id, request_id, request_json, status, created_at, updated_at)
		VALUES('draft-1','import_backlog',?,'syn-b2','{"id":"syn-b2"}','backlog',?,?)`, draftRecipe, store.FormatTime(t0), store.FormatTime(t0))
	mut := `{"operator":"compose_new","params":{"mode":"new_seed","seed_policy":"auto","n_seeds":1,"via":null},"recipe_patch":{"style":"pop"},"rationale":"try another seed","principal_id":"agent:t1"}`
	s := b.seed(batchSpec{recipe: draftRecipe, draft: "draft-1", mutation: mut})
	job := s.Jobs[0]
	_ = mother
	out := b.run1(job, b.flac(1), [2]bool{}, nil)

	b.wantState(job, StateCompleted)
	j := b.job(job)
	if j.Outcome != "clean" || j.TrackID == "" {
		t.Fatalf("job %+v", j)
	}
	track := j.TrackID
	if got := b.str(`SELECT origin || '|' || status_set_by || '|' || import_rule || '|' || lossy || '|' || status || '|' || job_id FROM tracks WHERE id = ?`, track); got != "job|system:job|job|0|draft|"+job {
		t.Errorf("track row %q", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM tracks WHERE id = ? AND parent_track_id IS NULL AND root_track_id = id AND generation = 0`, track); got != 1 {
		t.Error("a root take is its own root at generation 0")
	}
	// Operator Ledger row
	if got := b.str(`SELECT operator || '|' || job_id || '|' || principal_id || '|' || rationale || '|' || recipe_patch_json FROM mutations WHERE child_track_id = ?`, track); got != `compose_new|`+job+`|agent:t1|try another seed|{"style":"pop"}` {
		t.Errorf("mutation row %q", got)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(b.str(`SELECT params_json FROM mutations WHERE child_track_id = ?`, track)), &params); err != nil || params["job_id"] != job || params["mode"] != "new_seed" {
		t.Errorf("params_json %v %v", params, err)
	}
	// score and sections
	if got := b.str(`SELECT source || '|' || aligned FROM scores WHERE track_id = ?`, track); got != "job|1" {
		t.Errorf("score row %q", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'plan'`, track); got != 3 {
		t.Errorf("plan sections %d", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM sections WHERE track_id = ? AND source = 'lyrics'`, track); got != 3 {
		t.Errorf("lyrics sections %d", got)
	}
	// environment, RTF, draft
	env := b.str(`SELECT env_id FROM tracks WHERE id = ?`, track)
	if jobEnv := b.str(`SELECT env_id FROM jobs WHERE id = ?`, job); env == "" || env != jobEnv {
		t.Errorf("env id %q vs job %q", env, jobEnv)
	}
	if got := b.str(`SELECT gpu_class || '|' || backend || '|' || image_digest FROM env_fingerprints WHERE env_id = ?`, env); got != "A5000|fake|build:test" {
		t.Errorf("env row %q", got)
	}
	if got := b.str(`SELECT detail_json FROM env_fingerprints WHERE env_id = ?`, env); !strings.Contains(got, `"gpu_name":"NVIDIA RTX A5000"`) {
		t.Errorf("env detail %q", got)
	}
	if got := b.count(`SELECT samples FROM rtf_stats WHERE gpu_class = 'A5000'`); got != 1 {
		t.Errorf("rtf samples %d", got)
	}
	if got := b.str(`SELECT status || '|' || fulfilled_by_track_id || '|' || updated_by FROM recipe_drafts WHERE id = 'draft-1'`); got != "fulfilled|"+track+"|system:job" {
		t.Errorf("draft %q", got)
	}
	// uploads consumed, files gone, kept artifacts present
	if got := b.count(`SELECT COUNT(*) FROM uploads WHERE job_id = ? AND state = 'received'`, job); got != 0 {
		t.Errorf("%d uploads still received", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM uploads WHERE job_id = ? AND state = 'consumed'`, job); got < 5 {
		t.Errorf("%d uploads consumed", got)
	}
	ents, _ := os.ReadDir(filepath.Join(b.dataDir, "incoming", "uploads"))
	for _, e := range ents {
		if b.count(`SELECT COUNT(*) FROM uploads WHERE id = ? AND state = 'consumed'`, e.Name()) > 0 {
			t.Errorf("consumed upload file %s remains", e.Name())
		}
	}
	for _, n := range []string{"result.json", "request.json", "config.json", "mm-result.json", "score.abc"} {
		if got := b.count(`SELECT COUNT(*) FROM artifacts WHERE track_id = ? AND name = ?`, track, n); got != 1 {
			t.Errorf("artifact %s: %d rows", n, got)
		}
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, "takes", track, "score.abc")); err != nil {
		t.Errorf("score.abc kept: %v", err)
	}
	// Ears Tier A ran after the commit
	if got := b.count(`SELECT COUNT(*) FROM analyses WHERE track_id = ? AND analyzer IN ('silence','clipping')`, track); got < 2 {
		t.Errorf("tier A rows %d", got)
	}
	if out.Env.GPUName == "" {
		t.Fatal("rig output has no env")
	}
	if b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job) != 1 {
		t.Error("exactly one track per job")
	}
}

func TestIngestBridge_TruncationOutcome(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	b.run1(job, b.flac(2), [2]bool{false, true}, nil)
	b.wantState(job, StateCompleted)
	j := b.job(job)
	if j.Outcome != "truncated" {
		t.Errorf("outcome %q", j.Outcome)
	}
	if got := b.str(`SELECT truncated_abc || '|' || truncated_semantic FROM jobs WHERE id = ?`, job); got != "0|1" {
		t.Errorf("job truncation %q", got)
	}
	if got := b.str(`SELECT truncated_abc || '|' || truncated_semantic FROM tracks WHERE id = ?`, j.TrackID); got != "0|1" {
		t.Errorf("track truncation %q", got)
	}
}

// received writes the given artifact bodies as received uploads of a synthetic attempt and returns them.
func (b *bridgeRig) received(bodies map[string][]byte) (files []upload.Received, out provider.Output) {
	b.t.Helper()
	if err := b.data.MkdirAll("incoming/uploads", 0o755); err != nil {
		b.t.Fatal(err)
	}
	out = provider.Output{Contract: provider.Contract, TakeID: "job-x", AttemptID: "att-x", Status: "ok", Truncated: map[string]bool{"abc": false, "semantic": false},
		Artifacts: map[string]provider.ArtifactDigest{}}
	n := json.Number("4")
	out.AudioSeconds = &n
	for _, name := range sortedKeys(bodies) {
		id := "up-" + strings.ReplaceAll(name, ".", "-")
		if err := b.data.WriteFile("incoming/uploads/"+id, bodies[name], 0o400); err != nil {
			b.t.Fatal(err)
		}
		sum := sha256hex(bodies[name])
		files = append(files, upload.Received{UploadID: id, Artifact: name, SHA256: sum, Bytes: int64(len(bodies[name])), Path: "incoming/uploads/" + id})
		if name != provider.ResultArtifact {
			out.Artifacts[name] = provider.ArtifactDigest{SHA256: sum, Bytes: int64(len(bodies[name]))}
		}
	}
	return files, out
}

func TestIngestBridge_WorkerContractViolations(t *testing.T) {
	good := func() map[string][]byte {
		return map[string][]byte{"audio.flac": []byte("not really a flac"), "request.json": []byte(`{}`), "config.json": []byte(`{}`),
			"result.json": resultJSON(false, false), "mm-result.json": []byte(`{}`)}
	}
	for _, c := range []struct {
		name   string
		mutate func(files []upload.Received, out *provider.Output, bodies map[string][]byte)
		bodies func(map[string][]byte)
		want   string
	}{
		{name: "truncated not a dict", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) {
			out.Truncated = map[string]bool{"abc": false}
		}, want: "WORKER_CONTRACT"},
		{name: "wrong take", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) { out.TakeID = "other" }, want: "WORKER_CONTRACT"},
		{name: "error status", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) { out.Status = "error" }, want: "WORKER_CONTRACT"},
		{name: "no audio seconds", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) { out.AudioSeconds = nil }, want: "WORKER_CONTRACT"},
		{name: "sha differs from upload", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) {
			d := out.Artifacts["audio.flac"]
			d.SHA256 = strings.Repeat("0", 64)
			out.Artifacts["audio.flac"] = d
		}, want: "ARTIFACT_MISMATCH"},
		{name: "size differs from upload", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) {
			d := out.Artifacts["config.json"]
			d.Bytes++
			out.Artifacts["config.json"] = d
		}, want: "ARTIFACT_MISMATCH"},
		{name: "required artifact not received", bodies: func(m map[string][]byte) { delete(m, "config.json") }, want: "ARTIFACT_MISSING"},
		{name: "required artifact not reported", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) {
			delete(out.Artifacts, "request.json")
		}, want: "ARTIFACT_MISSING"},
		{name: "reported artifact not received", mutate: func(_ []upload.Received, out *provider.Output, _ map[string][]byte) {
			out.Artifacts["plan.json"] = provider.ArtifactDigest{SHA256: strings.Repeat("a", 64), Bytes: 3}
		}, want: "ARTIFACT_MISSING"},
		{name: "no mm-result", bodies: func(m map[string][]byte) { delete(m, "mm-result.json") }, want: "ARTIFACT_MISSING"},
		{name: "result.json truncation differs", bodies: func(m map[string][]byte) { m["result.json"] = resultJSON(true, false) }, want: "WORKER_CONTRACT"},
		{name: "result.json truncated is not a dict", bodies: func(m map[string][]byte) { m["result.json"] = []byte(`{"truncated":true}`) }, want: "WORKER_CONTRACT"},
		{name: "result.json truncated has extra keys", bodies: func(m map[string][]byte) {
			m["result.json"] = []byte(`{"truncated":{"abc":false,"semantic":false,"x":1}}`)
		}, want: "WORKER_CONTRACT"},
		{name: "result.json is not an object", bodies: func(m map[string][]byte) { m["result.json"] = []byte(`[1]`) }, want: "WORKER_CONTRACT"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBridgeRig(t, false)
			bodies := good()
			if c.bodies != nil {
				c.bodies(bodies)
			}
			files, out := b.received(bodies)
			out.TakeID = "job-x"
			if c.mutate != nil {
				c.mutate(files, &out, bodies)
			}
			_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: testRecipe}, AttemptRow{ID: "att-x"}, files, out)
			var cv *ContractViolation
			if !errors.As(err, &cv) || cv.Code != c.want {
				t.Fatalf("err = %v, want violation %s", err, c.want)
			}
			if strings.Contains(cv.Text, "http") {
				t.Errorf("text %q", cv.Text)
			}
			if n := b.count(`SELECT COUNT(*) FROM tracks`); n != 0 {
				t.Errorf("%d tracks written", n)
			}
		})
	}
}

func TestIngestBridge_Unwired(t *testing.T) {
	b := newBridgeRig(t, false)
	files, out := b.received(map[string][]byte{"audio.flac": []byte("x")})
	if _, err := b.bridge.IngestTake(t.Context(), JobRow{ID: "j"}, AttemptRow{}, files, out); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Errorf("no lease owner: %v", err)
	}
	empty := NewIngestBridge(BridgeOptions{})
	if _, err := empty.IngestTake(WithLeaseOwner(t.Context(), "o"), JobRow{ID: "j"}, AttemptRow{}, files, out); err == nil || !strings.Contains(err.Error(), "wired") {
		t.Errorf("unwired: %v", err)
	}
}

// capture runs the job up to the point where the pool calls the Ingester and returns what it was given.
// The pool stays blocked in the stub (the job stays ingesting under runner-1) until the test ends.
func (b *bridgeRig) capture(job string, flac []byte) (JobRow, AttemptRow, []upload.Received, provider.Output) {
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
	out := b.deliver(spec, deliverOpts{allOptional: true, e2e: 71, bodies: map[string][]byte{
		"audio.flac": flac, "result.json": resultJSON(false, false), "score.abc": []byte(bridgeABC),
		"request.json": []byte(`{"id":"x"}`), "config.json": []byte(`{}`)}})
	b.setStatus(b.refOf(job, 1), completed(out, 71000))
	for range 8 {
		if _, err := b.run.Step(b.t.Context()); err != nil {
			b.t.Fatal(err)
		}
		select {
		case <-stub.entered:
		default:
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

func (b *bridgeRig) tracksOnDisk() []string {
	ents, _ := os.ReadDir(filepath.Join(b.dataDir, "takes"))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func (b *bridgeRig) mastersOnDisk() int {
	ents, _ := os.ReadDir(filepath.Join(b.dataDir, "audio", "masters"))
	return len(ents)
}

func TestIngestBridge_DiscardOnTxError(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(3))
	b.exec(`CREATE TRIGGER t_boom BEFORE INSERT ON tracks BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	var ing *IngestFailedError
	if !errors.As(err, &ing) || ing.Code != "DB_INSERT" {
		t.Fatalf("err = %v", err)
	}
	if got := b.tracksOnDisk(); len(got) != 0 {
		t.Errorf("takes left behind: %v", got)
	}
	if n := b.mastersOnDisk(); n != 0 {
		t.Errorf("%d masters left behind", n)
	}
	if n := b.count(`SELECT COUNT(*) FROM env_fingerprints`); n != 0 {
		t.Errorf("env rows %d: the transaction must roll back as a whole", n)
	}
	if n := b.count(`SELECT COUNT(*) FROM uploads WHERE state = 'consumed'`); n != 0 {
		t.Errorf("uploads consumed: %d", n)
	}
	if _, err := os.Stat(filepath.Join(b.dataDir, files[0].Path)); err != nil {
		t.Errorf("uploaded files must survive for a retry: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(b.dataDir, "incoming")); len(ents) != 1 || ents[0].Name() != "uploads" {
		t.Errorf("incoming = %v: the work dir must be gone", ents)
	}
}

func TestIngestBridge_RetryAfterCrashNewTrackID(t *testing.T) {
	crash := errors.New("simulated crash")
	b := newBridgeRig(t, true, func(o *BridgeOptions) {
		o.Fault = func(p string) error {
			if p == FaultAfterIngest {
				return crash
			}
			return nil
		}
	})
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(4))
	ctx := WithLeaseOwner(t.Context(), "runner-1")
	if _, err := b.bridge.IngestTake(ctx, jr, ar, files, out); !errors.Is(err, crash) {
		t.Fatalf("first attempt: %v", err)
	}
	orphans := b.tracksOnDisk()
	if len(orphans) != 1 || b.count(`SELECT COUNT(*) FROM tracks`) != 0 {
		t.Fatalf("a crash leaves files and no row: %v", orphans)
	}
	b.bridge.o.Fault = nil // the process restarted
	id, err := b.bridge.IngestTake(ctx, jr, ar, files, out)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if id == orphans[0] || id == "" {
		t.Errorf("the retry reused track id %s", id)
	}
	if got := b.tracksOnDisk(); len(got) != 2 {
		t.Errorf("takes dirs %v: the old files stay for library_orphans", got)
	}
	if b.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, orphans[0]) != 0 || b.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id) != 1 {
		t.Error("only the retry's track is a row")
	}
}

func TestIngestBridge_DuplicateAudio(t *testing.T) {
	b := newBridgeRig(t, true)
	rec := b.newRecipe("pop")
	s := b.seed(batchSpec{jobs: 2, recipe: rec, workersMax: 1})
	flac := b.flac(5)
	b.run1(s.Jobs[0], flac, [2]bool{}, nil)
	b.wantState(s.Jobs[0], StateCompleted)
	first := b.job(s.Jobs[0]).TrackID
	b.run1(s.Jobs[1], flac, [2]bool{}, nil)
	b.wantState(s.Jobs[1], StateFailed)
	b.wantCode(s.Jobs[1], CodeDuplicateAudio)
	if got := b.count(`SELECT COUNT(*) FROM tracks`); got != 1 {
		t.Errorf("tracks %d", got)
	}
	var detail string
	if err := b.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? AND event = 'duplicate_audio'`, s.Jobs[1]).Scan(&detail); err != nil || !strings.Contains(detail, first) {
		t.Errorf("duplicate event %q: %v", detail, err)
	}
	if got := b.count(`SELECT COUNT(*) FROM job_attempts WHERE job_id = ? AND settled = 1`, s.Jobs[1]); got != 1 {
		t.Errorf("the render was paid and must settle: %d", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM mutations WHERE job_id = ?`, s.Jobs[1]); got != 0 {
		t.Errorf("mutations %d", got)
	}
	if got := b.tracksOnDisk(); len(got) != 1 {
		t.Errorf("takes dirs %v", got)
	}
	if b.mastersOnDisk() != 1 {
		t.Error("the duplicate's master must be discarded")
	}
}

func TestIngestBridge_AlreadyCommittedCompletesFromTrack(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(6))
	// the bridge commits the take; the process stops before the job row follows
	track, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	if err != nil {
		t.Fatal(err)
	}
	// a new runner adopts the job after the lease expired and completes it from the committed take
	b.advance(2 * LeaseLength)
	b2 := b.second("runner-2", func(o *RunnerOptions) { o.Ingest = b.bridge })
	for range 4 {
		if _, err := b2.Step(t.Context()); err != nil {
			t.Fatal(err)
		}
		_ = b2.Drain(t.Context())
		if IsTerminal(b.state(job)) {
			break
		}
	}
	b.wantState(job, StateCompleted)
	if j := b.job(job); j.TrackID != track || j.Outcome != "clean" {
		t.Errorf("job %+v, want track %s", j, track)
	}
	if got := b.count(`SELECT COUNT(*) FROM tracks WHERE job_id = ?`, job); got != 1 {
		t.Errorf("tracks %d", got)
	}
}

func TestIngestBridge_NeverLossyOrReference(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	b.run1(job, b.flac(7), [2]bool{}, nil)
	track := b.job(job).TrackID
	if got := b.str(`SELECT import_rule || '|' || lossy || '|' || origin FROM tracks WHERE id = ?`, track); got != "job|0|job" {
		t.Errorf("track %q", got)
	}
	// the M2c triggers reject a job take whose mother is a reference track
	lt.FixtureM2c(t, b.st, t0)
	var ref string
	if err := b.st.R.QueryRow(`SELECT id FROM tracks WHERE import_rule = 'reference' AND lossy = 0 LIMIT 1`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	child := "01900000-0000-7000-8000-0000000fffff"
	_, err := b.st.W.Exec(`INSERT INTO tracks(id, slug, title, recipe_id, parent_track_id, root_track_id, generation, status, status_set_by, origin,
		duration_s, sample_rate, bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		import_rule, recorded_at, created_at, job_id) SELECT ?, 'c', 'C', recipe_id, id, root_track_id, generation + 1, 'draft', 'system:job', 'job',
		4, 48000, 24, 2, 192000, 'audio/masters/c.flac', 'm', 1, 's', 'f', 'a', 'job', ?, ?, ? FROM tracks WHERE id = ?`,
		child, store.FormatTime(t0), store.FormatTime(t0), job, ref)
	if err == nil || !strings.Contains(err.Error(), "reference") {
		t.Errorf("a job take under a reference mother was accepted: %v", err)
	}
	_ = sql.ErrNoRows
}

func sha256hex(b []byte) string {
	return fmt.Sprintf("%x", sha256Sum(b))
}

func TestIngestBridge_ChildLineageAndTags(t *testing.T) {
	b := newBridgeRig(t, true)
	root := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	b.run1(root, b.flac(8), [2]bool{}, nil)
	parent := b.job(root).TrackID
	b.exec(`INSERT INTO tags(id, name, created_at) VALUES(1, 'keeper', ?)`, store.FormatTime(t0))
	b.exec(`INSERT INTO track_tags(track_id, tag_id, source, principal_id, created_at, updated_at) VALUES(?,1,'human','human:h1',?,?)`, parent, store.FormatTime(t0), store.FormatTime(t0))

	child := b.seed(batchSpec{recipe: b.newRecipe("pop, slow"), source: parent, operator: "reroll_seed",
		mutation: `{"operator":"reroll_seed","params":{"mode":"new_seed"},"recipe_patch":null,"rationale":null,"principal_id":"human:h1"}`}).Jobs[0]
	b.run1(child, b.flac(9), [2]bool{}, nil)
	b.wantState(child, StateCompleted)
	ct := b.job(child).TrackID
	if got := b.str(`SELECT parent_track_id || '|' || root_track_id || '|' || generation FROM tracks WHERE id = ?`, ct); got != parent+"|"+parent+"|1" {
		t.Errorf("lineage %q", got)
	}
	if got := b.str(`SELECT parent_track_id || '|' || operator || '|' || COALESCE(recipe_patch_json, 'NULL') || '|' || principal_id FROM mutations WHERE child_track_id = ?`, ct); got != parent+"|reroll_seed|NULL|human:h1" {
		t.Errorf("mutation %q", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM track_tags WHERE track_id = ? AND tag_id = 1 AND source = 'inherited' AND inherited_from = ?`, ct, parent); got != 1 {
		t.Errorf("inherited tag rows %d", got)
	}
	if got := b.count(`SELECT COUNT(*) FROM track_style_terms WHERE track_id = ?`, ct); got < 2 {
		t.Errorf("style terms %d", got)
	}
}

// TestIngestBridge_OwnTakeIsNotADuplicate: a retry after the take committed but before the job did finds the
// job's own track under the same audio identity and answers with it (no second track, no DUPLICATE_AUDIO).
func TestIngestBridge_OwnTakeIsNotADuplicate(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(12))
	bodies := map[string][]byte{}
	for _, f := range files {
		raw, err := b.data.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		bodies[f.Path] = raw
	}
	ctx := WithLeaseOwner(t.Context(), "runner-1")
	first, err := b.bridge.IngestTake(ctx, jr, ar, files, out)
	if err != nil {
		t.Fatal(err)
	}
	for p, raw := range bodies { // the consumed uploads are gone: put the same files back
		if err := b.data.WriteFile(p, raw, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	again, err := b.bridge.IngestTake(ctx, jr, ar, files, out)
	if err != nil || again != first {
		t.Fatalf("second call: %q %v, want %q", again, err, first)
	}
	if n := b.count(`SELECT COUNT(*) FROM tracks`); n != 1 {
		t.Errorf("tracks %d", n)
	}
	if b.mastersOnDisk() != 1 || len(b.tracksOnDisk()) != 1 {
		t.Errorf("the second ingest must leave nothing: %d masters, %v takes", b.mastersOnDisk(), b.tracksOnDisk())
	}
	// the files of a take already ingested but vanished from disk are an ingest failure, not a panic
	for p := range bodies {
		_ = b.data.Remove(p)
	}
	var ing *IngestFailedError
	if _, err := b.bridge.IngestTake(ctx, jr, ar, files, out); !errors.As(err, &ing) || ing.Code != "SOURCE_CHANGED" {
		t.Errorf("files gone: %v", err)
	}
}

func TestIngestBridge_RefusalsAfterTheAudioIsFrozen(t *testing.T) {
	b := newBridgeRig(t, true)
	mk := func(name string, mut func(job *JobRow)) {
		t.Run(name, func(t *testing.T) {
			// each case needs a job of its own that is ingesting
			c := newBridgeRig(t, true)
			job := c.seed(batchSpec{recipe: c.newRecipe("pop")}).Jobs[0]
			jr, ar, files, out := c.capture(job, c.flac(20+len(name)))
			mut(&jr)
			_, err := c.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
			if err == nil {
				t.Fatal("accepted")
			}
			if n := c.count(`SELECT COUNT(*) FROM tracks`); n != 0 || len(c.tracksOnDisk()) != 0 || c.mastersOnDisk() != 0 {
				t.Errorf("left behind: %d tracks %v %d masters", n, c.tracksOnDisk(), c.mastersOnDisk())
			}
		})
	}
	_ = b
	mk("broken mutation", func(j *JobRow) { j.MutationJSON = "{" })
	mk("unknown mother", func(j *JobRow) { x := "no-such-track"; j.SourceTrackID = &x })
	mk("root mutation without a mother", func(j *JobRow) { j.MutationJSON = `{"operator":"reroll_seed"}`; j.Operator = "reroll_seed" })
}

func TestIngestBridge_StaleJobAndLease(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(30))
	// another runner owns the lease: the guard refuses
	if _, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-9"), jr, ar, files, out); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("foreign owner: %v", err)
	}
	// the lease is ours but the job moved to another holder: stale, not lost
	b.exec(`UPDATE jobs SET lease_owner = 'someone-else' WHERE id = ?`, job)
	if _, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out); !errors.Is(err, errStale) {
		t.Errorf("job leased elsewhere: %v", err)
	}
	if b.count(`SELECT COUNT(*) FROM tracks`) != 0 || len(b.tracksOnDisk()) != 0 || b.mastersOnDisk() != 0 {
		t.Error("a refused take left something behind")
	}
}

func TestIngestBridge_BadAudioAndOddTitles(t *testing.T) {
	b := newBridgeRig(t, true)
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(31))
	// replace the uploaded audio with bytes that are not FLAC (same sha as reported, so verification passes)
	var audioPath string
	for i, f := range files {
		if f.Artifact == "audio.flac" {
			audioPath = f.Path
			junk := []byte("this is not a flac file at all")
			if err := b.data.Remove(f.Path); err != nil {
				t.Fatal(err)
			}
			if err := b.data.WriteFile(f.Path, junk, 0o400); err != nil {
				t.Fatal(err)
			}
			files[i].SHA256, files[i].Bytes = sha256hex(junk), int64(len(junk))
			out.Artifacts["audio.flac"] = provider.ArtifactDigest{SHA256: files[i].SHA256, Bytes: files[i].Bytes}
		}
	}
	_ = audioPath
	var ing *IngestFailedError
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	if !errors.As(err, &ing) || ing.Code == "" {
		t.Fatalf("bad audio: %v", err)
	}
	if len(b.tracksOnDisk()) != 0 || b.mastersOnDisk() != 0 || b.count(`SELECT COUNT(*) FROM tracks`) != 0 {
		t.Error("a failed ingest left something behind")
	}
}

func TestIngestBridge_SlugFallbackAndEarsWarning(t *testing.T) {
	b := newBridgeRig(t, true, func(o *BridgeOptions) { o.Ears = ears.New(ears.Options{}) }) // an unwired Ears fails: a warning, not a failure
	job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
	jr, ar, files, out := b.capture(job, b.flac(32))
	jr.Title = "!!!" // a title without any slug character
	id, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	if got := b.str(`SELECT slug FROM tracks WHERE id = ?`, id); got != "syn_b1" {
		t.Errorf("slug %q, want the request id's", got)
	}
	if n := b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'ears'`, job); n != 1 {
		t.Errorf("ears warning events %d", n)
	}
}

func TestIngestBridge_RecipeMissing(t *testing.T) {
	b := newBridgeRig(t, false)
	files, out := b.received(map[string][]byte{"audio.flac": []byte("x"), "request.json": []byte(`{}`), "config.json": []byte(`{}`),
		"result.json": resultJSON(false, false), "mm-result.json": []byte(`{}`)})
	out.TakeID = "job-x"
	if _, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: "no-recipe"}, AttemptRow{}, files, out); err == nil ||
		!strings.Contains(err.Error(), "recipe") {
		t.Errorf("missing recipe: %v", err)
	}
}

func TestNumberOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   *json.Number
		want float64
		ok   bool
	}{{nil, 0, false}, {numPtr("71.5"), 71.5, true}, {numPtr("-1"), 0, false}, {numPtr("x"), 0, false}} {
		if got, ok := numberOf(c.in); ok != c.ok || ok && got != c.want {
			t.Errorf("%v: %v %v", c.in, got, ok)
		}
	}
}

func numPtr(s string) *json.Number { n := json.Number(s); return &n }
