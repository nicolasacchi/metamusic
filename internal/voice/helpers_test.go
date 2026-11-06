package voice_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}}
	device = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "Phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent  = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

type env struct {
	st     *store.Store
	root   *os.Root
	dir    string
	judg   *judgments.Service
	svc    *voice.Service
	signer *serve.Signer
	now    time.Time
}

// newEnv builds the services over a fixture library and the given ffmpeg binary ("" => the pinned one).
func newEnv(t *testing.T, ffmpeg string) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, dir: t.TempDir(), now: lt.FixedNow.Add(time.Hour)}
	lt.Fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, e.dir)
	if e.root, err = os.OpenRoot(e.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	clock := func() time.Time { return e.now }
	var n atomic.Int64
	n.Store(0x7000)
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1)) }
	lib := library.New(st, e.root, clock)
	e.judg = judgments.New(st, lib, clock, newID)
	e.signer = serve.NewSigner([]byte("synthetic-test-key-0123456789abcdef"), clock)
	if ffmpeg == "" {
		ffmpeg = testtools.Tools().FFmpeg
	}
	e.svc = voice.New(voice.Options{Store: st, Data: e.root, DataDir: e.dir, FFmpeg: ffmpeg, Signer: e.signer, Now: clock, NewID: newID})
	e.judg.UseVoice(e.svc)
	return e
}

// sine renders a synthetic tone with the pinned ffmpeg: no recording ever enters the repository.
func sine(t *testing.T, seconds float64, codecArgs ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fx")
	args := []string{"-hide_banner", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=" + strconv.FormatFloat(seconds, 'f', -1, 64), "-ac", "1"}
	args = append(args, codecArgs...)
	args = append(args, out)
	if _, err := mediatool.Run(context.Background(), testtools.Tools().FFmpeg, args, mediatool.RunOpts{Timeout: 60 * time.Second, MaxStdout: 1 << 20}); err != nil {
		t.Fatalf("render fixture: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// fixtures is the container matrix of D-M5-23, generated at test time.
func fixtures(t *testing.T) map[string]struct {
	data             []byte
	container, codec string
} {
	t.Helper()
	type fx = struct {
		data             []byte
		container, codec string
	}
	return map[string]fx{
		"webm-opus":  {sine(t, 2, "-c:a", "libopus", "-b:a", "32k", "-f", "webm"), "webm", "opus"},
		"ogg-opus":   {sine(t, 2, "-c:a", "libopus", "-b:a", "32k", "-f", "ogg"), "ogg", "opus"},
		"ogg-vorbis": {sine(t, 2, "-c:a", "libvorbis", "-f", "ogg"), "ogg", "vorbis"},
		"mp4-aac":    {sine(t, 2, "-c:a", "aac", "-b:a", "48k", "-f", "mp4"), "mp4", "aac"},
		"mp4-opus":   {sine(t, 2, "-c:a", "libopus", "-f", "mp4"), "mp4", "opus"},
	}
}

// writeFakeTool writes a shell script tool and returns once it can be exec'd. A parallel test's fork may have inherited
// the script's write descriptor and holds it until its own exec, which fails ours with "text file busy"; the window
// closes for good once that child has exec'd (the descriptor is closed here, so no later fork inherits it). The probe
// runs exit before body, so a logging fake records no probe call.
func writeFakeTool(t testing.TB, path, body string) {
	t.Helper()
	script := "#!/bin/sh\n[ -n \"$MM_FAKE_TOOL_PROBE\" ] && exit 0\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		cmd := exec.Command(path) //nolint:gosec // the fake tool this test just wrote
		cmd.Env = append(os.Environ(), "MM_FAKE_TOOL_PROBE=1")
		err := cmd.Run()
		if err == nil || !errors.Is(err, syscall.ETXTBSY) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake tool %s stays busy: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
