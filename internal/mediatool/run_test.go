package mediatool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_Basic(t *testing.T) {
	out, err := Run(context.Background(), script(t, `printf hello`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	if err != nil || string(out) != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	var sink bytes.Buffer
	out, err = Run(context.Background(), script(t, `printf streamed`), nil, RunOpts{Timeout: 5 * time.Second, Stdout: &sink})
	if err != nil || len(out) != 0 || sink.String() != "streamed" {
		t.Fatalf("out=%q sink=%q err=%v", out, sink.String(), err)
	}
	if _, err := Run(context.Background(), "true", nil, RunOpts{}); err == nil {
		t.Fatal("zero timeout must be rejected")
	}
}

func TestRun_ToolError(t *testing.T) {
	_, err := Run(context.Background(), script(t, `echo boom >&2; exit 3`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != 3 || !strings.Contains(te.StderrTail, "boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestRun_Timeout(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), script(t, `exec sleep 30`), nil, RunOpts{Timeout: 200 * time.Millisecond, MaxStdout: 1 << 10})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond+waitDelay+time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRun_ParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Run(ctx, script(t, `exec sleep 30`), nil, RunOpts{Timeout: 20 * time.Second, MaxStdout: 1 << 10})
	if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want Canceled, got %v", err)
	}
}

func TestRun_MaxStdoutRequiredWhenCaptured(t *testing.T) {
	if _, err := Run(context.Background(), script(t, `printf x`), nil, RunOpts{Timeout: time.Second}); err == nil {
		t.Fatal("captured stdout without MaxStdout must be rejected")
	}
	var sink bytes.Buffer
	if _, err := Run(context.Background(), script(t, `printf x`), nil, RunOpts{Timeout: time.Second, Stdout: &sink}); err != nil {
		t.Fatal(err)
	}
}

func TestRun_WaitDelayOrphanPipe(t *testing.T) {
	// The shell exits at once, but a background grandchild keeps stdout open.
	start := time.Now()
	out, err := Run(context.Background(), script(t, `sleep 7 &
echo hi`), nil, RunOpts{Timeout: 10 * time.Second, MaxStdout: 1 << 10})
	if d := time.Since(start); d > waitDelay+3*time.Second {
		t.Fatalf("Run hung for %v (err=%v)", d, err)
	}
	// The tool itself succeeded; the orphan only held the pipe.
	if err != nil || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRun_OutputCap(t *testing.T) {
	_, err := Run(context.Background(), script(t, `head -c 10485760 /dev/zero`), nil,
		RunOpts{Timeout: 10 * time.Second, MaxStdout: 1 << 20})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestRun_ErrorHasNoArgv(t *testing.T) {
	const sentinel = "SENTINEL-ARG-9f3a"
	_, err := Run(context.Background(), script(t, `echo bad >&2; exit 1`), []string{sentinel}, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "MaxStdout") {
		t.Fatalf("err=%v", err)
	}
	var te *ToolError
	if errors.As(err, &te) && strings.Contains(te.StderrTail, sentinel) {
		t.Fatal("argv in stderr tail")
	}
	_, err = Run(context.Background(), "/nonexistent/tool-"+sentinel[:4], []string{sentinel}, RunOpts{Timeout: time.Second, MaxStdout: 1 << 10})
	if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "MaxStdout") {
		t.Fatalf("start error leaks argv: %v", err)
	}
}

func TestRun_EnvMinimal(t *testing.T) {
	t.Setenv("MM_SECRET_PROBE", "leak")
	out, err := Run(context.Background(), script(t, `env | sort`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "MM_SECRET_PROBE") {
		t.Fatalf("env leaked: %s", out)
	}
	if !strings.Contains(string(out), "LC_ALL=C") || !strings.Contains(string(out), "PATH=") {
		t.Fatalf("env: %s", out)
	}
}

func TestRun_StderrTailBounded(t *testing.T) {
	_, err := Run(context.Background(), script(t, `head -c 200000 /dev/zero | tr '\0' x >&2; exit 2`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	var te *ToolError
	if !errors.As(err, &te) || len(te.StderrTail) > 2048 {
		t.Fatalf("err=%v", err)
	}
}

func TestRun_StderrTailBoundedBinary(t *testing.T) {
	// invalid UTF-8 expands to 3 bytes per run when sanitised; the bound must hold afterwards.
	_, err := Run(context.Background(), script(t, `head -c 4096 /dev/urandom >&2; exit 2`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	var te *ToolError
	if !errors.As(err, &te) || len(te.StderrTail) > 2048 || !utf8.ValidString(te.StderrTail) {
		t.Fatalf("len=%d err=%v", len(te.StderrTail), err)
	}
	_, err = Run(context.Background(), script(t, `head -c 4096 /dev/urandom >&2; exec sleep 30`), nil, RunOpts{Timeout: 300 * time.Millisecond, MaxStdout: 1 << 10})
	if !errors.As(err, &te) || len(te.Error()) > 2048+64 {
		t.Fatalf("timeout err too long: %v", err)
	}
}

func TestRun_Stdin(t *testing.T) {
	out, err := Run(context.Background(), script(t, `cat | wc -c`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10})
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRun_StdinReader(t *testing.T) {
	var sink bytes.Buffer
	_, err := Run(context.Background(), script(t, `cat`), nil, RunOpts{Timeout: 5 * time.Second, Stdout: &sink, Stdin: strings.NewReader("piped bytes")})
	if err != nil || sink.String() != "piped bytes" {
		t.Fatalf("sink=%q err=%v", sink.String(), err)
	}
	// a nil reader keeps /dev/null even when the tool reads
	out, err := Run(context.Background(), script(t, `cat | wc -c`), nil, RunOpts{Timeout: 5 * time.Second, MaxStdout: 1 << 10, Stdin: nil})
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// A cancelled run must not wait for a stdin reader that never produces or ends.
func TestRun_StdinBlockedReaderCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := Run(ctx, script(t, `exec cat`), nil, RunOpts{Timeout: 30 * time.Second, Stdout: io.Discard, Stdin: pr})
	if err == nil || time.Since(start) > waitDelay+2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}
