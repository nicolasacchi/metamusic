package pods_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
)

// Canary values are built at run time (never literals: the gitleaks rule of C85).
func canary(name string) string { return "canary-" + name + "-" + strings.Repeat("x", 12) }

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

const (
	testDigest = "ghcr.io/example/mm-worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	gpu4090    = "NVIDIA GeForce RTX 4090"
	podName    = "mm-0190abcd1234"
)

type env struct {
	srv *podtest.Server
	dir string
	key string
	clk *clocktest.Manual
	cl  *pods.Client
}

func writeKey(t testing.TB, dir, key string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, "runpod-pod.env")
	if err := os.WriteFile(p, []byte("RUNPOD_"+"POD_KEY="+key+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

type envOpts struct {
	srv      podtest.Options
	timeouts pods.Timeouts
	// inproc routes the client to the fake's handler without a socket, so a test inside testing/synctest sees every
	// per-attempt timeout on the bubble's fake clock instead of the wall clock.
	inproc bool
}

// handlerTransport serves a request by calling the handler directly. The handler sees the caller's context, so a
// hung answer ends exactly when the client's per-attempt timeout does.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	sreq := req.Clone(req.Context())
	sreq.RequestURI = req.URL.RequestURI()
	sreq.RemoteAddr = "127.0.0.1:1"
	if sreq.Body == nil {
		sreq.Body = http.NoBody
	}
	done := make(chan struct{})
	var aborted bool
	go func() {
		defer close(done)
		defer func() { // the fake aborts a connection reset with http.ErrAbortHandler
			r := recover()
			if err, ok := r.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				aborted = true
			} else if r != nil {
				panic(r)
			}
		}()
		t.h.ServeHTTP(rec, sreq)
	}()
	select {
	case <-done:
		if aborted {
			return nil, io.ErrUnexpectedEOF
		}
		return rec.Result(), nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

func newEnv(t *testing.T, mod ...func(*envOpts)) *env {
	t.Helper()
	var eo envOpts
	for _, m := range mod {
		m(&eo)
	}
	key, dir, clk := canary("pod"), t.TempDir(), clocktest.NewManual(t0)
	writeKey(t, dir, key, 0o600)
	eo.srv.Key, eo.srv.Clock, eo.srv.NoListen = key, clk, eo.inproc
	srv := podtest.New(t, eo.srv)
	po := pods.Options{BaseURL: srv.URL, KeyDir: dir, Clock: clk, UserAgent: pods.UserAgentFor("test"), Timeouts: eo.timeouts}
	if eo.inproc {
		po.HTTP = &http.Client{Transport: handlerTransport{srv.Config.Handler}}
	}
	cl := pods.NewClient(po)
	return &env{srv: srv, dir: dir, key: key, clk: clk, cl: cl}
}

func body() pods.CreateBody {
	return pods.CreateBody{
		Name: podName, Image: testDigest, GPUTypeID: gpu4090, DiskGB: 40,
		Env: map[string]string{
			"MODE":                "agent",
			"MM_POLL_URL":         "https://mm.example.com/worker/v1/pods/PODID/poll?exp=1760018552&sig=" + canary("sig"),
			"MM_DEADLINE_UNIX":    "1760018552",
			"MM_DEADMAN_S":        "600",
			"MM_YUE2_3B_DIR":      "/opt/mm/pod-cache/hub",
			"MM_YUE2_3B_REVISION": "0123456789abcdef0123456789abcdef01234567",
		},
	}
}

func bg() context.Context { return context.Background() }

// drive advances the manual clock whenever something waits on a timer, until f returns.
func drive(t *testing.T, clk *clocktest.Manual, f func()) {
	t.Helper()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if clk.Pending() > 0 {
				clk.Advance(time.Second)
			} else {
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()
	f()
	close(done)
	wg.Wait()
}

func contextCancelled() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}
