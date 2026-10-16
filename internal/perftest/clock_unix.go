//go:build unix

package perftest

import (
	"syscall"
	"time"
)

// cpuNow is the process's consumed CPU time (user+system, all threads).
func cpuNow() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return time.Duration(time.Now().UnixNano())
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
