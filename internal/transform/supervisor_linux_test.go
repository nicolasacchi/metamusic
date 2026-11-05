//go:build linux

package transform_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// describeProcess reports what the supervisor set up around this process: nice level, process group, parent-death signal.
func describeProcess() string {
	var sig int32
	_, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 2 /* PR_GET_PDEATHSIG */, uintptr(unsafe.Pointer(&sig)), 0)
	if e != 0 {
		sig = -1
	}
	stat, _ := os.ReadFile("/proc/self/stat")
	rest := string(stat[strings.LastIndexByte(string(stat), ')')+2:]) // fields after "(comm) "
	f := strings.Fields(rest)
	nice := "?"
	if len(f) > 16 {
		nice = f[16] // field 19 overall
	}
	return fmt.Sprintf("--process--\nnice=%s\npgid=%d\npid=%d\npdeathsig=%d", nice, syscall.Getpgrp(), os.Getpid(), sig)
}

func checkProcess(t *testing.T, proc string) {
	t.Helper()
	kv := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(proc), "\n") {
		k, v, _ := strings.Cut(l, "=")
		kv[k] = v
	}
	if kv["nice"] != "10" && kv["nice"] != "" { // the nice value is 10 above the (usually 0) parent's
		if n, err := strconv.Atoi(kv["nice"]); err != nil || n < 10 {
			t.Errorf("nice = %q, want >= 10", kv["nice"])
		}
	}
	if kv["pgid"] != kv["pid"] {
		t.Errorf("the child is not its own process group leader: pgid %s pid %s", kv["pgid"], kv["pid"])
	}
	if kv["pdeathsig"] != "9" {
		t.Errorf("pdeathsig = %s, want 9 (SIGKILL)", kv["pdeathsig"])
	}
}
