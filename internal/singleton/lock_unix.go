// Package singleton: Unix flock backend.

//go:build !windows

package singleton

import (
	"errors"

	"golang.org/x/sys/unix"
)

func tryFlock(fd uintptr) error {
	return unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB)
}

func unflock(fd uintptr) error {
	return unix.Flock(int(fd), unix.LOCK_UN)
}

func isLockHeldErr(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
