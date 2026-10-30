package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/startup"
)

type healthBody struct {
	Status string              `json:"status"`
	Ready  bool                `json:"ready"`
	Issues []startup.StepIssue `json:"startup_issues"`
}

// doServe runs serve with a deadline so a fatal that wrongly degrades fails the test instead of hanging it.
func doServe(t *testing.T, env map[string]string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out, errb syncBuf
	code := run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, &errb)
	if ctx.Err() != nil {
		t.Fatalf("serve kept running instead of exiting: %s %s", out.String(), errb.String())
	}
	return code, out.String(), errb.String()
}

func getHealth(t *testing.T, addr string) (int, healthBody) {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var hb healthBody
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatalf("healthz body %q: %v", raw, err)
	}
	return resp.StatusCode, hb
}

// TestServe_DegradedWhenStartupSettleFails: a Settle that fails on every pass no longer ends serve (a crash loop with no HTTP).
// The server listens, /healthz stays 200 with status "degraded", ready=false and the failing step's code (never the error text),
// get_system_info carries the same issue, and once the fault clears the background retry settles and the issue goes away.
func TestServe_DegradedWhenStartupSettleFails(t *testing.T) {
	t.Parallel()
	env := libraryEnv(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	env["MM_ADDR"] = addr
	ctx, cancel := context.WithCancel(context.Background())
	b, err := boot(ctx, envFn(env), false)
	if err != nil {
		t.Fatal(err)
	}
	var failing atomic.Bool
	failing.Store(true)
	b.retryBase = 20 * time.Millisecond
	b.l1 = app.L1Options{FaultAfterPhaseA: func() error {
		if failing.Load() {
			return errors.New("injected /secret/path failure")
		}
		return nil
	}}
	var out, errb syncBuf
	done := make(chan error, 1)
	go func() { done <- serve(ctx, b, streams{strings.NewReader(""), &out, &errb}); b.close() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
	})
	select {
	case err := <-done:
		t.Fatalf("serve exited on a failing Settle: %v", err)
	case <-time.After(0):
	}
	if got := waitListening(&out, 60*time.Second); got != addr {
		t.Fatalf("server not listening while degraded: %q %s", got, errb.String())
	}
	var hb healthBody
	waitFor(t, "degraded healthz", 20*time.Second, func() bool {
		var code int
		code, hb = getHealth(t, addr)
		return code == 200 && hb.Status == "degraded" && len(hb.Issues) == 1 && hb.Issues[0].Attempts >= 2
	})
	if hb.Ready || hb.Issues[0].Step != "library_settle" || hb.Issues[0].Code != "SETTLE_FAILED" {
		t.Fatalf("degraded body: %+v", hb)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "injected") {
		t.Errorf("healthz leaks the error text: %s", raw)
	}
	if err := cmdHealthcheck(context.Background(), envFn(env), streams{strings.NewReader(""), &out, &errb}); err != nil {
		t.Errorf("healthcheck must stay healthy (liveness) while degraded: %v", err)
	}
	failing.Store(false)
	waitFor(t, "recovery", 30*time.Second, func() bool {
		code, hb := getHealth(t, addr)
		return code == 200 && hb.Status == "ok" && hb.Ready && len(hb.Issues) == 0
	})
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("queue = %d after recovery", n)
	}
}

// TestRunDerive_Steps: the step runner reports a failing step by code, skips the steps behind it, and a later success clears it.
func TestRunDerive_Steps(t *testing.T) {
	t.Parallel()
	h := startup.New(nil)
	fail := true
	var ran []string
	steps := []deriveStep{
		{name: "one", code: "ONE_FAILED", run: func(context.Context) error { ran = append(ran, "one"); return nil }},
		{name: "two", code: "TWO_FAILED", run: func(context.Context) error {
			ran = append(ran, "two")
			if fail {
				return errors.New("boom")
			}
			return nil
		}},
		{name: "three", code: "THREE_FAILED", run: func(context.Context) error { ran = append(ran, "three"); return nil }},
	}
	if runDerive(context.Background(), steps, h) {
		t.Fatal("a failing step must report not-ok")
	}
	if got := h.Issues(); len(got) != 1 || got[0].Step != "two" || got[0].Code != "TWO_FAILED" || strings.Join(ran, ",") != "one,two" {
		t.Fatalf("issues %+v ran %v", got, ran)
	}
	fail = false
	if !runDerive(context.Background(), steps, h) || !h.Ready() {
		t.Fatalf("recovery: %+v", h.Issues())
	}
}

// TestServe_TrueFatalsStayFatal: a bad config, missing secrets and an unmigratable database still end serve with an error and
// no listener; only derivation steps degrade.
func TestServe_TrueFatalsStayFatal(t *testing.T) {
	t.Parallel()
	t.Run("bad config", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t, "local")
		env["MM_AUTH"] = "nonsense"
		if code, _, errs := doServe(t, env); code == 0 || errs == "" {
			t.Fatalf("exit %d %q", code, errs)
		}
	})
	t.Run("missing secrets", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t, "local")
		env["MM_SECRETS_DIR"] = t.TempDir()
		if code, _, errs := doServe(t, env); code == 0 || errs == "" {
			t.Fatalf("exit %d %q", code, errs)
		}
	})
	t.Run("corrupt database", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t, "local")
		if err := os.WriteFile(filepath.Join(env["MM_DATA_DIR"], "metamusic.db"), []byte("this is not a sqlite database, not even close "+strings.Repeat("x", 4096)), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, errs := doServe(t, env); code == 0 || strings.Contains(out, "listening on") {
			t.Fatalf("exit %d %q %q", code, out, errs)
		}
	})
}
