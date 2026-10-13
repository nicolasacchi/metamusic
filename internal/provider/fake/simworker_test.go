package fake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func inputFor(t testing.TB, ts provider.TakeSpec) []byte {
	t.Helper()
	raw, err := provider.BuildWorkerInput(ts)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func loadOutputSchema(t testing.TB) *jsonschema.Resolved {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "worker", "src", "mmworker", "schema", "mm-worker-1.output.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func validateOutput(t testing.TB, rs *jsonschema.Resolved, raw []byte) {
	t.Helper()
	var inst any
	if err := json.Unmarshal(raw, &inst); err != nil {
		t.Fatal(err)
	}
	if err := rs.Validate(inst); err != nil {
		t.Errorf("output does not validate against the C73.5 schema: %v\n%s", err, raw)
	}
}

func realSim(t testing.TB, up fake.Uploader) *fake.SimWorker {
	t.Helper()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	return fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, Tools: testtools.Tools(), TempDir: t.TempDir()})
}

func TestSimWorker_ArtifactContract(t *testing.T) {
	t.Parallel()
	rs := loadOutputSchema(t)
	for _, tc := range []struct {
		cot  string
		want []string
	}{
		{"full", []string{"request.json", "config.json", "result.json", "score.abc", "plan.json", "plan_manifest.json", "semantic.npy", "audio.flac", "mm-result.json"}},
		{"off", []string{"request.json", "config.json", "result.json", "semantic.npy", "audio.flac", "mm-result.json"}},
	} {
		t.Run(tc.cot, func(t *testing.T) {
			t.Parallel()
			up := &recUploader{}
			sim := realSim(t, up)
			ts := specCot("job-"+tc.cot, 1, "pop, 100 BPM", tc.cot, 186)
			res := sim.Run(context.Background(), inputFor(t, ts), "ref-1")
			if res.Failed || res.Output.Status != "ok" {
				t.Fatalf("%+v", res)
			}
			if got := up.names(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("upload order\n got %v\nwant %v", got, tc.want)
			}
			// request.json: YuE2 asdict order, explicit nulls
			var keys []string
			dec := json.NewDecoder(bytes.NewReader(up.body("request.json")))
			if _, err := dec.Token(); err != nil {
				t.Fatal(err)
			}
			for dec.More() {
				k, _ := dec.Token()
				keys = append(keys, k.(string))
				var v json.RawMessage
				_ = dec.Decode(&v)
				if k == "abc" && string(v) != "null" {
					t.Errorf("abc %s", v)
				}
			}
			if strings.Join(keys, ",") != "style,lyrics,cot,seed,abc,cfg_scale,id" {
				t.Errorf("request.json key order %v", keys)
			}
			// result.json: truncated is a dict, timing and audio seconds present
			var rj struct {
				Truncated    map[string]bool    `json:"truncated"`
				AudioSeconds float64            `json:"audio_seconds"`
				Timing       map[string]float64 `json:"timing"`
			}
			if err := json.Unmarshal(up.body("result.json"), &rj); err != nil {
				t.Fatal(err)
			}
			if len(rj.Truncated) != 2 || rj.AudioSeconds != 6 || rj.Timing["e2e_seconds"] <= 0 {
				t.Errorf("result.json %+v", rj)
			}
			// score.abc parses and aligns with the lyrics
			if tc.cot != "off" {
				sc, err := yue2.ParseScore(up.body("score.abc"))
				if err != nil || sc.SecondsPerBar <= 0 || sc.Meter != "4/4" || sc.BPM != 120 {
					t.Fatalf("score %+v %v", sc, err)
				}
				var req struct {
					Lyrics string `json:"lyrics"`
				}
				_ = json.Unmarshal(ts.Request, &req)
				if _, ok := yue2.AlignSections(yue2.LyricsSections(req.Lyrics), sc); !ok {
					t.Errorf("score does not align with the lyrics: %+v", sc.Sections)
				}
			}
			// semantic.npy reads with M2b's reader
			arr, err := npy.Read(bytes.NewReader(up.body("semantic.npy")), 64<<20)
			if err != nil || len(arr.Data) == 0 {
				t.Errorf("npy %v", err)
			}
			// audio is a YuE2-shaped FLAC of 6 s at 48 kHz / 24 bit stereo
			audio := up.body("audio.flac")
			info, err := flacmeta.Read(bytes.NewReader(audio), int64(len(audio)))
			if err != nil || info.StreamInfo.SampleRate != 48000 || info.StreamInfo.BitsPerSample != 24 || info.StreamInfo.Channels != 2 || info.StreamInfo.TotalSamples != 6*48000 {
				t.Errorf("flac %+v %v", info.StreamInfo, err)
			}
			// output: validates, lists everything but mm-result.json, digests match the uploads
			last := up.body("mm-result.json")
			validateOutput(t, rs, last)
			validateOutput(t, rs, res.Raw)
			if _, listed := res.Output.Artifacts["mm-result.json"]; listed || len(res.Output.Artifacts) != len(tc.want)-1 {
				t.Errorf("artifacts %v", res.Output.Artifacts)
			}
			up.mu.Lock()
			for _, p := range up.puts {
				if p.Digest != digestOf(p.Body) {
					t.Errorf("%s: Content-Digest %s", p.Name, p.Digest)
				}
				if p.Name != "mm-result.json" && int64(len(p.Body)) != res.Output.Artifacts[p.Name].Bytes {
					t.Errorf("%s: bytes", p.Name)
				}
			}
			up.mu.Unlock()
			if res.Output.Env.Engine != "yue2" || res.Output.Env.Weights["m-a-p/YuE2-3B"] != yue2.Weights["m-a-p/YuE2-3B"] {
				t.Errorf("env %+v", res.Output.Env)
			}
		})
	}
}

func audioIdentity(t testing.TB, audio []byte) string {
	t.Helper()
	r := bytes.NewReader(audio)
	info, err := flacmeta.Read(r, int64(len(audio)))
	if err != nil {
		t.Fatal(err)
	}
	sha, err := flacmeta.FramesSHA256(r, int64(len(audio)), info.FramesOffset)
	if err != nil {
		t.Fatal(err)
	}
	return flacmeta.AudioID(sha, info.StreamInfo.TotalSamples)
}

func TestSimWorker_DistinctAudioPerTake(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	var mu sync.Mutex
	var wg sync.WaitGroup
	ids := map[string]string{}
	for _, tc := range []struct{ name, style string }{{"a", "pop"}, {"b", "pop"}, {"c", "pop fake:same_audio"}, {"d", "pop fake:same_audio"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			up := &recUploader{}
			sim := realSim(t, up)
			// the same seed, different attempts
			res := sim.Run(context.Background(), inputFor(t, specCot("job-"+tc.name, 1, tc.style, "off", 186)), "ref")
			if res.Failed {
				t.Errorf("%s: %+v", tc.name, res)
				return
			}
			id := audioIdentity(t, up.body("audio.flac"))
			mu.Lock()
			ids[tc.name] = id
			mu.Unlock()
		}()
	}
	wg.Wait()
	if ids["a"] == ids["b"] {
		t.Error("two attempts with the same seed must have different audio identities")
	}
	if ids["c"] != ids["d"] {
		t.Error("fake:same_audio must give identical audio")
	}
	if ids["c"] == ids["a"] {
		t.Error("the fixed tone is not an attempt tone")
	}
}

func TestSimWorker_StartHandshake(t *testing.T) {
	t.Parallel()
	t.Run("410 means no render", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{startCode: func(int) int { return 410 }}
		sim := newSim(t, up)
		res := sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref-1")
		if !res.Failed || res.Output.Error == nil || res.Output.Error.Code != "ATTEMPT_CLOSED" {
			t.Fatalf("%+v", res)
		}
		if len(up.names()) != 0 {
			t.Errorf("uploads after a closed attempt: %v", up.names())
		}
		if up.starts[0].Ref != "ref-1" {
			t.Errorf("the start body carries the job id: %+v", up.starts)
		}
		var wrapped struct{ Error string }
		if err := json.Unmarshal(res.Raw, &wrapped); err != nil || !strings.HasPrefix(wrapped.Error, "ATTEMPT_CLOSED: ") {
			t.Errorf("job return %s", res.Raw)
		}
	})

	t.Run("re-polls every 15 s while rendering", func(t *testing.T) {
		t.Parallel()
		clk := clocktest.NewManual(t0)
		up := &recUploader{}
		sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Clock: clk, RenderDuration: 40 * time.Second})
		done := make(chan fake.Result, 1)
		go func() { done <- sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref") }()
		r, ok := drive(clk, done, 15*time.Second)
		if !ok {
			t.Fatal("worker did not finish")
		}
		if r.Failed {
			t.Fatalf("%+v", r)
		}
		// one start call, then polls at 15 s and 30 s of the 40 s render
		if n := up.startCount(); n != 3 {
			t.Errorf("start calls %d, want 3", n)
		}
	})

	t.Run("410 while rendering stops without uploading", func(t *testing.T) {
		t.Parallel()
		clk := clocktest.NewManual(t0)
		up := &recUploader{startCode: func(n int) int {
			if n >= 2 {
				return 410
			}
			return 200
		}}
		sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Clock: clk, RenderDuration: 60 * time.Second})
		done := make(chan fake.Result, 1)
		go func() { done <- sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref") }()
		r, ok := drive(clk, done, 15*time.Second)
		if !ok {
			t.Fatal("worker did not stop")
		}
		if r.Output.Error == nil || r.Output.Error.Code != "ATTEMPT_CLOSED" || len(up.names()) != 0 {
			t.Errorf("%+v %v", r, up.names())
		}
	})

	t.Run("start failing for 60 s", func(t *testing.T) {
		t.Parallel()
		clk := clocktest.NewManual(t0)
		up := &recUploader{startCode: func(int) int { return 503 }}
		sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Clock: clk})
		done := make(chan fake.Result, 1)
		go func() { done <- sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref") }()
		r, ok := drive(clk, done, 5*time.Second)
		if !ok {
			t.Fatal("no result")
		}
		if r.Output.Error == nil || r.Output.Error.Code != "START_FAILED" || len(up.names()) != 0 {
			t.Errorf("%+v", r)
		}
	})
}

// drive advances clk by step whenever the worker goroutine has armed a timer, until it delivers a result.
func drive(clk *clocktest.Manual, done <-chan fake.Result, step time.Duration) (fake.Result, bool) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case r := <-done:
			return r, true
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(step)
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	return fake.Result{}, false
}

func TestSimWorker_UploadsDigestAndOrder(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	sim := newSim(t, up)
	ts := specCot("j", 1, "pop", "full", 186)
	res := sim.Run(context.Background(), inputFor(t, ts), "ref")
	if res.Failed {
		t.Fatalf("%+v", res)
	}
	names := up.names()
	if names[len(names)-1] != "mm-result.json" || strings.Count(strings.Join(names, ","), "mm-result.json") != 1 {
		t.Fatalf("mm-result.json last, once: %v", names)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	for _, p := range up.puts {
		if p.Digest != digestOf(p.Body) {
			t.Errorf("%s: digest %q", p.Name, p.Digest)
		}
		if !strings.HasPrefix(p.URL, uploadBase+"j-a1/") {
			t.Errorf("uploaded to the target of the input: %s", p.URL)
		}
	}
}

func TestSimWorker_UploadErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code func(string) int
		want string
	}{
		{"closed", func(string) int { return 410 }, "UPLOAD_CLOSED"},
		{"conflict", func(string) int { return 409 }, "UPLOAD_CONFLICT"},
		{"forbidden", func(string) int { return 403 }, "UPLOAD_FAILED"},
		{"digest never ok", func(string) int { return 422 }, "UPLOAD_FAILED"},
	} {
		up := &recUploader{putCode: tc.code}
		sim := newSim(t, up)
		res := sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref")
		if !res.Failed || res.Output.Error == nil || res.Output.Error.Code != tc.want {
			t.Errorf("%s: %+v", tc.name, res.Output.Error)
		}
	}
}

func TestSimWorker_BadInputAndWeights(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	sim := newSim(t, up)
	if res := sim.Run(context.Background(), []byte(`{"contract":"mm.worker/1"}`), "ref"); res.Output.Error == nil || res.Output.Error.Code != "BAD_INPUT" {
		t.Errorf("%+v", res.Output.Error)
	}
	ts := spec("j", 1, "pop", 186)
	ts.Expect = provider.WorkerExpect{Weights: map[string]string{"m-a-p/YuE2-3B": strings.Repeat("a", 40)}}
	pinned := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Weights: yue2.Weights})
	res := pinned.Run(context.Background(), inputFor(t, ts), "ref")
	if res.Output.Error == nil || res.Output.Error.Code != "WEIGHTS_MISMATCH" || len(up.names()) != 1 {
		t.Errorf("%+v %v", res.Output.Error, up.names()) // only the best-effort error mm-result.json
	}
	for _, msg := range []string{res.Output.Error.Message} {
		if strings.Contains(msg, "sig=") || strings.Contains(msg, "http") {
			t.Errorf("message leaks a URL: %q", msg)
		}
	}
}

func TestSimWorker_HeaderOnlyMode(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	// no ffmpeg tools configured at all
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
	ts := spec("j", 1, "pop", 186)
	res := sim.Run(context.Background(), inputFor(t, ts), "ref")
	if res.Failed {
		t.Fatalf("%+v", res)
	}
	audio := up.body("audio.flac")
	if !bytes.HasPrefix(audio, []byte("fLaC")) {
		t.Fatalf("not a FLAC: %q", audio[:8])
	}
	if _, err := flacmeta.Read(bytes.NewReader(audio), int64(len(audio))); err != nil {
		t.Errorf("header-valid: %v", err)
	}
	// two attempts differ
	up2 := &recUploader{}
	sim2 := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up2, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
	sim2.Run(context.Background(), inputFor(t, spec("j", 2, "pop", 186)), "ref")
	if bytes.Equal(audio, up2.body("audio.flac")) {
		t.Error("distinct attempts, distinct audio even in header-only mode")
	}
}

func TestSimWorker_NoTestDeps(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		n++
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, banned := range []string{"jsonschema", "testtools", "flactest", "clocktest", "cliptest", "providertest", "runpodtest", "net/http/httptest", "testing"} {
				if strings.Contains(path, banned) {
					t.Errorf("%s imports %s: the production fake must not depend on test packages", name, path)
				}
			}
		}
	}
	if n < 5 {
		t.Fatalf("only %d files scanned", n)
	}
}

// compile-time: the fake is a provider.Backend and a recording Uploader satisfies the interface.
var (
	_ provider.Backend = (*fake.Backend)(nil)
	_ fake.Uploader    = (*recUploader)(nil)
	_ sync.Locker      = (*sync.Mutex)(nil)
)
