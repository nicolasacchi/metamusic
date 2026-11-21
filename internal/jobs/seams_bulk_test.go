package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/objstore/objstoretest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/upload/fallback"
)

// stubMinter is a FallbackMinter that counts its calls and hands out one URL per upload of the attempt.
type stubMinter struct {
	calls atomic.Int32
	err   error
	after int
	rig   *rig
}

func (m *stubMinter) MintFallback(ctx context.Context, attemptID string, _ time.Time) (map[string]string, int, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, 0, m.err
	}
	ups, err := m.rig.up.AttemptUploads(ctx, attemptID)
	if err != nil {
		return nil, 0, err
	}
	urls := map[string]string{}
	for _, u := range ups {
		urls[u.Artifact] = "https://bucket.example.com/mm/" + u.ID + "?X-Amz-Signature=synthetic"
	}
	after := m.after
	if after == 0 {
		after = 120
	}
	return urls, after, nil
}

func TestMint_FallbackOnlyOnV2WithCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		wire     bool // a minter is wired
		open     []string
		closed   bool // the gate has a row that closes everything
		mintErr  error
		after    int
		wantCall bool
		wantFB   bool
	}{
		{name: "no minter wired", open: []string{"v2", "fallback"}, wantCall: false},
		{name: "capability open", wire: true, open: []string{"v2", "fallback"}, wantCall: true, wantFB: true},
		{name: "capability closed", wire: true, open: []string{"v2"}, closed: true, wantCall: false},
		{name: "no gate row on a simulated backend opens everything", wire: true, wantCall: true, wantFB: true},
		{name: "mint error means no fallback", wire: true, open: []string{"v2", "fallback"}, mintErr: errors.New("boom https://x?X-Amz-Signature=secret"), wantCall: true},
		{name: "switch time out of range means no fallback", wire: true, open: []string{"v2", "fallback"}, after: 30, wantCall: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			m := &stubMinter{rig: r, err: tc.mintErr, after: tc.after}
			if tc.wire {
				r.run.UseFallback(m, nil)
			}
			if tc.closed || len(tc.open) > 0 {
				r.setCaps(gateDigest, tc.open...)
			}
			job := r.seedBatch(batchSpec{}).Jobs[0]
			spec := r.submitOne(job)
			if got := m.calls.Load() > 0; got != tc.wantCall {
				t.Fatalf("minter called = %v, want %v", got, tc.wantCall)
			}
			has := false
			for _, u := range spec.Uploads {
				has = has || u.Fallback != nil
			}
			if has != tc.wantFB {
				t.Fatalf("fallback in the spec = %v, want %v", has, tc.wantFB)
			}
			if tc.wantFB {
				if spec.Wire != provider.ContractV2 || spec.FallbackAfterS != 120 {
					t.Errorf("wire %q after %d", spec.Wire, spec.FallbackAfterS)
				}
				for n, u := range spec.Uploads {
					if u.Fallback == nil {
						t.Errorf("%s has no fallback", n)
					}
				}
				raw, err := provider.BuildWorkerInput(spec)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := provider.ParseWorkerInput(raw); err != nil {
					t.Errorf("the document the worker gets does not parse: %v", err)
				}
			} else if spec.Wire == provider.ContractV2 || spec.FallbackAfterS != 0 {
				t.Errorf("a job without fallback went out as %q", spec.Wire)
			}
			if tc.mintErr != nil {
				if strings.Contains(strings.Join(r.events(job), " "), "X-Amz") {
					t.Error("the mint error reached the events")
				}
			}
		})
	}
}

// fbRig is a rig whose runner has a real upload fallback over an objstoretest bucket, with the capability open.
func fbRig(t *testing.T) (*rig, *objstoretest.Server, *fallback.Fallback) {
	t.Helper()
	r := newRig(t)
	bkt := objstoretest.New(t)
	client, err := objstore.New(bkt.Config(), bkt.HTTPClient(), time.Now) // the bucket runs on the real clock
	if err != nil {
		t.Fatal(err)
	}
	fb, err := fallback.New(fallback.Options{Store: r.st, Uploads: r.up, Objects: client, Prefix: "mm/", Clock: r.clk, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	r.run.UseFallback(fb, fb)
	r.setCaps(gateDigest, "v2", "fallback")
	return r, bkt, fb
}

// bucketWorker plays a worker that could not reach the app: it puts every artifact and mm-result.json into the bucket through the
// fallback URLs of the spec, mm-result.json last, and returns its output.
func bucketWorker(t *testing.T, r *rig, bkt *objstoretest.Server, spec provider.TakeSpec) provider.Output {
	t.Helper()
	put := func(url string, body []byte) {
		req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = int64(len(body))
		resp, err := bkt.HTTPClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("bucket put: %d", resp.StatusCode)
		}
	}
	out := provider.Output{Contract: provider.ContractV2, Kind: provider.KindRender, TakeID: spec.JobID, AttemptID: spec.AttemptID, Status: "ok",
		Artifacts: map[string]provider.ArtifactDigest{}, Truncated: map[string]bool{"abc": false, "semantic": false},
		Env: provider.OutputEnv{Engine: "yue2", PkgVersion: "0.1.6", CodeCommit: "0000000000000000000000000000000000000003", Decoder: "m-a-p/YuE2-Vae",
			GPUName: "NVIDIA RTX A5000", CUDA: "12.8", Torch: "2.10.0", WorkerBuild: "test", Python: "3.12", Weights: spec.Expect.Weights}}
	n := json.Number("215.8")
	out.AudioSeconds = &n
	for _, name := range sortedKeys(spec.Uploads) {
		if name == provider.ResultArtifact || !requiredName(name) {
			continue
		}
		fbk := spec.Uploads[name].Fallback
		if fbk == nil {
			t.Fatalf("%s has no fallback target", name)
		}
		body := []byte("synthetic " + name + " " + spec.AttemptID)
		put(fbk.URL, body)
		out.Artifacts[name] = provider.ArtifactDigest{SHA256: sha256Hex(body), Bytes: int64(len(body))}
	}
	put(spec.Uploads[provider.ResultArtifact].Fallback.URL, rawOutput(out))
	return out
}

func TestTakeSpec_FallbackURLsNeverPersisted(t *testing.T) {
	t.Parallel()
	r, bkt, _ := fbRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	if spec.Wire != provider.ContractV2 || spec.Uploads["audio.flac"].Fallback == nil {
		t.Fatalf("the attempt has no fallback: %q", spec.Wire)
	}
	if n := r.count(`SELECT COUNT(*) FROM fallback_objects WHERE attempt_id = ?`, spec.AttemptID); n != len(spec.Uploads) {
		t.Fatalf("%d objects recorded for %d uploads", n, len(spec.Uploads))
	}
	cfg := bkt.Config()
	for _, tbl := range []string{"job_events", "job_attempts", "uploads", "fallback_objects", "audit_log", "jobs", "batches"} {
		rows, err := r.st.R.Query(`SELECT * FROM ` + tbl)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			line := fmt.Sprint(vals...)
			for _, banned := range []string{"X-Amz", "Signature", cfg.SecretAccessKey, cfg.AccessKeyID, "127.0.0.1"} {
				if strings.Contains(line, banned) {
					t.Errorf("%s: %q reached a row", tbl, banned)
				}
			}
		}
		rows.Close()
	}
}

// TestRunner_RescueBeforeTimeBound: the worker put its result in the bucket and the app never heard from it; when the attempt's window
// closes the runner asks the rescuer first, so the take completes instead of failing WORKER_LOST.
func TestRunner_RescueBeforeTimeBound(t *testing.T) {
	t.Parallel()
	r, bkt, _ := fbRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
	r.advance(10 * time.Second)
	r.step()
	if s := r.state(job); s != StateGenerating {
		t.Fatalf("state %s", s)
	}
	bucketWorker(t, r, bkt, spec)
	r.advance(4 * time.Hour) // past the window and the ttl
	r.steps(3)
	if s := r.state(job); s != StateCompleted {
		t.Fatalf("state %s, events %v", s, r.events(job))
	}
	if n := r.count(`SELECT COUNT(*) FROM uploads WHERE attempt_id = ? AND via = 'fallback' AND state IN ('received','consumed')`, spec.AttemptID); n < 5 {
		t.Errorf("%d uploads arrived through the fallback", n)
	}
	if !r.hasEvent(job, "result_received") || r.count(`SELECT COUNT(*) FROM job_events WHERE event = 'failed'`) != 0 {
		t.Errorf("events %v", r.events(job))
	}
}

// TestRunner_RescueWhenStatusLost: a status that no longer exists (NOT_FOUND) is not a lost result.
func TestRunner_RescueWhenStatusLost(t *testing.T) {
	t.Parallel()
	r, bkt, _ := fbRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
	r.advance(10 * time.Second)
	r.step()
	bucketWorker(t, r, bkt, spec)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"})
	r.advance(30 * time.Second)
	r.steps(3)
	if s := r.state(job); s != StateCompleted {
		t.Fatalf("state %s, events %v", s, r.events(job))
	}
}

// TestRunner_RescueWhileCompletedWaitsForUploads: the provider says Completed but the uploads never reached the app.
func TestRunner_RescueWhileCompletedWaitsForUploads(t *testing.T) {
	t.Parallel()
	r, bkt, _ := fbRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
	r.advance(10 * time.Second)
	r.step()
	out := bucketWorker(t, r, bkt, spec)
	r.setStatus(r.refOf(job, 1), completed(out, 70000))
	r.advance(10 * time.Second)
	r.steps(3)
	if s := r.state(job); s != StateCompleted {
		t.Fatalf("state %s, events %v", s, r.events(job))
	}
}

// TestRunner_FallbackMismatchFailsTheJob: a bucket object that does not match the output is never received; the job ends the way a
// missing artifact ends it today.
func TestRunner_FallbackMismatchIsNeverReceived(t *testing.T) {
	t.Parallel()
	r, bkt, _ := fbRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS"})
	r.advance(10 * time.Second)
	r.step()
	out := bucketWorker(t, r, bkt, spec)
	// the worker's output lies about audio.flac
	out.Artifacts["audio.flac"] = provider.ArtifactDigest{SHA256: strings.Repeat("0", 64), Bytes: out.Artifacts["audio.flac"].Bytes}
	put := func(url string, body []byte) {
		req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
		req.ContentLength = int64(len(body))
		resp, err := bkt.HTTPClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	put(spec.Uploads[provider.ResultArtifact].Fallback.URL, rawOutput(out))
	r.advance(4 * time.Hour)
	r.steps(3)
	if s := r.state(job); s == StateCompleted {
		t.Fatal("a take with a mismatching bucket object completed")
	}
	if r.count(`SELECT COUNT(*) FROM uploads WHERE artifact = 'audio.flac' AND via = 'fallback'`) != 0 {
		t.Error("the mismatching object was received")
	}
	if r.count(`SELECT COUNT(*) FROM job_events WHERE event = 'fallback_mismatch'`) != 1 {
		t.Errorf("events %v", r.events(job))
	}
}

// TestRescue_NotConfiguredIsM3: with no fallback wired every seam is inert: a plain render stays on /1, the rescue is never asked and no
// fallback table is touched.
func TestRescue_NotConfiguredIsM3(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	if spec.Wire != "" || spec.FallbackAfterS != 0 {
		t.Fatalf("wire %q", spec.Wire)
	}
	for _, u := range spec.Uploads {
		if u.Fallback != nil {
			t.Fatal("a fallback was minted")
		}
	}
	r.advance(4 * time.Hour)
	r.steps(2)
	if n := r.count(`SELECT COUNT(*) FROM fallback_objects`) + r.count(`SELECT COUNT(*) FROM upload_drills`); n != 0 {
		t.Errorf("%d fallback rows", n)
	}
	if r.run.rescue(t.Context(), "x", true) {
		t.Error("a nil rescuer found something")
	}
}

type countingRescuer struct {
	calls atomic.Int32
	found bool
	err   error
}

func (c *countingRescuer) Rescue(context.Context, string) (bool, error) {
	c.calls.Add(1)
	return c.found, c.err
}

func TestRescue_RateLimitAndFaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	cr := &countingRescuer{err: errors.New("boom X-Amz-Signature=secret")}
	r.run.UseFallback(nil, cr)
	for range 3 {
		if r.run.rescue(t.Context(), "a1", false) {
			t.Fatal("a fault reported as found")
		}
	}
	if cr.calls.Load() != 1 {
		t.Fatalf("%d calls inside the gap, want 1", cr.calls.Load())
	}
	r.advance(rescueMinGap)
	r.run.rescue(t.Context(), "a1", false)
	r.run.rescue(t.Context(), "a1", true) // forced: a settlement is about to decide
	if cr.calls.Load() != 3 {
		t.Fatalf("%d calls, want 3", cr.calls.Load())
	}
	cr.err, cr.found = nil, true
	if !r.run.rescue(t.Context(), "a2", false) {
		t.Fatal("found not reported")
	}
}

func TestOutputWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, out string
		fb        bool
		want      string
	}{
		{provider.ContractV1, provider.ContractV2, true, provider.ContractV2},
		{provider.ContractV1, provider.ContractV2, false, provider.ContractV1},
		{provider.ContractV1, provider.ContractV1, true, provider.ContractV1},
		{provider.ContractV2, provider.ContractV1, true, provider.ContractV2},
		{"", provider.ContractV2, true, ""},
	} {
		if got := outputWire(tc.base, tc.out, tc.fb); got != tc.want {
			t.Errorf("outputWire(%q,%q,%v) = %q, want %q", tc.base, tc.out, tc.fb, got, tc.want)
		}
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestMint_NotForOtherEngines: only yue2 speaks /2, so another engine's attempt is never given a fallback.
func TestMint_NotForOtherEngines(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := &stubMinter{rig: r}
	r.run.UseFallback(m, nil)
	spec := provider.TakeSpec{Engine: provider.EngineACEStep, Uploads: map[string]provider.UploadTarget{"audio.flac": {URL: "https://mm.example.com/u", MaxBytes: 1}}}
	r.run.applyFallback(t.Context(), &claim{job: jobRec{Engine: provider.EngineACEStep, Backend: provider.BackendFake}}, &spec)
	if m.calls.Load() != 0 || spec.Uploads["audio.flac"].Fallback != nil || spec.Wire != "" {
		t.Fatalf("an acestep attempt was given a fallback (%d calls)", m.calls.Load())
	}
}

// TestMint_NotForADocumentTheWorkerWouldRefuse: nothing is minted (so no object is recorded) when the /2 document of the attempt cannot
// be built or does not parse.
func TestMint_NotForADocumentTheWorkerWouldRefuse(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2", "fallback")
	m := &stubMinter{rig: r}
	r.run.UseFallback(m, nil)
	ups := map[string]provider.UploadTarget{"audio.flac": {URL: "https://mm.example.com/u", MaxBytes: 1}}
	for name, req := range map[string]string{
		"does not parse (no request)": "",
		"cannot be built (too big)":   `{"lyrics":"` + strings.Repeat("a", provider.MaxRunBody+1) + `"}`,
	} {
		spec := provider.TakeSpec{Engine: provider.EngineYuE2, JobID: "j", AttemptID: "a", Request: json.RawMessage(req), Uploads: ups, TimeoutS: 100, UploadBudgetS: 60}
		r.run.applyFallback(t.Context(), &claim{job: jobRec{Engine: provider.EngineYuE2, Backend: provider.BackendFake}}, &spec)
		if m.calls.Load() != 0 || spec.Wire != "" || spec.Uploads["audio.flac"].Fallback != nil {
			t.Errorf("%s: a fallback was minted (%d calls)", name, m.calls.Load())
		}
	}
}

// TestMint_EmptyAnswerMeansNoFallback: a minter that hands out no URL leaves the attempt as it was.
func TestMint_EmptyAnswerMeansNoFallback(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps(gateDigest, "v2", "fallback")
	r.run.UseFallback(emptyMinter{}, nil)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	if spec.Wire != "" || spec.Uploads["audio.flac"].Fallback != nil {
		t.Fatalf("wire %q", spec.Wire)
	}
}

type emptyMinter struct{}

func (emptyMinter) MintFallback(context.Context, string, time.Time) (map[string]string, int, error) {
	return nil, 120, nil
}
