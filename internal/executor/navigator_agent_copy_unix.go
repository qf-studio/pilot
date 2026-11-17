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

	"golang.org/x/sys/unix"
)

// openWorkspaceRootFD opens the workspace worktreePath for the copy out: the
// configured workspace root as given, then the workspace name below it with
// Openat and O_NOFOLLOW. worktreePath must name an entry directly below the
// workspace root (in the given or the resolved form of the root); any other
// path is an error, and a link at the workspace name is refused
// (noFollowRefused). The agent user owns the workspace and can rename it and
// put a link in its place; the copy out holds the returned descriptor to its
// end and never opens the path string again.
func openWorkspaceRootFD(worktreePath string) (*os.File, error) {
	s := runAsSettingsNow()
	if s == nil || s.workspaceRoot == "" {
		return nil, errors.New("open workspace: run_as_user is not set")
	}
	ws, err := absCleanPath(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("open workspace %q: %w", worktreePath, err)
	}
	parent, name := filepath.Dir(ws), filepath.Base(ws)
	var root string
	for _, form := range pathForms(s.workspaceRoot) {
		if form == parent {
			root = form
			break
		}
	}
	if root == "" || ws == parent {
		return nil, fmt.Errorf("open workspace %s: not directly below the workspace root %s", ws, s.workspaceRoot)
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

// openWorkspaceNoFollow opens rel (slash-separated) below the workspace
// descriptor ws of openWorkspaceRootFD one path element at a time, each with
// O_NOFOLLOW: a link at any element is refused (noFollowRefused), never
// followed, and there is no window between a check and the open. Every
// element but the last must be a directory; the last one too when dir is set.
// ws stays open; the name of the result is for messages only.
func openWorkspaceNoFollow(ws *os.File, rel string, dir bool) (*os.File, error) {
	parts := strings.Split(rel, "/")
	for _, name := range parts {
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("open %s below %s: invalid path element %q", rel, ws.Name(), name)
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
			return nil, &fs.PathError{Op: "open", Path: filepath.Join(ws.Name(), filepath.Join(parts[:i+1]...)), Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Join(ws.Name(), filepath.FromSlash(rel))), nil
}

// noFollowRefused reports whether err of openWorkspaceNoFollow is the refusal
// of a link (ELOOP, EMLINK on some BSDs) or of a file where a directory
// should be (ENOTDIR).
func noFollowRefused(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) || errors.Is(err, unix.ENOTDIR)
}

// openSourceFileNoFollow opens path (a file of the daemon's .agent) for the
// copy in with O_NOFOLLOW, and O_NONBLOCK so that a FIFO put there after the
// Lstat cannot block the open. Anything but a regular file is refused with
// errNotRegular.
func openSourceFileNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "fstat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// linkCount is the number of hard links of the open file f.
func linkCount(f *os.File) (uint64, error) {
	var st unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &st)
	runtime.KeepAlive(f)
	if err != nil {
		return 0, &fs.PathError{Op: "fstat", Path: f.Name(), Err: err}
	}
	return uint64(st.Nlink), nil //nolint:unconvert // the type of Nlink differs between platforms
}
