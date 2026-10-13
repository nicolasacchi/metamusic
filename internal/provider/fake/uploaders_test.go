package fake_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

type seen struct {
	Method, Path, Query, Digest, CT string
	Body                            []byte
}

func recorder(status int) (http.Handler, func() []seen) {
	var mu sync.Mutex
	var got []seen
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got = append(got, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Digest"), r.Header.Get("Content-Type"), b})
			mu.Unlock()
			w.WriteHeader(status)
		}), func() []seen {
			mu.Lock()
			defer mu.Unlock()
			return append([]seen(nil), got...)
		}
}

func TestHTTPUploader(t *testing.T) {
	t.Parallel()
	h, calls := recorder(http.StatusCreated)
	srv := httptest.NewServer(h)
	defer srv.Close()
	u := fake.HTTPUploader{}
	ctx := context.Background()
	if code, err := u.Put(ctx, srv.URL+"/worker/v1/uploads/x?exp=1&sig=s", []byte("abc"), digestOf([]byte("abc"))); err != nil || code != 201 {
		t.Fatalf("%d %v", code, err)
	}
	if code, err := u.Start(ctx, srv.URL+"/worker/v1/attempts/a/start?exp=1&sig=s", []byte(`{"runpod_job_id":"r"}`)); err != nil || code != 201 {
		t.Fatalf("%d %v", code, err)
	}
	got := calls()
	if len(got) != 2 || got[0].Method != "PUT" || got[0].Digest != digestOf([]byte("abc")) || got[0].CT != "application/octet-stream" || string(got[0].Body) != "abc" ||
		got[1].Method != "POST" || got[1].Path != "/worker/v1/attempts/a/start" || got[1].CT != "application/json" {
		t.Errorf("%+v", got)
	}
	// a transport error never carries the capability URL
	srv.Close()
	_, err := u.Put(ctx, srv.URL+"/worker/v1/uploads/x?exp=1&sig=SECRETSIG", []byte("abc"), "d")
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") || strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error leaks the URL: %v", err)
	}
	if _, err := u.Put(ctx, "://bad", nil, ""); err == nil || strings.Contains(err.Error(), "://") {
		t.Errorf("bad URL: %v", err)
	}
}

func TestHandlerUploader(t *testing.T) {
	t.Parallel()
	up, upCalls := recorder(http.StatusCreated)
	st, stCalls := recorder(http.StatusGone)
	u := fake.HandlerUploader{UploadHandler: up, StartHandler: st}
	ctx := context.Background()
	if code, err := u.Put(ctx, uploadBase+"a/audio.flac?exp=1&sig=s", []byte("xyz"), "sha-256=:AA==:"); err != nil || code != 201 {
		t.Fatalf("%d %v", code, err)
	}
	if code, err := u.Start(ctx, "https://mm.example.com/worker/v1/attempts/a/start?exp=1&sig=s", []byte("{}")); err != nil || code != 410 {
		t.Fatalf("%d %v", code, err)
	}
	if g := upCalls(); len(g) != 1 || g[0].Method != "PUT" || g[0].Digest != "sha-256=:AA==:" || g[0].Query != "exp=1&sig=s" {
		t.Errorf("%+v", g)
	}
	if g := stCalls(); len(g) != 1 || g[0].Method != "POST" {
		t.Errorf("%+v", g)
	}
	// a handler that writes without WriteHeader is a 200
	silent := fake.HandlerUploader{UploadHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }), StartHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	if code, _ := silent.Put(ctx, uploadBase+"a/x", nil, ""); code != 200 {
		t.Errorf("implicit 200: %d", code)
	}
	if code, _ := silent.Start(ctx, uploadBase+"a/x", nil); code != 200 {
		t.Errorf("empty handler: %d", code)
	}
	if _, err := u.Put(ctx, "://bad", nil, ""); err == nil {
		t.Error("bad URL")
	}
}

// A SimWorker over HandlerUploader drives real handlers end to end (the shape the property suite uses).
func TestSimWorker_OverHandlerUploader(t *testing.T) {
	t.Parallel()
	up, calls := recorder(http.StatusCreated)
	st, _ := recorder(http.StatusOK)
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: fake.HandlerUploader{UploadHandler: up, StartHandler: st}, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir()})
	res := sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref")
	if res.Failed {
		t.Fatalf("%+v", res)
	}
	g := calls()
	if len(g) != 6 || g[len(g)-1].Path != "/worker/v1/uploads/j-a1/mm-result.json" {
		t.Errorf("%d uploads, last %+v", len(g), g[len(g)-1])
	}
}

func TestSimWorker_WeightsMatch(t *testing.T) {
	t.Parallel()
	up := &recUploader{}
	sim := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Weights: yue2.Weights})
	if res := sim.Run(context.Background(), inputFor(t, spec("j", 1, "pop", 186)), "ref"); res.Failed {
		t.Errorf("pinned revisions match: %+v", res.Output.Error)
	}
	other := map[string]string{"m-a-p/YuE2-3B": yue2.Weights["m-a-p/YuE2-3B"]}
	sim2 := fake.NewSimWorker(fake.SimWorkerOptions{Uploader: up, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Weights: other})
	if res := sim2.Run(context.Background(), inputFor(t, spec("j", 2, "pop", 186)), "ref"); !res.Failed {
		t.Error("a different set of repositories is a mismatch")
	}
}

func TestSimWorker_PromptNoURLInMessages(t *testing.T) {
	t.Parallel()
	// every error path keeps URLs (which carry signatures) out of the job return
	up := &recUploader{putCode: func(string) int { return 500 }}
	sim := newSim(t, up)
	ts := spec("j", 1, "pop", 186)
	res := sim.Run(context.Background(), inputFor(t, ts), "ref")
	if !res.Failed || strings.Contains(string(res.Raw), "mm.example.com") || strings.Contains(string(res.Raw), "sig=") {
		t.Errorf("%s", res.Raw)
	}
}

func TestFake_WithoutWorker(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ref := mustSubmit(t, b, spec("j1", 1, "pop fake:fail=worker_error", 186))
	clk.Advance(10 * time.Second)
	if st := mustPoll(t, b, ref); st.State != provider.Failed || st.ErrorText == "" {
		t.Errorf("%+v", st)
	}
}

func TestFake_QueuedJobExpiresWithTTLEvenOnAColdWorker(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ts := spec("j", 1, "pop fake:cold=100", 186)
	ts.TTL = 10 * time.Second // expires while the worker is still cold
	ref := mustSubmit(t, b, ts)
	clk.Advance(time.Minute)
	if st := mustPoll(t, b, ref); st.State != provider.TimedOut || st.ExecutionMS != nil {
		t.Errorf("%+v", st)
	}
	ts2 := spec("k", 1, "pop", 186)
	ref2 := mustSubmit(t, b, ts2)
	clk.Advance(5 * time.Second)
	if st := mustPoll(t, b, ref2); st.State != provider.Queued && st.State != provider.Running && st.State != provider.Completed {
		t.Errorf("a cold-but-unused worker can serve the next job: %+v", st)
	}
}

func TestFake_TimeoutDefaultsWhenUnset(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ts := spec("j", 1, "pop", 0) // no timeout given: the endpoint default (15 min) applies
	ts.TTL = 0
	ref := mustSubmit(t, b, ts)
	clk.Advance(10 * time.Second)
	if st := mustPoll(t, b, ref); st.State != provider.Completed {
		t.Errorf("%+v", st)
	}
}

func TestFake_SecondStuckSubmitAddsNoWorker(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	mustSubmit(t, b, spec("j1", 1, "pop fake:stuck_worker", 186))
	mustSubmit(t, b, spec("j2", 1, "pop fake:stuck_worker", 186))
	clk.Advance(time.Second)
	if n := countStatus(workersOf(t, b), "RUNNING"); n != 1 {
		t.Errorf("one stuck worker per session, got %d", n)
	}
}

func TestParseMarkers_IgnoresGarbage(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	// invalid values and unknown tokens are plain style text
	ref := mustSubmit(t, b, spec("j", 1, "pop fake:slow=abc fake:nope fake:queue_expire=-3 fake:submit=teapot fake:cold=-1 FAKE:slow=9 fake:slow=1e9", 186))
	clk.Advance(10 * time.Second)
	if st := mustPoll(t, b, ref); st.State != provider.Completed || ms(st.ExecutionMS) != 1000 {
		t.Errorf("%+v", st)
	}
}
