//go:build !unix

package leakscan

import "os"

// fileID has no inode to report here; rotation is then noticed by size alone.
func fileID(os.FileInfo) (uint64, bool) { return 0, false }
