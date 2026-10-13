//go:build unix

package ears_test

import (
	"os"
	"syscall"
)

func nlink(st os.FileInfo) (uint64, bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(s.Nlink), true //nolint:gosec,unconvert // Nlink is uint64 or uint32 per platform
}
