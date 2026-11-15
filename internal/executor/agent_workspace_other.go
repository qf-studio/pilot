//go:build !unix

package executor

import "os"

// fileOwnerUID is not known off Unix; run_as_user is Linux only.
func fileOwnerUID(os.FileInfo) (string, bool) { return "", false }

// fileLinkID is not known off Unix; run_as_user is Linux only.
func fileLinkID(os.FileInfo) (dev, ino, nlink uint64, ok bool) { return 0, 0, 0, false }
