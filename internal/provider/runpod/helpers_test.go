package runpod_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/runpodtest"
)

// Canary values are built at runtime (never literals: the gitleaks rule of C85).
func canary(name string) string { return "canary-" + name + "-" + strings.Repeat("x", 12) }

var t0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type env struct {
	srv   *runpodtest.Server
	keys  config.RunPodKeys
	set   runpodtest.KeySet
	clk   *clocktest.Manual
	cl    *runpod.Client
	epRaw string
}

type envOpts struct {
	srv     runpodtest.Options
	client  func(*runpod.ClientOptions)
	noWrite bool
}

func newEnv(t *testing.T, mod ...func(*envOpts)) *env {
	t.Helper()
	var eo envOpts
	for _, m := range mod {
		m(&eo)
	}
	set := runpodtest.KeySet{Jobs: canary("jobs"), Read: canary("read"), Write: canary("write"), Admin: canary("admin")}
	if eo.noWrite {
		set.Write = canary("unused-write")
	}
	ep := canary("endpoint")
	clk := clocktest.NewManual(t0)
	eo.srv.Keys, eo.srv.EndpointID, eo.srv.Clock = set, ep, clk
	srv := runpodtest.New(t, eo.srv)
	keys := config.RunPodKeys{Jobs: set.Jobs, Read: set.Read, Write: set.Write, EndpointID: ep}
	if eo.noWrite {
		keys.Write = ""
	}
	co := runpod.ClientOptions{MgmtBase: srv.URL, JobsBase: srv.URL, Keys: keys, Clock: clk, UserAgent: runpod.UserAgentFor("test")}
	if eo.client != nil {
		eo.client(&co)
	}
	return &env{srv: srv, keys: keys, set: set, clk: clk, cl: runpod.NewClient(co), epRaw: ep}
}

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

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bg() context.Context { return context.Background() }

func adminKey(t *testing.T, v string) config.AdminKey {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runpod-admin.env")
	if err := os.WriteFile(path, []byte("RUNPOD_ADMIN_KEY="+v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := config.LoadAdminKey(path)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
