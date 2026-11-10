//go:build unix

package leakscan

import (
	"os"
	"syscall"
)

// fileID returns the inode of a file, which changes when a log is rotated by
// rename and a new file takes its path.
func fileID(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Ino), true //nolint:unconvert // Ino is uint32 on some platforms
}
