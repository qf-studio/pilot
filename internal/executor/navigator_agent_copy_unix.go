//go:build unix

package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openWorkspaceNoFollow opens rel (slash-separated, below root) one path
// element at a time from a descriptor of root, each with O_NOFOLLOW: a link at
// any element below root is refused (noFollowRefused), never followed, and
// there is no window between a check and the open. Every element but the last
// must be a directory; the last one too when dir is set. root itself is the
// workspace path of the daemon and is opened as given.
func openWorkspaceNoFollow(root, rel string, dir bool) (*os.File, error) {
	parts := strings.Split(rel, "/")
	for _, name := range parts {
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("open %s below %s: invalid path element %q", rel, root, name)
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: root, Err: err}
	}
	for i, name := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if dir || i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK // a FIFO the agent put there must not block the open
		}
		next, err := unix.Openat(fd, name, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: filepath.Join(root, filepath.Join(parts[:i+1]...)), Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Join(root, filepath.FromSlash(rel))), nil
}

// noFollowRefused reports whether err of openWorkspaceNoFollow is the refusal
// of a link (ELOOP, EMLINK on some BSDs) or of a file where a directory
// should be (ENOTDIR).
func noFollowRefused(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) || errors.Is(err, unix.ENOTDIR)
}
