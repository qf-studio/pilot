// Package singleton: Windows stub.
//
// The Pilot CLI daemon is not currently built or shipped for Windows (only
// the desktop app is, via wails — see release-desktop.yml); this stub keeps
// `go build ./...` working under GOOS=windows without pulling in
// LockFileEx/golang.org/x/sys/windows. It intentionally does not enforce
// single-instance semantics.

//go:build windows

package singleton

func tryFlock(fd uintptr) error {
	return nil
}

func unflock(fd uintptr) error {
	return nil
}

func isLockHeldErr(err error) bool {
	return false
}
