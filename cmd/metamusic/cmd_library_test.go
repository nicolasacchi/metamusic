package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// libraryEnv builds a data directory holding the unsettled L1 fixture (the queue is non-empty).
func libraryEnv(t *testing.T) map[string]string {
	t.Helper()
	env := testEnv(t, "local")
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureL1(t, st, librarytest.FixedNow)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestCLI_LibraryReconcile(t *testing.T) {
	t.Parallel()
	env := libraryEnv(t)
	queue := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`)
	code, out, errs := do(t, env, "", "library", "reconcile", "--dry-run")
	if code != 0 || !strings.Contains(out, "queue: ") || !strings.Contains(out, "track=") || !strings.Contains(out, "draft=") || !strings.Contains(out, "nothing written") {
		t.Fatalf("dry-run: %d %q %q", code, out, errs)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("a dry run wrote %d Songs", n)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n != queue {
		t.Errorf("a dry run changed the queue: %d -> %d", queue, n)
	}
	code, out, errs = do(t, env, "", "library", "reconcile")
	if code != 0 || !strings.Contains(out, "reconciled:") {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("queue = %d after reconcile", n)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM songs`); n == 0 {
		t.Errorf("no Songs after reconcile")
	}
}

func TestCLI_LibraryVerify(t *testing.T) {
	t.Parallel()
	env := libraryEnv(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %s %s", code, out, errs)
	}
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "consistent") {
		t.Fatalf("verify on a settled library: %d %q %q", code, out, errs)
	}
	// plant a fault: a take inserted without a reconcile has no Song
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	sc := librarytest.NewScene(t, st, librarytest.FixedNow)
	sc.Track(sc.Recipe("planted_v1", "pop"), "Planted", "", sc.At(0))
	st.Close()
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(out, "track_without_song") {
		t.Errorf("verify on a planted fault: %d %q %q", code, out, errs)
	}
}

// TestCLI_ReconcileWhileLeaseHeld: with a live lease held by another process the command prints the queue, exits 0 and
// writes nothing; --wait returns once the other settler has drained the queue.
func TestCLI_ReconcileWhileLeaseHeld(t *testing.T) {
	t.Parallel()
	env := libraryEnv(t)
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.W.Exec(`UPDATE provenance_lease SET owner = 'server', expires_at = '2999-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	code, out, errs := do(t, env, "", "library", "reconcile")
	if code != 0 || !strings.Contains(out, "another process holds the settle lease") || !strings.Contains(out, "track=") {
		t.Fatalf("held lease: %d %q %q", code, out, errs)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("a held lease must write nothing: %d Songs", n)
	}
	// --wait: poll the command's own output until it reports the held lease, then let the other settler drain the queue
	var wout, werr syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{"library", "reconcile", "--wait"}, envFn(env), strings.NewReader(""), &wout, &werr)
	}()
	deadline := time.Now().Add(120 * time.Second)
	for !strings.Contains(wout.String(), "another process holds the settle lease") {
		if time.Now().After(deadline) {
			t.Fatalf("--wait never reported the held lease: %q %q", wout.String(), werr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case c := <-done:
		t.Fatalf("--wait returned %d while the queue was still full", c)
	case <-time.After(400 * time.Millisecond):
	}
	// the other settler (the server) drains the queue under its own owner name
	srv := provenance.New(provenance.Options{Store: st, Now: time.Now, Owner: "server"})
	if _, err := srv.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-done:
		if c != 0 || !strings.Contains(wout.String(), "queue: empty") {
			t.Errorf("--wait: exit %d, output %q", c, wout.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("--wait did not return after the queue drained")
	}
}

// TestCLI_ImportSettles: a CLI seed-synthetic (the import path) leaves an empty queue and a derived model.
func TestCLI_ImportSettles(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(out, "Library model:") {
		t.Errorf("no library model line:\n%s", out)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("queue = %d after the import", n)
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM songs WHERE retired_at IS NULL`); n == 0 {
		t.Errorf("no Songs after the import")
	}
	if code, out, errs := do(t, env, "", "library", "verify"); code != 0 {
		t.Errorf("verify after an import: %d %s %s", code, out, errs)
	}
}

// TestServe_SettlesBeforeListening: with an unsettled library the address does not accept a connection while the first
// Settle is held; once released the order is seed -> Settle -> listening, and the server answers.
func TestServe_SettlesBeforeListening(t *testing.T) {
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
	held, release := make(chan struct{}), make(chan struct{})
	b.l1 = app.L1Options{BeforeSettle: func(context.Context) {
		select {
		case held <- struct{}{}:
			<-release
		default:
		}
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
	case <-held:
	case <-time.After(60 * time.Second):
		t.Fatalf("the startup Settle never started: %s %s", out.String(), errb.String())
	}
	if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the address accepted a connection while the first Settle was held")
	}
	if strings.Contains(out.String(), "listening on") {
		t.Fatal("the server announced itself before the first Settle finished")
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n == 0 {
		t.Error("the queue must still be non-empty while the Settle is held")
	}
	close(release)
	if got := waitListening(&out, 60*time.Second); got != addr {
		t.Fatalf("server did not start after the release: %q (want %s) %s", got, addr, errb.String())
	}
	if n := dbRow(t, env, `SELECT COUNT(*) FROM provenance_dirty`); n != 0 {
		t.Errorf("queue = %d when the listener opened", n)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
}
