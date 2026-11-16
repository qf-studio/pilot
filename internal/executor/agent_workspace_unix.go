//go:build unix

package executor

import (
	"os"
	"strconv"
	"syscall"
)

// fileOwnerUID returns the uid of the owner of fi.
func fileOwnerUID(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return strconv.FormatUint(uint64(st.Uid), 10), true
}

// fileOwnerGID returns the gid of the group of fi.
func fileOwnerGID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Gid), true
}

// fileLinkID returns device, inode and link count of fi.
func fileLinkID(fi os.FileInfo) (dev, ino, nlink uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), uint64(st.Nlink), true //nolint:unconvert // the field types differ per platform
}
