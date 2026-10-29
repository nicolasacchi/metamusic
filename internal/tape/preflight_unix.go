//go:build unix

package tape

import "syscall"

// freeBytes is the space available to an unprivileged writer on the filesystem holding dir.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec // block counts and sizes are non-negative
}
