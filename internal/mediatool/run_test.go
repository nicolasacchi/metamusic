package mediatool

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	out, err := Run(context.Background(), script(t, `printf hello`), nil, RunOpts{Timeout: 5 * time.Second})
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
	_, err := Run(context.Background(), script(t, `echo boom >&2; exit 3`), nil, RunOpts{Timeout: 5 * time.Second})
	var te *ToolError
	if !errors.As(err, &te) || te.ExitCode != 3 || !strings.Contains(te.StderrTail, "boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestRun_Timeout(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), script(t, `exec sleep 30`), nil, RunOpts{Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > 200*time.Millisecond+waitDelay+time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRun_WaitDelayOrphanPipe(t *testing.T) {
	// The shell exits at once, but a background grandchild keeps stdout open.
	start := time.Now()
	_, err := Run(context.Background(), script(t, `sleep 30 &
echo hi`), nil, RunOpts{Timeout: 10 * time.Second})
	if d := time.Since(start); d > waitDelay+3*time.Second {
		t.Fatalf("Run hung for %v (err=%v)", d, err)
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
	_, err := Run(context.Background(), script(t, `echo bad >&2; exit 1`), []string{sentinel}, RunOpts{Timeout: 5 * time.Second})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err=%v", err)
	}
	var te *ToolError
	if errors.As(err, &te) && strings.Contains(te.StderrTail, sentinel) {
		t.Fatal("argv in stderr tail")
	}
	_, err = Run(context.Background(), "/nonexistent/tool-"+sentinel[:4], []string{sentinel}, RunOpts{Timeout: time.Second})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("start error leaks argv: %v", err)
	}
}

func TestRun_EnvMinimal(t *testing.T) {
	t.Setenv("MM_SECRET_PROBE", "leak")
	out, err := Run(context.Background(), script(t, `env | sort`), nil, RunOpts{Timeout: 5 * time.Second})
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
	_, err := Run(context.Background(), script(t, `head -c 200000 /dev/zero | tr '\0' x >&2; exit 2`), nil, RunOpts{Timeout: 5 * time.Second})
	var te *ToolError
	if !errors.As(err, &te) || len(te.StderrTail) > 2048 {
		t.Fatalf("err=%v", err)
	}
}

func TestRun_Stdin(t *testing.T) {
	out, err := Run(context.Background(), script(t, `cat | wc -c`), nil, RunOpts{Timeout: 5 * time.Second})
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
