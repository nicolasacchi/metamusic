package pods_test

import (
	"context"
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
}

func newEnv(t *testing.T, mod ...func(*envOpts)) *env {
	t.Helper()
	var eo envOpts
	for _, m := range mod {
		m(&eo)
	}
	key, dir, clk := canary("pod"), t.TempDir(), clocktest.NewManual(t0)
	writeKey(t, dir, key, 0o600)
	eo.srv.Key, eo.srv.Clock = key, clk
	srv := podtest.New(t, eo.srv)
	cl := pods.NewClient(pods.Options{BaseURL: srv.URL, KeyDir: dir, Clock: clk, UserAgent: pods.UserAgentFor("test"), Timeouts: eo.timeouts})
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
