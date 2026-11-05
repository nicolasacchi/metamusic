//go:build linux

package transform

import "syscall"

// childSysProcAttr puts the child in its own process group and has the kernel kill it when the server dies.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// lowerPriority is setpriority(+10) on the started child (best effort).
func lowerPriority(pid int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }
