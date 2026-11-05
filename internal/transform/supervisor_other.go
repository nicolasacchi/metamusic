//go:build !linux

package transform

import "syscall"

// childSysProcAttr: no Pdeathsig outside Linux; the server's own shutdown path kills the group.
func childSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// lowerPriority is setpriority(+10) on the started child (best effort).
func lowerPriority(pid int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }
