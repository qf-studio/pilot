// Package singleton provides an adapter-agnostic single-instance guard for
// the Pilot daemon process, backed by an OS-level advisory file lock
// (flock). Unlike the Telegram-only 409 conflict check it complements
// (internal/adapters/telegram singleton check), this guard applies
// regardless of which adapters are enabled — including github-only/headless
// runs (GH-4311).
package singleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LockFileName is the name of the lock file created under the daemon's
// memory directory (Config.Memory.Path).
const LockFileName = "pilot.lock"

// Lock is a held exclusive single-instance lock. The zero value is not
// usable; obtain one via Acquire.
type Lock struct {
	file *os.File
	path string
}

// ErrHeld is returned by Acquire when another live process already holds
// the lock. PID is the process id recorded by the holder (best-effort; 0 if
// it could not be read).
type ErrHeld struct {
	PID  int
	Path string
}

func (e *ErrHeld) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("pilot daemon already running (pid %d, lock held at %s)", e.PID, e.Path)
	}
	return fmt.Sprintf("pilot daemon already running (lock held at %s)", e.Path)
}

// Acquire takes an exclusive, non-blocking lock on <dir>/pilot.lock,
// creating the directory and file as needed, and stamps the file with the
// current process's pid. The lock is released automatically by the OS if
// this process dies or exits — including a crash — so callers do not need
// to arrange crash cleanup; Release is only needed for a clean shutdown.
//
// Returns *ErrHeld if another process already holds the lock.
func Acquire(dir string) (*Lock, error) {
	if dir == "" {
		return nil, fmt.Errorf("singleton: empty lock directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("singleton: create lock dir: %w", err)
	}
	path := filepath.Join(dir, LockFileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("singleton: open lock file: %w", err)
	}

	if err := tryFlock(f.Fd()); err != nil {
		defer func() { _ = f.Close() }()
		if isLockHeldErr(err) {
			pid, _ := readPID(f)
			return nil, &ErrHeld{PID: pid, Path: path}
		}
		return nil, fmt.Errorf("singleton: lock %s: %w", path, err)
	}

	// We hold the lock — stamp our pid, replacing whatever the previous
	// holder last wrote.
	if err := f.Truncate(0); err != nil {
		_ = unflock(f.Fd())
		_ = f.Close()
		return nil, fmt.Errorf("singleton: truncate lock file: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		_ = unflock(f.Fd())
		_ = f.Close()
		return nil, fmt.Errorf("singleton: write pid: %w", err)
	}
	_ = f.Sync()

	return &Lock{file: f, path: path}, nil
}

// Release unlocks and closes the lock file. Safe to call on a nil *Lock.
// The lock file itself is left in place — the next Acquire truncates and
// re-stamps it, and flock is keyed on the open file description, not file
// content, so leaving it behind is harmless.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unflock(l.file.Fd())
	err := l.file.Close()
	l.file = nil
	return err
}

// Path returns the filesystem path of the lock file, or "" for a nil Lock.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// ReadPID reads the pid recorded in <dir>/pilot.lock without acquiring the
// lock. Returns 0, nil if the lock file does not exist or has no readable
// pid — callers should treat 0 as "no known holder", not an error.
func ReadPID(dir string) (int, error) {
	path := filepath.Join(dir, LockFileName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("singleton: open lock file: %w", err)
	}
	defer func() { _ = f.Close() }()
	pid, _ := readPID(f)
	return pid, nil
}

func readPID(f *os.File) (int, error) {
	buf := make([]byte, 32)
	n, err := f.ReadAt(buf, 0)
	if n == 0 {
		if err != nil {
			return 0, nil
		}
	}
	s := strings.TrimSpace(string(buf[:n]))
	pid, convErr := strconv.Atoi(s)
	if convErr != nil {
		return 0, nil
	}
	return pid, nil
}
