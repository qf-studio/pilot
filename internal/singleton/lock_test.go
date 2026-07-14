package singleton

import (
	"errors"
	"os"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()

	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lock.Path() == "" {
		t.Fatal("expected non-empty lock path")
	}

	pid, err := ReadPID(dir)
	if err != nil {
		t.Fatalf("ReadPID: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("ReadPID = %d, want %d", pid, os.Getpid())
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquireHeldByAnother(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = Acquire(dir)
	if err == nil {
		t.Fatal("expected second Acquire to fail while first holds the lock")
	}
	var held *ErrHeld
	if !errors.As(err, &held) {
		t.Fatalf("expected *ErrHeld, got %T: %v", err, err)
	}
	if held.PID != os.Getpid() {
		t.Fatalf("ErrHeld.PID = %d, want %d", held.PID, os.Getpid())
	}
}

func TestAcquireAfterRelease(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("second Acquire after release: %v", err)
	}
	defer func() { _ = second.Release() }()
}

func TestReadPIDNoLockFile(t *testing.T) {
	dir := t.TempDir()

	pid, err := ReadPID(dir)
	if err != nil {
		t.Fatalf("ReadPID: %v", err)
	}
	if pid != 0 {
		t.Fatalf("ReadPID = %d, want 0 for missing lock file", pid)
	}
}

func TestReleaseNilLock(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatalf("Release on nil *Lock should be a no-op, got %v", err)
	}
}
