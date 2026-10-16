//go:build !unix

package perftest

import "time"

// cpuNow falls back to wall time where process CPU time is unavailable.
func cpuNow() time.Duration { return time.Duration(time.Now().UnixNano()) }
