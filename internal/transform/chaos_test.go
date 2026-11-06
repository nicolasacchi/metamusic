package transform_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/testtools"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

// The chaos suite of T1 (C216.5), media tier: the real render child, the real ffmpeg and flac, the real M1 ingest. Each test
// breaks one thing at one moment and checks the same three invariants: the row ends where the contract says, no Take exists
// unless the row is done, and no work dir, takes/<id> directory or master file is left behind.

// chaosRig is d5Rig with the child chosen by the test.
func chaosRig(t *testing.T, tools mediatool.Tools, child func(o *transform.Options) transform.ChildRunner, mod ...func(*transform.Options)) *rig {
	t.Helper()
	r := newRig(t, false, func(o *transform.Options) {
		an, err := ffmpeg.New(context.Background(), tools)
		if err != nil {
			t.Fatal(err)
		}
		o.Media = transform.ToolMedia{Tools: tools}
		o.Tools = tools
		o.Pipeline = ingest.New(o.Data, o.DataDir, tools, an, o.Now)
		o.Ears = &fakeEars{}
		o.Child = child(o)
		for _, m := range mod {
			m(o)
		}
	})
	installMaster(t, r, tools, "A", 120, 1)
	installMaster(t, r, tools, "B", 120, 2)
	return r
}

const chaosGlacier = `{"factor":20,"start_s":0,"length_s":3,"window_s":0.5,"fade_in_s":0.5,"fade_out_s":0.5}`

// chaosChild is the real child with an unthrottled progress relay: a fast machine finishes the whole render in a second or two,
// and a throttled relay would deliver nothing between the first and the last report.
func chaosChild(bin string) *transform.ExecChild {
	return &transform.ExecChild{Self: bin, Throttle: time.Millisecond}
}

// queueChaos queues a small glacier of A; tag makes its spec unique.
func queueChaos(t *testing.T, r *rig, tag string) string {
	t.Helper()
	return queueD5(t, r, transform.KindGlacier, []string{"A"}, strings.Replace(chaosGlacier, `"start_s":0`, `"start_s":0, "tag":"`+tag+`"`, 1), nil)
}

// noTrace asserts the invariants of a transform that did not finish: no Take, no work dir, no takes/<id>, and no master file
// beyond the fixture's own.
func (r *rig) noTrace(id string, mastersBefore int) {
	r.t.Helper()
	if n := r.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id); n != 0 {
		r.t.Errorf("a Take exists for the unfinished transform %s", id)
	}
	for _, p := range []string{"incoming/transform-" + id, "takes/" + id} {
		if r.exists(p) {
			r.t.Errorf("%s was left behind", p)
		}
	}
	if got := r.masterFiles(); got != mastersBefore {
		r.t.Errorf("%d master files, want %d: an unreferenced master was left behind", got, mastersBefore)
	}
}

// masterFiles counts the files under audio/masters.
func (r *rig) masterFiles() int {
	ents, _ := os.ReadDir(filepath.Join(r.dir, "audio", "masters"))
	n := 0
	for _, e := range ents {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// killChild SIGKILLs the dsp-run process working in dir; it reports whether one was found.
func killChild(dir string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !strings.Contains(string(b), "dsp-run") || !strings.Contains(string(b), dir) {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err == nil && syscall.Kill(pid, syscall.SIGKILL) == nil {
			return true
		}
	}
	return false
}

// interrupter wraps a ChildRunner and, on its first n runs, calls hit the first time the child reports at least `at` permille.
type interrupter struct {
	inner transform.ChildRunner
	at    int
	n     int32
	hit   func(dir string) bool // true when it did what it was meant to (a kill that found no process did not)
	runs  atomic.Int32
	fired atomic.Int32
}

func (w *interrupter) Run(ctx context.Context, j transform.ChildJob, progress transform.ChildProgress) (transform.ChildResult, error) {
	arm := w.runs.Add(1) <= w.n
	var once sync.Once
	return w.inner.Run(ctx, j, func(p int, phase string) {
		if progress != nil {
			progress(p, phase)
		}
		if arm && p >= w.at {
			once.Do(func() {
				if w.hit(j.Dir) {
					w.fired.Add(1)
				}
			})
		}
	})
}

// weeklyOnly skips a test of the weekly tier: too slow for a PR shard (the go shards run close to their timeout), run with
// MM_T1_WEEKLY=1 (or MM_T1_CHAOS_FULL=1, the weekly chaos switch) in the weekly workflow.
func weeklyOnly(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("MM_T1_WEEKLY") != "1" && os.Getenv("MM_T1_CHAOS_FULL") != "1" {
		t.Skip("weekly tier (MM_T1_WEEKLY=1): " + why)
	}
}

var chaosPoints = func() []int {
	if os.Getenv("MM_T1_CHAOS_FULL") == "1" { // the weekly run: ten points
		return []int{0, 100, 200, 300, 400, 500, 600, 700, 800, 900}
	}
	return []int{0, 400, 800} // the PR subset
}()

// TestChaos_KillChildAtProgress: the render child dies at ten points (three in the PR subset, ten with MM_T1_CHAOS_FULL=1) of its progress, by SIGKILL from outside (what the kernel's
// OOM killer does) and by the runner stopping (the server shutting down). Contract:
//   - SIGKILL from outside is an interruption (only the supervisor's own RSS guard gives TRANSFORM_MEMORY_EXCEEDED): the row goes
//     back to queued at once and the retry completes; a second one fails it INTERRUPTED;
//   - a stopping runner kills the child and puts the row back to queued; the retry completes; two interruptions fail it INTERRUPTED;
//   - in every case no Take, work dir, take directory or stray master survives an unfinished attempt.
func TestChaos_KillChildAtProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real child and tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)

	t.Run("sigkill", func(t *testing.T) {
		killed := 0
		for _, at := range chaosPoints {
			t.Run(fmt.Sprintf("p=%d", at), func(t *testing.T) {
				var w *interrupter
				r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
					w = &interrupter{inner: chaosChild(bin), at: at, n: 1, hit: killChild}
					return w
				})
				before := r.masterFiles()
				id := queueChaos(t, r, "sigkill")
				if !r.runOne() {
					t.Fatal("nothing claimed")
				}
				row := r.row(id)
				if w.fired.Load() == 0 {
					t.Skipf("the render finished before reaching %d permille", at) // a short render may not report this point
				}
				killed++
				if row.State != "queued" || row.Attempts != 1 || row.ErrCode != "" {
					t.Fatalf("row after an external SIGKILL = %+v, want queued after 1 attempt", row)
				}
				r.noTrace(id, before)
				if !r.runOne() || r.row(id).State != "done" {
					t.Fatalf("the retry did not finish: %+v", r.row(id))
				}
			})
		}
		if killed < (len(chaosPoints)+1)/2 {
			t.Errorf("only %d of %d points were reached: the test is vacuous", killed, len(chaosPoints))
		}
	})

	t.Run("sigkill_twice", func(t *testing.T) {
		var w *interrupter
		r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
			w = &interrupter{inner: chaosChild(bin), at: 100, n: 2, hit: killChild}
			return w
		})
		before := r.masterFiles()
		id := queueChaos(t, r, "sigkill2")
		r.runOne()
		if w.fired.Load() != 1 || r.row(id).State != "queued" {
			t.Fatalf("first kill: fired %d, row %+v", w.fired.Load(), r.row(id))
		}
		r.runOne()
		if row := r.row(id); w.fired.Load() != 2 || row.State != "failed" || row.ErrCode != transform.ErrInterrupted || !row.CPU.Valid {
			t.Fatalf("after two kills: fired %d, row %+v, want failed INTERRUPTED with cpu_ms recorded", w.fired.Load(), row)
		}
		r.noTrace(id, before)
	})

	t.Run("runner_stop", func(t *testing.T) {
		stopped := 0
		for _, at := range chaosPoints {
			t.Run(fmt.Sprintf("p=%d", at), func(t *testing.T) {
				ctx, stop := context.WithCancel(context.Background())
				defer stop()
				var w *interrupter
				r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
					w = &interrupter{inner: chaosChild(bin), at: at, n: 1, hit: func(string) bool { stop(); return true }}
					return w
				})
				before := r.masterFiles()
				id := queueChaos(t, r, "stop")
				_, _ = r.svc.RunOne(ctx)
				if w.fired.Load() == 0 {
					t.Skipf("the render finished before reaching %d permille", at)
				}
				stopped++
				// a stopping runner leaves the row running under its lease (C210.3); Recover puts it back once the lease is over
				if row := r.row(id); row.State != "running" || row.Attempts != 1 {
					t.Fatalf("row after a stopped runner = %+v, want running with 1 attempt", row)
				}
				if r.exists("incoming/transform-" + id) {
					t.Error("the work dir survived the stop")
				}
				r.requeue(id)
				r.noTrace(id, before)
				if !r.runOne() || r.row(id).State != "done" {
					t.Fatalf("the retry did not finish: %+v", r.row(id))
				}
				if n := r.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id); n != 1 {
					t.Errorf("%d Takes after the retry", n)
				}
			})
		}
		if stopped < (len(chaosPoints)+1)/2 {
			t.Errorf("only %d points reached", stopped)
		}
	})

	t.Run("twice_fails_interrupted", func(t *testing.T) {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		var w *interrupter
		r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
			w = &interrupter{inner: chaosChild(bin), at: 100, n: 2, hit: func(string) bool { stop(); return true }}
			return w
		})
		before := r.masterFiles()
		id := queueChaos(t, r, "twice")
		_, _ = r.svc.RunOne(ctx)
		if w.fired.Load() != 1 || r.row(id).State != "running" {
			t.Fatalf("first interruption: fired %d, row %+v", w.fired.Load(), r.row(id))
		}
		r.requeue(id)
		ctx2, stop2 := context.WithCancel(context.Background())
		defer stop2()
		w.hit = func(string) bool { stop2(); return true }
		_, _ = r.svc.RunOne(ctx2)
		if w.fired.Load() != 2 {
			t.Fatalf("second interruption did not fire: %d", w.fired.Load())
		}
		r.clk.add(transform.LeaseTTL + time.Second)
		if err := r.svc.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if row := r.row(id); row.State != "failed" || row.ErrCode != transform.ErrInterrupted || !row.CPU.Valid {
			t.Fatalf("after two interruptions: %+v, want failed INTERRUPTED with cpu_ms recorded", row)
		}
		r.noTrace(id, before)
	})
}

// TestChaos_ServerRestartMidRender: the runner is stopped in the middle of a real render (the process is shutting down), a new
// service over the same data (a restarted process) sweeps and runs: exactly one Take, one master, no leftovers.
func TestChaos_ServerRestartMidRender(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real child and tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
		return &interrupter{inner: chaosChild(bin), at: 100, n: 1, hit: func(string) bool { stop(); return true }}
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "restart")
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }() // the real loop, not RunOne
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the runner did not stop")
	}
	if row := r.row(id); row.State != "running" {
		t.Fatalf("row after the stop = %+v", row)
	}
	// "restart": a new service, same store and data, startup recovery and sweep, then the loop
	an, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	second := transform.New(transform.Options{Store: r.st, Library: r.lib, Now: r.clk.now, NewID: func() string { r.n++; return fmt.Sprintf("tx-r%03d", r.n) },
		Config: testConfig(), Pipeline: ingest.New(r.data, r.dir, tools, an, r.clk.now), Media: transform.ToolMedia{Tools: tools}, Tools: tools,
		Child: &transform.ExecChild{Self: bin}, Data: r.data, DataDir: r.dir, Owner: "runner-b", Ears: &fakeEars{}})
	r.clk.add(transform.LeaseTTL + time.Second) // the dead process's lease runs out
	if err := second.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.noTrace(id, before)
	if ok, err := second.RunOne(context.Background()); err != nil || !ok {
		t.Fatalf("restart run: %v %v", ok, err)
	}
	if row := r.row(id); row.State != "done" {
		t.Fatalf("row after the restart = %+v", row)
	}
	if n := r.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id); n != 1 {
		t.Errorf("%d Takes, want exactly one", n)
	}
	if r.masterFiles() != before+1 {
		t.Errorf("%d master files, want %d", r.masterFiles(), before+1)
	}
	if ok, _ := second.RunOne(context.Background()); ok {
		t.Error("the restart found more work")
	}
}

// requeue lets a dead runner's lease run out and has Recover and the sweep put the transform back, as a restart does.
func (r *rig) requeue(id string) {
	r.t.Helper()
	r.clk.add(transform.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil {
		r.t.Fatal(err)
	}
	if err := r.svc.Sweep(context.Background()); err != nil {
		r.t.Fatal(err)
	}
	if row := r.row(id); row.State != "queued" {
		r.t.Fatalf("after recover: %+v", row)
	}
}

// failingMedia and failingIngest fail one step of the bridge with a full disk.
type failingMedia struct {
	transform.Media
	encode bool
}

func (m failingMedia) Encode(ctx context.Context, pcm, out string, rate int) (time.Duration, error) {
	if m.encode {
		// a partial output first, as a full disk leaves it
		_ = os.WriteFile(out, []byte("partial"), 0o600)
		return 0, fmt.Errorf("write %s: %w", out, syscall.ENOSPC)
	}
	return m.Media.Encode(ctx, pcm, out, rate)
}

type failingIngest struct {
	transform.Ingester
	root string
}

func (f failingIngest) Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error) {
	// M1 removes its own partial output when it fails, so a failure it reports leaves nothing; the crash inside it is the
	// B3 window test below
	return ingest.Result{}, &ingest.Error{Code: ingest.CodeIO, Detail: "write " + f.root + "/audio/masters/x.flac: " + syscall.ENOSPC.Error()}
}

// TestChaos_DiskFullAtEncode and _AtIngest: a full disk while encoding the result, or while the freeze writes the master, fails
// the row with a message that names no path; the attempt leaves no work dir, no take directory and no master file behind.
func TestChaos_DiskFullAtEncode(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return &transform.ExecChild{Self: bin} }, func(o *transform.Options) {
		o.Media = failingMedia{Media: transform.ToolMedia{Tools: tools}, encode: true}
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "enc")
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	row := r.row(id)
	if row.State != "failed" || row.ErrCode == "" {
		t.Fatalf("row = %+v", row)
	}
	assertNoPaths(t, r, row.ErrText)
	r.noTrace(id, before)
}

func TestChaos_DiskFullAtIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return &transform.ExecChild{Self: bin} }, func(o *transform.Options) {
		o.Pipeline = failingIngest{Ingester: o.Pipeline, root: o.DataDir}
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "ing")
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || !strings.HasPrefix(row.ErrCode, transform.ErrIngestFailedPrefix) {
		t.Fatalf("row = %+v", row)
	}
	assertNoPaths(t, r, row.ErrText)
	r.noTrace(id, before)
}

func assertNoPaths(t *testing.T, r *rig, text string) {
	t.Helper()
	for _, bad := range []string{r.dir, "/tmp/", "/var/", "/home/", "audio/masters"} {
		if strings.Contains(text, bad) {
			t.Errorf("error text %q names %q", text, bad)
		}
	}
	if strings.Contains(text, "incoming/") {
		t.Errorf("error text %q shows the data-dir layout", text)
	}
}

// TestChaos_ParentRowTampered: a parent whose master sha no longer matches what the transform froze fails the transform
// INPUT_CHANGED before anything is decoded or written.
func TestChaos_ParentRowTampered(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	var calls atomic.Int32
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
		return &interrupter{inner: chaosChild(bin), at: 1 << 20, n: 0, hit: func(string) bool { calls.Add(1); return true }}
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "tamper")
	r.exec(`DROP TRIGGER tracks_frozen`) // scratch library only: this is the corruption the alarm exists for
	r.exec(`UPDATE tracks SET master_sha256 = ? WHERE id = ?`, strings.Repeat("0", 64), r.fx["A"])
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrInputChanged {
		t.Fatalf("row = %+v, want failed INPUT_CHANGED", row)
	}
	r.noTrace(id, before)
}

// shellChild runs /bin/sh -c script as the render child, with a deadline of its own.
func shellChild(script string, deadline time.Duration, mem func(pid int) (int64, bool)) transform.ChildRunner {
	return deadlineChild{inner: &transform.ExecChild{Self: "/bin/sh", Args: func(string) []string { return []string{"-c", script} }, ReadRSS: mem, RSSPoll: 20 * time.Millisecond}, d: deadline}
}

type deadlineChild struct {
	inner transform.ChildRunner
	d     time.Duration
}

func (c deadlineChild) Run(ctx context.Context, j transform.ChildJob, p transform.ChildProgress) (transform.ChildResult, error) {
	if c.d > 0 {
		j.Deadline = c.d
	}
	return c.inner.Run(ctx, j, p)
}

// cmdlineAlive reports whether a process whose command line holds marker exists.
func cmdlineAlive(marker string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && strings.Contains(string(b), marker) {
			return true
		}
	}
	return false
}

// TestChaos_ChildHangs: a child that ignores SIGTERM, SIGINT and SIGHUP and never exits is killed at the wall deadline; the row
// fails TRANSFORM_TIMEOUT and the whole process group is gone.
func TestChaos_ChildHangs(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	marker := fmt.Sprintf("chaos-hang-%d", time.Now().UnixNano())
	script := "trap '' TERM INT HUP; while :; do sleep 1; done # " + marker
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return shellChild(script, 1500*time.Millisecond, nil) })
	before := r.masterFiles()
	id := queueChaos(t, r, "hang")
	t0 := time.Now()
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrTimeout {
		t.Fatalf("row = %+v, want failed TRANSFORM_TIMEOUT", row)
	}
	if d := time.Since(t0); d > 20*time.Second {
		t.Errorf("the hung child held the runner for %v", d)
	}
	if cmdlineAlive(marker) {
		t.Error("the hung child's process group survived the deadline")
	}
	r.noTrace(id, before)
}

// TestChaos_ChildMemoryBomb: a child whose resident set passes the estimate plus the guard's slack is killed; the row fails
// TRANSFORM_MEMORY_EXCEEDED, the server process is unaffected (this test goes on to run another transform).
func TestChaos_ChildMemoryBomb(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	marker := fmt.Sprintf("chaos-bomb-%d", time.Now().UnixNano())
	var bomb atomic.Bool
	bomb.Store(true)
	exec := &transform.ExecChild{Self: bin}
	bombChild := shellChild("sleep 120 # "+marker, 0, func(int) (int64, bool) { return 64 << 20, true }) // 64 GiB, in KiB
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner {
		return childFunc(func(ctx context.Context, j transform.ChildJob, p transform.ChildProgress) (transform.ChildResult, error) {
			if bomb.Load() {
				return bombChild.Run(ctx, j, p)
			}
			return exec.Run(ctx, j, p)
		})
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "bomb")
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrMemoryExceeded {
		t.Fatalf("row = %+v, want failed TRANSFORM_MEMORY_EXCEEDED", row)
	}
	if cmdlineAlive(marker) {
		t.Error("the bomb's process survived the guard")
	}
	r.noTrace(id, before)
	bomb.Store(false)
	next := queueChaos(t, r, "after-bomb")
	if !r.runOne() || r.row(next).State != "done" {
		t.Errorf("the runner did not survive the bomb: %+v", r.row(next))
	}
}

type childFunc func(ctx context.Context, j transform.ChildJob, p transform.ChildProgress) (transform.ChildResult, error)

func (f childFunc) Run(ctx context.Context, j transform.ChildJob, p transform.ChildProgress) (transform.ChildResult, error) {
	return f(ctx, j, p)
}

// TestChaos_LeaseExpiry: a runner dies after the child finished (the crash is injected after each step in turn) and its lease
// runs out; Recover puts the row back, the sweep removes the dead attempt's files, a second runner completes it once, and the
// dead owner's late write finds the row is not its own.
func TestChaos_LeaseExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	weeklyOnly(t, "the PR chaos subset is the kill, restart and hang tests (the lease logic is covered by the fake-child runner tests)")
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	points := []string{transform.FaultAfterChild, transform.FaultAfterIngest}
	if os.Getenv("MM_T1_CHAOS_FULL") == "1" {
		points = append(points, transform.FaultAfterDecode, transform.FaultAfterEncode)
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			crash := true
			r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return &transform.ExecChild{Self: bin} }, func(o *transform.Options) {
				o.Fault = func(p string) error {
					if crash && p == point {
						return context.Canceled
					}
					return nil
				}
			})
			before := r.masterFiles()
			id := queueChaos(t, r, "lease-"+point)
			r.runOne()
			if row := r.row(id); row.State != "running" || row.Owner != "runner-a" {
				t.Fatalf("a crash must leave the row running under its owner: %+v", row)
			}
			crash = false
			// nothing recovers it before the lease runs out
			if err := r.svc.Recover(context.Background()); err != nil || r.row(id).State != "running" {
				t.Fatalf("recovered a live lease: %v %+v", err, r.row(id))
			}
			r.clk.add(transform.LeaseTTL + time.Second)
			if err := r.svc.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := r.svc.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if row := r.row(id); row.State != "queued" || row.Owner != "" {
				t.Fatalf("after recovery: %+v", row)
			}
			if got := r.masterFiles(); got != before {
				t.Errorf("%d master files after recover and sweep, want %d", got, before)
			}
			if !r.runOne() || r.row(id).State != "done" {
				t.Fatalf("the retry did not finish: %+v", r.row(id))
			}
			if n := r.count(`SELECT COUNT(*) FROM tracks WHERE id = ?`, id); n != 1 {
				t.Errorf("%d Takes", n)
			}
			if got := r.masterFiles(); got != before+1 {
				t.Errorf("%d master files after the retry, want %d", got, before+1)
			}
		})
	}
}

// TestChaos_OrphanMasterCrashWindow: the crash window of B3 between the freeze (the master and take files exist) and the write of
// the journal that names them. Nothing then says which files belong to the attempt. After the lease expires, Recover, Sweep and the
// retry, the library must hold exactly one master for the transform and no master file that no Take owns: an orphan must be
// attributable or harmless.
func TestChaos_OrphanMasterCrashWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos suite: real tools")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin := childBinary(t)
	crash := true
	var ig windowIngest
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return &transform.ExecChild{Self: bin} }, func(o *transform.Options) {
		ig = windowIngest{Ingester: o.Pipeline, root: o.DataDir, armed: &crash}
		o.Pipeline = ig
		o.Fault = func(p string) error {
			if crash && p == transform.FaultAfterIngest {
				return context.Canceled
			}
			return nil
		}
	})
	before := r.masterFiles()
	id := queueChaos(t, r, "window")
	r.runOne()
	if row := r.row(id); row.State != "running" {
		t.Fatalf("a crash must leave the row running: %+v", row)
	}
	if fi, err := os.Stat(filepath.Join(r.dir, "incoming", "transform-"+id, "claimed.json")); r.masterFiles() != before+1 || err != nil || !fi.IsDir() {
		t.Fatalf("the window was not reproduced: %d masters (want %d), journal state %v %v", r.masterFiles(), before+1, fi, err)
	}
	crash = false
	r.clk.add(transform.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.runOne() || r.row(id).State != "done" {
		t.Fatalf("the retry did not finish: %+v", r.row(id))
	}
	owned := map[string]bool{}
	rows, err := r.st.R.Query(`SELECT master_path FROM tracks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		owned[filepath.Base(p)] = true
	}
	_ = rows.Close()
	ents, _ := os.ReadDir(filepath.Join(r.dir, "audio", "masters"))
	for _, e := range ents {
		if !owned[e.Name()] {
			t.Errorf("master %s belongs to no track: the crash window left a second master", e.Name())
		}
	}
}

// windowIngest runs the real freeze and then removes the work dir, so the journal write that follows it in the bridge cannot
// happen: the state a crash between the two leaves.
type windowIngest struct {
	transform.Ingester
	root  string
	armed *bool
}

func (w windowIngest) Ingest(ctx context.Context, req ingest.Request) (ingest.Result, error) {
	res, err := w.Ingester.Ingest(ctx, req)
	if err == nil && w.armed != nil && *w.armed {
		// the bridge writes claimed.json after this returns; a directory of that name makes the write fail, as the death of the
		// process before it would have
		_ = os.Mkdir(filepath.Join(w.root, "incoming", "transform-"+req.TrackID, "claimed.json"), 0o700)
	}
	return res, err
}

// TestAcceptance_T1_LoudnessUnsafeRefused (the bug alarm, media tier): a child whose audio ends up far louder than its own
// report says is caught by the pipeline's measurement after the freeze: LOUDNESS_UNSAFE, the ingest is discarded, no Take, no
// master file, no take directory.
func TestAcceptance_T1_LoudnessUnsafeRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("media tier: real ffmpeg, flac and ingest")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	fc := &transformtest.FakeChild{Amp: 0.4} // far above the level the report claims (its sine of 0.13 reads within a few LU of -16 on any ffmpeg)
	r := chaosRig(t, tools, func(*transform.Options) transform.ChildRunner { return fc })
	before := r.masterFiles()
	id := queueChaos(t, r, "unsafe")
	r.runOne()
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrLoudnessUnsafe {
		t.Fatalf("row = %+v, want failed LOUDNESS_UNSAFE", row)
	}
	r.noTrace(id, before)
	if !strings.Contains(row.ErrText, "loudness") {
		t.Errorf("the refusal does not say why: %q", row.ErrText)
	}
}
