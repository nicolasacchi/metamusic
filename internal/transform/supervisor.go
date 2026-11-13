package transform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ExecChild is the production ChildRunner (C210.4, D-T1-7): it starts `<Self> dsp-run --work <dir>` as its own process group
// with an explicit minimal environment, low priority and Pdeathsig, relays throttled progress, and enforces the wall deadline,
// the RSS guard and cancellation. Its exported fields beyond Self exist for tests; production leaves them zero.
type ExecChild struct {
	// Self is the binary to start (os.Executable of the server).
	Self string
	// Args builds the argv after the program name; nil means ["dsp-run", "--work", dir].
	Args func(dir string) []string
	// ExtraEnv is appended to the child's environment. Tests only: production children get GOMEMLIMIT and GOMAXPROCS and nothing else.
	ExtraEnv []string
	// ReadRSS returns the child's resident set in KiB; nil reads /proc/<pid>/status (a no-op where /proc is absent).
	ReadRSS func(pid int) (kib int64, ok bool)
	// After arms the wall deadline: it returns the channel that fires when d has passed and a stop func. Tests only (they fire it
	// once the child is known to be running); nil means a real timer.
	After func(d time.Duration) (<-chan time.Time, func())
	// ReadCPU returns the child's user+system time in clock ticks; nil reads /proc/<pid>/stat. It is one of the two signs of life
	// the stall window watches (the other is a progress line).
	ReadCPU func(pid int) (ticks int64, ok bool)
	// RSSPoll and Throttle default to the C200.4 constants. RSSPoll is also the cadence of the stall check.
	RSSPoll, Throttle time.Duration
}

const (
	maxProgressBytes = 64 << 10
	stderrKeep       = 4 << 10
	rssSlackMiB      = 128
)

// tail keeps the last n bytes written.
type tail struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.n:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(strings.TrimSpace(string(t.buf)), "�")
}

// Run implements ChildRunner.
func (c *ExecChild) Run(ctx context.Context, j ChildJob, progress ChildProgress) (ChildResult, error) {
	var res ChildResult
	if !filepath.IsAbs(j.Dir) {
		return res, errors.New("transform: child work dir must be absolute")
	}
	if c.Self == "" {
		return res, errors.New("transform: no child binary")
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	args := []string{"dsp-run", "--work", j.Dir}
	if c.Args != nil {
		args = c.Args(j.Dir)
	}
	cmd := exec.Command(c.Self, args...) // #nosec G204 -- Self is os.Executable; argv is literals and an app-created absolute dir
	cmd.Dir = j.Dir
	memLimit := int(float64(j.MemMB) * 1.25)
	cmd.Env = append([]string{"GOMEMLIMIT=" + strconv.Itoa(memLimit) + "MiB", "GOMAXPROCS=1"}, c.ExtraEnv...)
	cmd.SysProcAttr = childSysProcAttr()
	cmd.Stdin = nil // /dev/null
	errTail := &tail{n: stderrKeep}
	cmd.Stderr = errTail
	cmd.WaitDelay = 2 * time.Second
	pr, pw, err := os.Pipe()
	if err != nil {
		return res, errors.New("transform: cannot start the render child")
	}
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return res, errors.New("transform: cannot start the render child")
	}
	_ = pw.Close() // the child holds the only write end
	pid := cmd.Process.Pid
	lowerPriority(pid)

	throttle := c.Throttle
	if throttle <= 0 {
		throttle = ProgressThrottle
	}
	readerDone := make(chan struct{})
	var lines atomic.Int64 // progress lines seen: a sign of life for the stall window
	go func() {
		defer close(readerDone)
		relayProgress(pr, throttle, progress, func() { lines.Add(1) })
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	rssPoll := c.RSSPoll
	if rssPoll <= 0 {
		rssPoll = RSSPoll
	}
	readRSS := c.ReadRSS
	if readRSS == nil {
		readRSS = procRSS
	}
	readCPU := c.ReadCPU
	if readCPU == nil {
		readCPU = procCPUTicks
	}
	limitKiB := int64(float64(j.MemMB)*1.5+rssSlackMiB) * 1024
	var deadline <-chan time.Time
	if j.Deadline > 0 {
		if c.After != nil {
			ch, stop := c.After(j.Deadline)
			defer stop()
			deadline = ch
		} else {
			t := time.NewTimer(j.Deadline)
			defer t.Stop()
			deadline = t.C
		}
	}
	tick := time.NewTicker(rssPoll)
	defer tick.Stop()

	var cause error
	var waitErr error
	lastLines, lastCPU, lastAlive := lines.Load(), int64(-1), time.Now()
loop:
	for {
		select {
		case waitErr = <-waited:
			break loop
		case <-ctx.Done():
			cause = ctx.Err()
		case <-deadline:
			cause = ErrChildDeadline
		case <-tick.C:
			if kib, ok := readRSS(pid); ok && kib > limitKiB {
				cause = ErrChildMemory
			}
			if j.Stall > 0 && cause == nil {
				alive := false
				if n := lines.Load(); n != lastLines {
					lastLines, alive = n, true
				}
				if t, ok := readCPU(pid); ok && t != lastCPU {
					lastCPU, alive = t, true
				}
				if alive {
					lastAlive = time.Now()
				} else if time.Since(lastAlive) > j.Stall {
					cause = ErrChildDeadline
				}
			}
		}
		if cause != nil {
			killGroup(pid)
			waitErr = <-waited
			break loop
		}
	}
	killGroup(pid) // descendants that outlived the child must not keep the pipe or the CPU
	_ = pr.Close()
	<-readerDone
	if ps := cmd.ProcessState; ps != nil {
		res.CPU = ps.UserTime() + ps.SystemTime()
		res.ExitCode = ps.ExitCode()
	}
	res.StderrTail = errTail.String()
	if cause != nil {
		return res, cause
	}
	var ee *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &ee) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return res, fmt.Errorf("transform: child wait: %w", waitErr)
	}
	if res.ExitCode < 0 { // killed by a signal we did not send (the OOM killer, a stray kill): an interruption, not a verdict
		return res, ErrChildSignaled
	}
	return res, nil
}

// killGroup sends SIGKILL to the child's process group; the group id is the pid (Setpgid).
func killGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// relayProgress parses the child's `{"p":412,"phase":"rendering"}` lines: at most 64 KiB are read, values are clamped to
// 0..1000 and made monotone, and the callback fires no more often than throttle (the first line always passes).
func relayProgress(r io.Reader, throttle time.Duration, progress ChildProgress, alive func()) {
	lr := &io.LimitedReader{R: r, N: maxProgressBytes}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 0, 4096), 4096)
	var last time.Time
	best := 0
	for sc.Scan() {
		var line struct {
			P     *int   `json:"p"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &line) != nil || line.P == nil {
			continue
		}
		alive()
		p := min(max(*line.P, 0), 1000)
		best = max(best, p)
		if progress == nil || (!last.IsZero() && time.Since(last) < throttle) {
			continue
		}
		last = time.Now()
		progress(best, line.Phase)
	}
	_, _ = io.Copy(io.Discard, r) // keep draining so a chatty child never blocks on a full pipe
}

// procCPUTicks reads utime+stime (fields 14 and 15, in clock ticks) of a process from /proc.
func procCPUTicks(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// the command name is in parentheses and may hold spaces: the fields after the last ')' start at field 3 (state)
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(string(b)[i+2:])
	if len(f) < 13 {
		return 0, false
	}
	u, err1 := strconv.ParseInt(f[11], 10, 64)
	s, err2 := strconv.ParseInt(f[12], 10, 64)
	return u + s, err1 == nil && err2 == nil
}

// procRSS reads VmRSS of a process from /proc.
func procRSS(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			n, err := strconv.ParseInt(f[0], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}
