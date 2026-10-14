package fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

// Uploader is how the simulated handler talks to the app: the start handshake (POST, body
// {"runpod_job_id": ...}) and artifact PUTs with an RFC 9530 Content-Digest. Status is the HTTP status.
// URLs carry capability signatures: implementations must never log them or put them in errors.
type Uploader interface {
	Start(ctx context.Context, url string, body []byte) (status int, err error)
	Put(ctx context.Context, url string, body []byte, contentDigest string) (status int, err error)
}

// SimWorkerOptions builds a SimWorker.
type SimWorkerOptions struct {
	Uploader Uploader
	// Clock drives the waits of Run (start retries, the render and its 15 s start polls). Nil is the real
	// clock. The Backend does not use it: it keeps virtual time itself.
	Clock clock.Clock
	// Tools renders the audio with ffmpeg (synth.FLACUncalibrated). Unused when HeaderOnlyFLAC is set.
	Tools mediatool.Tools
	// HeaderOnlyFLAC, when set, replaces the render: it returns the FLAC bytes for an audio identity (the
	// first 8 bytes of sha256(attempt_id), fixed for fake:same_audio) and a length in seconds. The property
	// suite and unit tests use header-valid files and need no ffmpeg.
	HeaderOnlyFLAC func(id uint64, seconds float64) []byte
	TempDir        string        // scratch for rendered files; default os.TempDir()
	RenderDuration time.Duration // how long Run "renders" (default 0)
	AudioSeconds   float64       // default 6
	// Weights, when set, are the revisions this worker "loaded"; a different expect.weights is answered
	// WEIGHTS_MISMATCH. Nil means the worker loaded exactly what the input expects.
	Weights map[string]string
}

// SimWorker simulates the worker handler of C83.2: it validates the input, announces itself through the
// start URL, renders the exact artifact contract with internal/synth, uploads every artifact with a
// Content-Digest (mm-result.json last) and returns the C73.5 output.
type SimWorker struct{ o SimWorkerOptions }

// NewSimWorker builds the simulated worker.
func NewSimWorker(o SimWorkerOptions) *SimWorker {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.AudioSeconds <= 0 {
		o.AudioSeconds = 6
	}
	return &SimWorker{o: o}
}

// Result is what a handler run produced.
type Result struct {
	Output provider.Output
	// Raw is what the provider job returns: the output JSON, or {"error": "CODE: message", "mm": output}
	// for a failed run (RunPod marks the job FAILED).
	Raw     json.RawMessage
	Failed  bool
	ErrText string
	// Elapsed is the virtual time an early exit consumed (a start handshake that kept failing: 60 s).
	Elapsed time.Duration
}

const (
	startRetryFor   = 60 * time.Second
	startRetryEvery = 5 * time.Second
	putTries        = 3
	workerBuild     = "0000000000000000000000000000000000000fa1"
)

// Session is one handler run between Begin and Finish.
type Session struct {
	w       *SimWorker
	in      provider.WorkerInput
	req     requestFields
	mk      markers
	ref     string
	virtual bool
	elapsed time.Duration
	closed  bool
}

type requestFields struct {
	Style    string   `json:"style"`
	Lyrics   string   `json:"lyrics"`
	Cot      string   `json:"cot"`
	Seed     int64    `json:"seed"`
	ABC      *string  `json:"abc"`
	CFGScale *float64 `json:"cfg_scale"`
	ID       string   `json:"id"`
}

// Run is a complete handler run in the worker's own time: Begin, a render of RenderDuration during which
// the start URL is re-polled every 15 s, then Finish.
func (w *SimWorker) Run(ctx context.Context, rawInput []byte, ref string) Result {
	s, early := w.Begin(ctx, rawInput, ref, false)
	if early != nil {
		return *early
	}
	for left := w.o.RenderDuration; left > 0; {
		step := min(left, startPollEvery)
		if err := s.sleep(ctx, step); err != nil {
			return s.fail("PIPELINE_ERROR", "cancelled", true)
		}
		left -= step
		if left > 0 && s.Poll(ctx) {
			r := s.closedResult()
			return *r
		}
	}
	return s.Finish(ctx)
}

// Begin validates the input and performs the start handshake. A non-nil Result means the handler already
// ended (bad input, closed attempt, start failure, weights mismatch). virtual makes waits accumulate in
// Result.Elapsed instead of sleeping (the Backend keeps its own virtual time).
func (w *SimWorker) Begin(ctx context.Context, rawInput []byte, ref string, virtual bool) (*Session, *Result) {
	s := &Session{w: w, ref: ref, virtual: virtual}
	in, err := provider.ParseWorkerInput(rawInput)
	if err != nil {
		r := s.errorResult(idsOf(rawInput), "BAD_INPUT", err.Error(), false)
		return nil, &r
	}
	s.in = in
	_ = json.Unmarshal(in.Request, &s.req)
	if s.req.Cot == "" {
		s.req.Cot = "full"
	}
	s.mk = parseMarkers(s.req.Style)
	if !s.mk.noStart {
		switch s.announce(ctx) {
		case announceClosed:
			r := s.closedResult()
			return s, r
		case announceFailed:
			r := s.errorResult(s.ids(), "START_FAILED", "the start call kept failing", false)
			r.Elapsed = s.elapsed
			return s, &r
		}
	}
	if w.o.Weights != nil && !sameWeights(w.o.Weights, in.Expect.Weights) {
		r := s.fail("WEIGHTS_MISMATCH", "expect.weights differ from the loaded revisions", true)
		return s, &r
	}
	return s, nil
}

func sameWeights(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

type announceResult int

const (
	announceOK announceResult = iota
	announceClosed
	announceFailed
)

func (s *Session) announce(ctx context.Context) announceResult {
	body, _ := json.Marshal(map[string]string{"runpod_job_id": s.ref})
	var waited time.Duration
	for {
		code, err := s.w.o.Uploader.Start(ctx, s.in.Start.URL, body)
		switch {
		case err == nil && code == http.StatusGone:
			s.closed = true
			return announceClosed
		case err == nil && code >= 200 && code < 300:
			return announceOK
		case err == nil && code >= 400 && code < 500 && code != 408 && code != 429:
			return announceFailed
		}
		if waited >= startRetryFor {
			return announceFailed
		}
		if s.sleep(ctx, startRetryEvery) != nil {
			return announceFailed
		}
		waited += startRetryEvery
	}
}

// Poll re-polls the start URL (the handler's cancelled callback); true means the attempt is closed (410).
func (s *Session) Poll(ctx context.Context) bool {
	if s.closed {
		return true
	}
	body, _ := json.Marshal(map[string]string{"runpod_job_id": s.ref})
	if code, err := s.w.o.Uploader.Start(ctx, s.in.Start.URL, body); err == nil && code == http.StatusGone {
		s.closed = true
	}
	return s.closed
}

func (s *Session) sleep(ctx context.Context, d time.Duration) error {
	if s.virtual {
		s.elapsed += d
		return ctx.Err()
	}
	t := s.w.o.Clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) closedResult() *Result {
	r := s.errorResult(s.ids(), "ATTEMPT_CLOSED", "the attempt was closed", false) // nothing is uploaded
	return &r
}

type ids struct{ take, attempt string }

func (s *Session) ids() ids { return ids{s.in.TakeID, s.in.AttemptID} }

var idPat = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// idsOf extracts the ids of an input that failed validation, falling back to a placeholder id.
func idsOf(raw []byte) ids {
	var m struct {
		Take    string `json:"take_id"`
		Attempt string `json:"attempt_id"`
	}
	_ = json.Unmarshal(raw, &m)
	pick := func(v string) string {
		if idPat.MatchString(v) {
			return v
		}
		return "unknown"
	}
	return ids{pick(m.Take), pick(m.Attempt)}
}

func (s *Session) baseOutput() provider.Output {
	weights := map[string]string{}
	for k, v := range s.in.Expect.Weights {
		weights[k] = v
	}
	return provider.Output{
		Contract: provider.Contract, TakeID: s.in.TakeID, AttemptID: s.in.AttemptID, Status: "ok",
		Artifacts: map[string]provider.ArtifactDigest{},
		Timings:   map[string]*json.Number{},
		Env: provider.OutputEnv{Engine: s.in.Engine, PkgVersion: "0.1.6", CodeCommit: workerBuild, Weights: weights,
			Decoder: "m-a-p/YuE2-Vae", GPUName: "NVIDIA RTX A5000 (simulated)", CUDA: "12.8", Torch: "2.10.0", WorkerBuild: workerBuild, Python: "3.12.12"},
	}
}

// errorResult builds a failed run's Result; the message never contains a URL.
func (s *Session) errorResult(id ids, code, msg string, _ bool) Result {
	out := provider.Output{Contract: provider.Contract, TakeID: id.take, AttemptID: id.attempt, Status: "error",
		Error: &provider.OutputError{Code: code, Message: truncate(msg, 500)}, Artifacts: map[string]provider.ArtifactDigest{},
		Timings: map[string]*json.Number{}}
	if s.in.Engine != "" {
		out = mergeEnv(out, s.baseOutput())
	}
	text := code + ": " + out.Error.Message
	raw, _ := json.Marshal(map[string]any{"error": text, "mm": out})
	return Result{Output: out, Raw: raw, Failed: true, ErrText: text, Elapsed: s.elapsed}
}

func mergeEnv(out, base provider.Output) provider.Output {
	out.Env = base.Env
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// fail is errorResult plus the best-effort error mm-result.json upload (not after a closed attempt).
func (s *Session) fail(code, msg string, upload bool) Result {
	r := s.errorResult(s.ids(), code, msg, false)
	if upload {
		s.putResult(context.Background(), r.Output)
	}
	return r
}

func (s *Session) putResult(ctx context.Context, out provider.Output) {
	t, ok := s.in.Uploads[provider.ResultArtifact]
	if !ok {
		return
	}
	body, _ := json.Marshal(out)
	_, _ = s.put(ctx, provider.ResultArtifact, t, body)
}

func contentDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}

func (s *Session) put(ctx context.Context, name string, t provider.UploadTarget, body []byte) (int, error) {
	if s.mk.uploadClosed {
		return http.StatusGone, nil
	}
	var code int
	var err error
	for i := 0; i < putTries; i++ {
		code, err = s.w.o.Uploader.Put(ctx, t.URL, body, contentDigest(body))
		if err == nil && (code == 200 || code == 201) {
			return code, nil
		}
		retry := err != nil || code == 408 || code == 429 || code == 422 || code >= 500
		if !retry {
			break
		}
	}
	return code, err
}

type artifact struct {
	name string
	body []byte
}

// Finish renders and uploads the artifacts and returns the output. Call it once, after Begin.
func (s *Session) Finish(ctx context.Context) Result {
	if s.mk.failWorkerError {
		return s.fail("PIPELINE_ERROR", "simulated pipeline failure", true)
	}
	arts, audioSecs, err := s.render(ctx)
	if err != nil {
		return s.fail("PIPELINE_ERROR", "render failed: "+err.Error(), true)
	}
	out := s.baseOutput()
	out.Truncated = map[string]bool{"abc": s.mk.truncAbc, "semantic": s.mk.truncSem}
	secs := json.Number(strconv.FormatFloat(audioSecs, 'f', -1, 64))
	out.AudioSeconds = &secs
	gen := s.w.o.RenderDuration.Seconds()
	if gen <= 0 {
		gen = 1
	}
	num := func(v float64) *json.Number { n := json.Number(strconv.FormatFloat(v, 'f', 3, 64)); return &n }
	out.Timings = map[string]*json.Number{"load_s": nil, "generate_s": num(gen), "save_s": num(0.1), "upload_s": num(0.1), "handler_s": num(gen + 0.2), "e2e_seconds": num(gen + 0.2)}
	for _, a := range arts {
		t, ok := s.in.Uploads[a.name]
		if !ok {
			continue // optional artifacts the app did not ask for
		}
		if int64(len(a.body)) > t.MaxBytes {
			return s.fail("ARTIFACT_TOO_LARGE", a.name+" exceeds its limit", true)
		}
		code, err := s.put(ctx, a.name, t, a.body)
		switch {
		case err == nil && (code == 200 || code == 201):
		case err == nil && code == http.StatusGone:
			return s.fail("UPLOAD_CLOSED", a.name+" refused: the upload channel is closed", false)
		case err == nil && code == http.StatusConflict:
			return s.fail("UPLOAD_CONFLICT", a.name+" refused: conflicting upload", true)
		default:
			return s.fail("UPLOAD_FAILED", a.name+" could not be uploaded", true)
		}
		sum := sha256.Sum256(a.body)
		if s.mk.shaMismatch && a.name == "audio.flac" {
			sum[0] ^= 0xff // report a digest that is not the uploaded bytes
		}
		out.Artifacts[a.name] = provider.ArtifactDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(a.body))}
	}
	raw, _ := json.Marshal(out)
	if t, ok := s.in.Uploads[provider.ResultArtifact]; ok {
		if code, err := s.put(ctx, provider.ResultArtifact, t, raw); err != nil || (code != 200 && code != 201) {
			return s.fail("UPLOAD_FAILED", "mm-result.json could not be uploaded", false)
		}
	}
	return Result{Output: out, Raw: raw}
}

// ---- artifacts ---------------------------------------------------------------------------------------

// attemptID hashes to the audio identity of an attempt.
func audioIdentity(attemptID string, same bool) uint64 {
	if same {
		return 0x5a5a5a5a5a5a5a5a
	}
	sum := sha256.Sum256([]byte(attemptID))
	return binary.BigEndian.Uint64(sum[:8])
}

func jsonIndent(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	_ = e.Encode(v)
	return b.Bytes()
}

var headerRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

// scoreLabels lists the section labels of the lyrics (headers; non-empty text before the first one is an
// "other" section), the layout LyricsSections reads.
func scoreLabels(lyrics string) []string {
	var labels []string
	pre := false
	for _, ln := range strings.Split(strings.ReplaceAll(lyrics, "\r\n", "\n"), "\n") {
		if m := headerRe.FindStringSubmatch(ln); m != nil {
			labels = append(labels, strings.TrimSpace(m[1]))
		} else if strings.TrimSpace(ln) != "" && len(labels) == 0 {
			pre = true
		}
	}
	if pre {
		labels = append([]string{"other"}, labels...)
	}
	return labels
}

const barsPerSection = 2

func scoreABC(labels []string) []byte {
	var b strings.Builder
	b.WriteString("X:1\nT:synthetic\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n")
	for i, l := range labels {
		b.WriteString("% " + l + "\n")
		if i == len(labels)-1 {
			b.WriteString("CDEF|CDEF|]\n")
		} else {
			b.WriteString("CDEF|CDEF|\n")
		}
	}
	return []byte(b.String())
}

func (s *Session) render(ctx context.Context) ([]artifact, float64, error) {
	secs := s.w.o.AudioSeconds
	reqDoc := jsonIndent(s.req) // YuE2 asdict order with explicit nulls
	rep := func(repo string) string { return s.in.Expect.Weights[repo] }
	config := jsonIndent(map[string]any{
		"engine": s.in.Engine, "model": "m-a-p/YuE2-3B", "revision": rep("m-a-p/YuE2-3B"),
		"vae": "m-a-p/YuE2-Vae", "vae_revision": rep("m-a-p/YuE2-Vae"),
		"sampling": map[string]any{"temperature": 1.0, "top_p": 0.93, "repetition_penalty": 1.1},
	})
	gen := s.w.o.RenderDuration.Seconds()
	if gen <= 0 {
		gen = 1
	}
	result := jsonIndent(map[string]any{
		"id": s.req.ID, "truncated": map[string]bool{"abc": s.mk.truncAbc, "semantic": s.mk.truncSem},
		"audio_seconds": secs, "duration": secs, "timing": map[string]float64{"e2e_seconds": gen + 0.2, "generate_s": gen},
	})
	arts := []artifact{{"request.json", reqDoc}, {"config.json", config}, {"result.json", result}}
	if s.req.Cot != "off" {
		labels := scoreLabels(s.req.Lyrics)
		type sec struct {
			Label    string `json:"label"`
			StartBar int    `json:"start_bar"`
			Bars     int    `json:"bars"`
		}
		secsDoc := make([]sec, len(labels))
		for i, l := range labels {
			secsDoc[i] = sec{l, i * barsPerSection, barsPerSection}
		}
		arts = append(arts,
			artifact{"score.abc", scoreABC(labels)},
			artifact{"plan.json", jsonIndent(map[string]any{"bpm": 120, "sections": secsDoc})},
			artifact{"plan_manifest.json", jsonIndent(map[string]any{"files": []string{"plan.json"}, "sections": len(labels)})})
	}
	dir, err := os.MkdirTemp(s.w.o.TempDir, "simworker-")
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	id := audioIdentity(s.in.AttemptID, s.mk.sameAudio)
	npyPath := filepath.Join(dir, "semantic.npy")
	if err := synth.SemanticNPY(npyPath, synth.NPYOptions{TokensPerS: 50, Seconds: secs, Vocab: 1024, Seed: id}); err != nil {
		return nil, 0, err
	}
	npyBytes, err := os.ReadFile(npyPath) // #nosec G304 -- a file this function just wrote in its own temp dir
	if err != nil {
		return nil, 0, err
	}
	var audio []byte
	if f := s.w.o.HeaderOnlyFLAC; f != nil {
		audio = f(id, secs)
	} else {
		variant := 0 // the fixed tone of fake:same_audio; attempts use 1..1000
		if !s.mk.sameAudio {
			variant = int(id%1000) + 1
		}
		path := filepath.Join(dir, "audio.flac")
		tone := synth.Tone{Seconds: secs, BPM: 120, Variant: variant}
		if err := synth.FLACUncalibrated(ctx, s.w.o.Tools, path, tone, synth.StyleYuE2); err != nil {
			return nil, 0, err
		}
		if audio, err = os.ReadFile(path); err != nil { // #nosec G304 -- as above
			return nil, 0, err
		}
	}
	// C83.2 step 8 order: request, config, result, score, plan files, npy files, audio
	arts = append(arts, artifact{"semantic.npy", npyBytes}, artifact{"audio.flac", audio})
	return arts, secs, nil
}

// ---- uploaders ---------------------------------------------------------------------------------------

// HTTPUploader talks to the app over HTTP (dev and E2E).
type HTTPUploader struct{ Client *http.Client }

func (u HTTPUploader) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (u HTTPUploader) do(ctx context.Context, method, target string, body []byte, header http.Header) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("fake: bad request URL")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := u.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries the capability signature
		}
		return 0, fmt.Errorf("fake: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// Start implements Uploader.
func (u HTTPUploader) Start(ctx context.Context, target string, body []byte) (int, error) {
	return u.do(ctx, http.MethodPost, target, body, http.Header{"Content-Type": {"application/json"}})
}

// Put implements Uploader.
func (u HTTPUploader) Put(ctx context.Context, target string, body []byte, digest string) (int, error) {
	return u.do(ctx, http.MethodPut, target, body, http.Header{"Content-Type": {"application/octet-stream"}, "Content-Digest": {digest}})
}

// HandlerUploader invokes the upload and start http.Handlers directly, without sockets (the property
// suite).
type HandlerUploader struct{ UploadHandler, StartHandler http.Handler }

type statusRecorder struct {
	h    http.Header
	code int
}

func (r *statusRecorder) Header() http.Header { return r.h }
func (r *statusRecorder) WriteHeader(c int) {
	if r.code == 0 {
		r.code = c
	}
}
func (r *statusRecorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return len(b), nil
}

func (h HandlerUploader) serve(ctx context.Context, handler http.Handler, method, target string, body []byte, header http.Header) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("fake: bad request URL")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.RequestURI = req.URL.RequestURI()
	rec := &statusRecorder{h: http.Header{}}
	handler.ServeHTTP(rec, req)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return rec.code, nil
}

// Start implements Uploader.
func (h HandlerUploader) Start(ctx context.Context, target string, body []byte) (int, error) {
	return h.serve(ctx, h.StartHandler, http.MethodPost, target, body, http.Header{"Content-Type": {"application/json"}})
}

// Put implements Uploader.
func (h HandlerUploader) Put(ctx context.Context, target string, body []byte, digest string) (int, error) {
	return h.serve(ctx, h.UploadHandler, http.MethodPut, target, body, http.Header{"Content-Type": {"application/octet-stream"}, "Content-Digest": {digest}})
}
