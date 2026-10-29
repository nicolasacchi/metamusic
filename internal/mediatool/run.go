// Package mediatool is the only way the app executes external media tools
// (ffmpeg, flac, metaflac). Callers must obey the argv rule: arguments are
// literals or app-created absolute paths, never strings derived from inbox
// file names, tags, titles or recipe text.
package mediatool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Tools holds tool paths; shared by ingest, analysis and synth.
type Tools struct{ FFmpeg, FLAC, Metaflac string }

// RunOpts configures Run.
type RunOpts struct {
	Timeout   time.Duration // required, > 0
	Stdout    io.Writer     // streaming consumer; nil => captured and returned
	MaxStdout int64         // required (> 0) when Stdout is nil: cap for captured stdout; exceeded => process killed, ErrOutputLimit
	MaxStderr int           // ring buffer size (default 64 KiB); errors carry its last 2 KiB
	Stdin     io.Reader     // fed to the tool's stdin; nil => /dev/null. Run does not wait for a reader blocked in Read.
}

// ErrOutputLimit is returned when captured stdout exceeds RunOpts.MaxStdout.
var ErrOutputLimit = errors.New("mediatool: output limit exceeded")

const (
	waitDelay      = 5 * time.Second
	defaultStderr  = 64 << 10
	stderrTailSize = 2 << 10
)

// ToolError reports a failed tool run. It never contains argv.
type ToolError struct {
	Tool       string
	ExitCode   int
	StderrTail string
	// Err is the context error when the run was cancelled or timed out; it never carries argv.
	Err error
}

// Unwrap exposes the context error so errors.Is(err, context.DeadlineExceeded) works.
func (e *ToolError) Unwrap() error { return e.Err }

func (e *ToolError) Error() string {
	if e.StderrTail == "" {
		return fmt.Sprintf("%s: exit %d", e.Tool, e.ExitCode)
	}
	return fmt.Sprintf("%s: exit %d: %s", e.Tool, e.ExitCode, e.StderrTail)
}

// ring keeps the last n bytes written.
type ring struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.n {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.n:]...)
	}
	return len(p), nil
}

// tail returns the last <= n bytes (valid UTF-8, cut on a rune boundary).
func (r *ring) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buf
	if len(b) > n {
		b = b[len(b)-n:]
	}
	s := strings.ToValidUTF8(strings.TrimSpace(string(b)), "\uFFFD")
	if len(s) > n {
		s = s[len(s)-n:]
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
	}
	return s
}

// limitWriter fails once more than max bytes have been written.
type limitWriter struct {
	buf    bytes.Buffer
	max    int64
	cancel context.CancelFunc
	hit    bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.max > 0 && int64(l.buf.Len())+int64(len(p)) > l.max {
		l.hit = true
		l.cancel()
		return 0, ErrOutputLimit
	}
	return l.buf.Write(p)
}

// Run executes bin with args under a timeout. Stdin is /dev/null unless RunOpts.Stdin is set, and the
// environment is LC_ALL=C plus PATH only.
func Run(ctx context.Context, bin string, args []string, o RunOpts) ([]byte, error) {
	if o.Timeout <= 0 {
		return nil, errors.New("mediatool: Timeout is required")
	}
	if o.Stdout == nil && o.MaxStdout <= 0 {
		return nil, errors.New("mediatool: MaxStdout is required when stdout is captured")
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	name := bin
	if i := strings.LastIndexByte(bin, '/'); i >= 0 {
		name = bin[i+1:]
	}
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- argv rule in package doc
	cmd.WaitDelay = waitDelay
	cmd.Stdin = nil // /dev/null
	if o.Stdin != nil {
		// Feed the tool through our own OS pipe rather than cmd.Stdin = reader: exec waits for its copier goroutine, which
		// never returns while the reader blocks in Read, so a cancelled run could hang. Here the copier is detached: it ends
		// at the reader's EOF or error, or at EPIPE once the tool is gone; a reader that blocks forever is released only
		// by its owner (the pipeline closes its io.Pipe on every exit path).
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, &ToolError{Tool: name, ExitCode: -1, StderrTail: "cannot run tool"}
		}
		defer func() { _ = pr.Close() }()
		cmd.Stdin = pr
		go func() {
			_, _ = io.Copy(pw, o.Stdin)
			_ = pw.Close()
		}()
	}
	cmd.Env = []string{"LC_ALL=C", "PATH=" + os.Getenv("PATH")}
	size := o.MaxStderr
	if size <= 0 {
		size = defaultStderr
	}
	errRing := &ring{n: size}
	cmd.Stderr = errRing
	var lw *limitWriter
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	} else {
		lw = &limitWriter{max: o.MaxStdout, cancel: cancel}
		cmd.Stdout = lw
	}
	err := cmd.Run()
	if lw != nil && lw.hit {
		return nil, ErrOutputLimit
	}
	if err != nil && errors.Is(err, exec.ErrWaitDelay) {
		// The tool exited successfully; only an orphan kept the pipes open. Keep the output.
		err = nil
	}
	if err != nil {
		var ee *exec.ExitError
		switch {
		case parent.Err() != nil:
			return nil, &ToolError{Tool: name, ExitCode: -1, StderrTail: "cancelled", Err: parent.Err()}
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			const prefix = "timeout: "
			return nil, &ToolError{Tool: name, ExitCode: -1, StderrTail: prefix + errRing.tail(stderrTailSize-len(prefix)), Err: context.DeadlineExceeded}
		case errors.As(err, &ee):
			return nil, &ToolError{Tool: name, ExitCode: ee.ExitCode(), StderrTail: errRing.tail(stderrTailSize)}
		default:
			// start failures (*exec.Error / *fs.PathError) carry the path, not argv; keep only the tool name.
			return nil, &ToolError{Tool: name, ExitCode: -1, StderrTail: "cannot run tool"}
		}
	}
	if lw != nil {
		return lw.buf.Bytes(), nil
	}
	return nil, nil
}
