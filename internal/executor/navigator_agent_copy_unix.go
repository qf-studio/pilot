//go:build unix

package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// openWorkspaceRootFD opens the workspace worktreePath for the copy out: the
// configured workspace root, then the workspace name below it with O_NOFOLLOW.
// worktreePath must be a directory directly below the workspace root (as
// configured or as resolved); any other path and a link at the workspace name
// are errors. The agent user owns the workspace and can rename it and put a
// link in its place: every later open of the copy starts from this one
// descriptor, never again from the path string.
func openWorkspaceRootFD(worktreePath string) (*os.File, error) {
	s := runAsSettingsNow()
	if s == nil || s.workspaceRoot == "" {
		return nil, errors.New("run_as_user is not set")
	}
	ws, err := absCleanPath(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("workspace %q: %w", worktreePath, err)
	}
	parent, name := filepath.Dir(ws), filepath.Base(ws)
	root := ""
	for _, r := range pathForms(s.workspaceRoot) {
		if parent == r {
			root = r
			break
		}
	}
	if root == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("workspace %q is not a directory directly below the workspace root %q", worktreePath, s.workspaceRoot)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: root, Err: err}
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(rootFD)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: ws, Err: err}
	}
	return os.NewFile(uintptr(fd), ws), nil
}

// openWorkspaceNoFollow opens rel (slash-separated, below the workspace
// descriptor ws of openWorkspaceRootFD) one path element at a time, each with
// O_NOFOLLOW: a link at any element below the workspace is refused
// (noFollowRefused), never followed, and there is no window between a check
// and the open. Every element but the last must be a directory; the last one
// too when dir is set. ws stays open.
func openWorkspaceNoFollow(ws *os.File, rel string, dir bool) (*os.File, error) {
	root := ws.Name()
	parts := strings.Split(rel, "/")
	for _, name := range parts {
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("open %s below %s: invalid path element %q", rel, root, name)
		}
	}
	base := int(ws.Fd())
	defer runtime.KeepAlive(ws)
	fd := base
	for i, name := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if dir || i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK // a FIFO the agent put there must not block the open
		}
		next, err := unix.Openat(fd, name, flags, 0)
		if fd != base {
			_ = unix.Close(fd)
		}
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

// openFileNoFollow opens path for reading without following a link at its
// last element; O_NONBLOCK keeps a FIFO from blocking the open.
func openFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- the daemon's own .agent directory.
}

// fileLinkCount is the number of hard links of info (1 when unknown).
func fileLinkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:unconvert // the type of Nlink differs between platforms
	}
	return 1
}
