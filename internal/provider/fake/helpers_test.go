package fake_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// t0 is hour-aligned so billing buckets are easy to reason about.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const uploadBase = "https://mm.example.com/worker/v1/uploads/"

// newFake builds a backend on its own manual clock (no globals shared between tests).
func newFake(tb testing.TB, o fake.Options) (*fake.Backend, *clocktest.Manual) {
	tb.Helper()
	clk := clocktest.NewManual(t0)
	o.Clock = clk
	return fake.New(o), clk
}

// reqJSON is a synthetic canonical request.
func reqJSON(style, cot string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"cot": cot, "id": "syn-a-s1", "lyrics": "[Verse]\nla la\n\n[Chorus]\nmo mo", "seed": 1, "style": style})
	return b
}

// spec builds a TakeSpec; the attempt id is derived from the job id and attempt number.
func spec(job string, attempt int, style string, timeoutS int) provider.TakeSpec {
	return specCot(job, attempt, style, "off", timeoutS)
}

func specCot(job string, attempt int, style, cot string, timeoutS int) provider.TakeSpec {
	aid := fmt.Sprintf("%s-a%d", job, attempt)
	up := map[string]provider.UploadTarget{}
	for _, a := range provider.Artifacts {
		up[a.Name] = provider.UploadTarget{URL: uploadBase + aid + "/" + a.Name, MaxBytes: a.MaxBytes}
	}
	return provider.TakeSpec{
		JobID: job, AttemptID: aid, Engine: "yue2", Request: reqJSON(style, cot),
		TimeoutS: timeoutS, TTL: 2 * time.Hour, UploadBudgetS: 60,
		Uploads:  up,
		StartURL: "https://mm.example.com/worker/v1/attempts/" + aid + "/start?exp=1&sig=x",
		Expect:   provider.WorkerExpect{Weights: yue2.Weights},
		Deadline: t0.Add(3 * time.Hour),
	}
}

// recUploader records every call and answers from scripted functions (no sockets).
type recUploader struct {
	mu        sync.Mutex
	puts      []rec
	starts    []startRec
	startCode func(n int) int       // nil => 200
	putCode   func(name string) int // nil => 201
	order     []string
}

type rec struct {
	URL, Name, Digest string
	Body              []byte
}

type startRec struct{ URL, Ref string }

func (u *recUploader) Start(_ context.Context, url string, body []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	var b struct {
		Ref string `json:"runpod_job_id"`
	}
	_ = json.Unmarshal(body, &b)
	u.starts = append(u.starts, startRec{URL: url, Ref: b.Ref})
	if u.startCode != nil {
		return u.startCode(len(u.starts)), nil
	}
	return http.StatusOK, nil
}

func (u *recUploader) Put(_ context.Context, url string, body []byte, digest string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	name := url[strings.LastIndex(url, "/")+1:]
	u.puts = append(u.puts, rec{URL: url, Name: name, Digest: digest, Body: append([]byte(nil), body...)})
	u.order = append(u.order, name)
	if u.putCode != nil {
		return u.putCode(name), nil
	}
	return http.StatusCreated, nil
}

func (u *recUploader) names() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.order...)
}

func (u *recUploader) body(name string) []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := len(u.puts) - 1; i >= 0; i-- {
		if u.puts[i].Name == name {
			return u.puts[i].Body
		}
	}
	return nil
}

func (u *recUploader) startCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.starts)
}

// digestOf renders RFC 9530 Content-Digest for a body.
func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(s[:]) + ":"
}

// headerOnly returns header-valid FLAC bytes whose frames depend on the audio identity.
func headerOnly(id uint64, seconds float64) []byte {
	frames := make([]byte, 64)
	for i := range frames {
		frames[i] = byte(id >> (8 * (i % 8)))
	}
	return flactest.Build(flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: uint64(seconds * 48000), Vendor: "libFLAC", Frames: frames})
}

// newSim is a header-only SimWorker on up (no ffmpeg).
func newSim(tb testing.TB, up fake.Uploader) *fake.SimWorker {
	tb.Helper()
	return fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: tb.TempDir()})
}

func mustPoll(tb testing.TB, b *fake.Backend, ref provider.Ref) provider.RemoteStatus {
	tb.Helper()
	st, err := b.Poll(context.Background(), ref)
	if err != nil {
		tb.Fatalf("poll: %v", err)
	}
	return st
}

func mustSubmit(tb testing.TB, b *fake.Backend, ts provider.TakeSpec) provider.Ref {
	tb.Helper()
	ref, err := b.Submit(context.Background(), ts)
	if err != nil {
		tb.Fatalf("submit: %v", err)
	}
	return ref
}

func ms(v *int64) int64 {
	if v == nil {
		return -1
	}
	return *v
}

func workersOf(tb testing.TB, b *fake.Backend) []provider.Worker {
	tb.Helper()
	r, err := b.Sweep(context.Background())
	if err != nil {
		tb.Fatalf("sweep: %v", err)
	}
	return r.Workers
}

func countStatus(ws []provider.Worker, status string) int {
	n := 0
	for _, w := range ws {
		if w.Status == status {
			n++
		}
	}
	return n
}

// newWithSim builds a backend that runs a header-only SimWorker uploading to up.
func newWithSim(tb testing.TB, o fake.Options, up *recUploader) (*fake.Backend, *clocktest.Manual) {
	tb.Helper()
	o.Worker = newSim(tb, up)
	return newFake(tb, o)
}
