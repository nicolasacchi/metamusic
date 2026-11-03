package transform_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/transform"
)

// TestHelperChild is the render child of the supervisor tests: the test binary re-executes itself and behaves as
// MM_TEST_CHILD says. It is a no-op in a normal test run.
func TestHelperChild(t *testing.T) {
	mode := os.Getenv("MM_TEST_CHILD")
	if mode == "" {
		return
	}
	dir, _ := os.Getwd() // the supervisor starts the child in its work dir
	switch mode {
	case "env":
		time.Sleep(300 * time.Millisecond) // the supervisor lowers the priority right after Start
		out := strings.Join(os.Environ(), "\n") + "\n" + describeProcess() + "\n"
		_ = os.WriteFile(filepath.Join(dir, "env.txt"), []byte(out), 0o600)
		os.Exit(0)
	case "progress":
		for _, p := range []int{50, 100, 150, 120, 5000, -3} { // a burst, a regression, out-of-range values
			fmt.Printf("{\"p\":%d,\"phase\":\"rendering\"}\n", p)
		}
		fmt.Println("not json")
		time.Sleep(450 * time.Millisecond)
		fmt.Println(`{"p":800,"phase":"finishing"}`)
		os.Exit(0)
	case "flood":
		line := strings.Repeat("x", 1000) + "\n"
		for i := 0; i < 3000; i++ { // 3 MB of output
			fmt.Print(line)
		}
		fmt.Println(`{"p":999,"phase":"rendering"}`)
		os.Exit(0)
	case "exit13":
		fmt.Fprint(os.Stderr, strings.Repeat("noise\n", 2000)+"final words\n")
		os.Exit(13)
	case "hang", "grandchild":
		if mode == "grandchild" {
			c := exec.Command(os.Getenv("MM_TEST_SLEEP"), "60") // the child has no PATH
			_ = c.Start()
			_ = os.WriteFile(filepath.Join(dir, "gpid"), []byte(strconv.Itoa(c.Process.Pid)), 0o600)
		}
		_ = os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(99)
}

type superRig struct {
	t   *testing.T
	dir string
	c   *transform.ExecChild
}

func newSuper(t *testing.T, mode string) *superRig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	extra := []string{"MM_TEST_CHILD=" + mode}
	if mode == "grandchild" {
		sleep, err := exec.LookPath("sleep")
		if err != nil {
			t.Skip("no sleep binary")
		}
		extra = append(extra, "MM_TEST_SLEEP="+sleep)
	}
	return &superRig{t: t, dir: dir, c: &transform.ExecChild{
		Self:     self,
		Args:     func(string) []string { return []string{"-test.run=^TestHelperChild$"} },
		ExtraEnv: extra,
	}}
}

func (s *superRig) run(ctx context.Context, memMB int, deadline time.Duration, progress transform.ChildProgress) (transform.ChildResult, error) {
	return s.c.Run(ctx, transform.ChildJob{Dir: s.dir, MemMB: memMB, Deadline: deadline}, progress)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestSupervisor_EnvMinimal: the child's environment is explicit and minimal (GOMEMLIMIT from the memory estimate, GOMAXPROCS=1,
// nothing inherited: no secrets, no config), it runs in its own process group at low priority with Pdeathsig, and its working
// directory is the work dir.
func TestSupervisor_EnvMinimal(t *testing.T) {
	t.Setenv("MM_SECRET_CANARY", "must-not-leak")
	t.Setenv("MM_DATA_DIR", "/nowhere")
	s := newSuper(t, "env")
	res, err := s.run(context.Background(), 800, 30*time.Second, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env, proc, _ := strings.Cut(string(b), "\n--process--\n")
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(env), "\n") {
		k, v, _ := strings.Cut(l, "=")
		got[k] = v
	}
	want := map[string]string{"GOMEMLIMIT": "1000MiB", "GOMAXPROCS": "1", "MM_TEST_CHILD": "env"}
	if len(got) != len(want) {
		t.Errorf("environment = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	checkProcess(t, proc)
	if res.CPU <= 0 {
		t.Errorf("CPU time was not reported: %v", res.CPU)
	}
}

// TestSupervisor_Progress: progress lines are parsed, clamped to 0..1000, made monotone, ignored when malformed, and throttled:
// a burst yields one callback, a later line another.
func TestSupervisor_Progress(t *testing.T) {
	s := newSuper(t, "progress")
	s.c.Throttle = 200 * time.Millisecond
	var mu sync.Mutex
	var got [][2]any
	res, err := s.run(context.Background(), 100, 30*time.Second, func(p int, phase string) {
		mu.Lock()
		got = append(got, [2]any{p, phase})
		mu.Unlock()
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != [2]any{50, "rendering"} || got[1] != [2]any{1000, "finishing"} {
		// 5000 clamps to 1000 and progress never goes back, so the late 800 reads 1000
		t.Errorf("callbacks = %v, want the first line of the burst and the late one (clamped, monotone)", got)
	}
}

// TestSupervisor_StdoutFlood: a child that prints megabytes never blocks the supervisor, and at most 64 KiB is parsed.
func TestSupervisor_StdoutFlood(t *testing.T) {
	s := newSuper(t, "flood")
	var n int
	res, err := s.run(context.Background(), 100, 30*time.Second, func(int, string) { n++ })
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("run: %+v %v", res, err)
	}
	if n != 0 {
		t.Errorf("a line past the 64 KiB cap was parsed (%d callbacks)", n)
	}
}

// TestSupervisor_ExitCodeAndStderrTail: a non-zero exit is a result, not an error, and the last 4 KiB of stderr is kept.
func TestSupervisor_ExitCodeAndStderrTail(t *testing.T) {
	s := newSuper(t, "exit13")
	res, err := s.run(context.Background(), 100, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("a normal exit is not an error: %v", err)
	}
	if res.ExitCode != 13 || !strings.Contains(res.StderrTail, "final words") || len(res.StderrTail) > 4096 {
		t.Errorf("result = code %d, tail %d bytes %q", res.ExitCode, len(res.StderrTail), res.StderrTail[max(0, len(res.StderrTail)-20):])
	}
}

// TestSupervisor_Deadline: past the wall deadline the whole process group is killed, grandchildren included.
func TestSupervisor_Deadline(t *testing.T) {
	s := newSuper(t, "grandchild")
	start := time.Now()
	res, err := s.run(context.Background(), 100, 600*time.Millisecond, nil)
	if !errors.Is(err, transform.ErrChildDeadline) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the deadline took %v", d)
	}
	_ = res
	waitFor(t, "the grandchild", func() bool { return fileExists(filepath.Join(s.dir, "gpid")) })
	b, _ := os.ReadFile(filepath.Join(s.dir, "gpid"))
	pid, _ := strconv.Atoi(string(b))
	waitFor(t, "the grandchild to die", func() bool { return !processAlive(pid) })
}

// TestSupervisor_RSSGuard: a child above 1.5 x the memory estimate + 128 MiB is killed (fake /proc reader).
func TestSupervisor_RSSGuard(t *testing.T) {
	s := newSuper(t, "hang")
	var calls int
	var mu sync.Mutex
	s.c.RSSPoll = 20 * time.Millisecond
	s.c.ReadRSS = func(pid int) (int64, bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls < 3 {
			return 200 * 1024, true // 200 MiB: under the limit of 1.5*100+128 = 278 MiB
		}
		return 279 * 1024, true
	}
	start := time.Now()
	_, err := s.run(context.Background(), 100, 30*time.Second, nil)
	if !errors.Is(err, transform.ErrChildMemory) {
		t.Fatalf("err = %v, want the memory guard", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the guard was slow")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 {
		t.Errorf("the guard fired on an under-limit reading (calls %d)", calls)
	}
}

// TestSupervisor_RSSNoProc: where /proc is not readable the guard is a no-op, not an error.
func TestSupervisor_RSSNoProc(t *testing.T) {
	s := newSuper(t, "progress")
	s.c.RSSPoll = 10 * time.Millisecond
	s.c.ReadRSS = func(int) (int64, bool) { return 0, false }
	if _, err := s.run(context.Background(), 100, 30*time.Second, nil); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisor_Cancel: a cancelled context kills the group within the grace and reports the context error.
func TestSupervisor_Cancel(t *testing.T) {
	s := newSuper(t, "grandchild")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.run(ctx, 100, 30*time.Second, nil); done <- err }()
	waitFor(t, "the child", func() bool { return fileExists(filepath.Join(s.dir, "ready")) })
	start := time.Now()
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > transform.CancelGrace {
		t.Errorf("killed after %v, grace %v", d, transform.CancelGrace)
	}
	b, _ := os.ReadFile(filepath.Join(s.dir, "gpid"))
	pid, _ := strconv.Atoi(string(b))
	waitFor(t, "the grandchild to die", func() bool { return !processAlive(pid) })
}

// TestSupervisor_Refusals: a relative work dir and a missing binary are refused without running anything.
func TestSupervisor_Refusals(t *testing.T) {
	c := &transform.ExecChild{Self: "/nonexistent/binary"}
	if _, err := c.Run(context.Background(), transform.ChildJob{Dir: "relative"}, nil); err == nil {
		t.Error("a relative dir was accepted")
	}
	if _, err := c.Run(context.Background(), transform.ChildJob{Dir: t.TempDir()}, nil); err == nil || strings.Contains(err.Error(), "/nonexistent") {
		t.Errorf("a missing binary must fail without naming a path: %v", err)
	}
	if _, err := (&transform.ExecChild{}).Run(context.Background(), transform.ChildJob{Dir: t.TempDir()}, nil); err == nil {
		t.Error("no binary was accepted")
	}
}
