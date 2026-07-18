//go:build linux

package executor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCreateCgroupLeaf covers the pure directory/file-writing logic of the
// cgroup v2 leaf creation, independent of whether a real cgroupfs is
// mounted at the test's chosen base path. GH-4401.
func TestCreateCgroupLeaf(t *testing.T) {
	tests := []struct {
		name     string
		maxRSSMB int
	}{
		{name: "small cap", maxRSSMB: 512},
		{name: "default cap", maxRSSMB: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			leaf, err := createCgroupLeaf(base, "pilot-test", tt.maxRSSMB)
			if err != nil {
				t.Fatalf("createCgroupLeaf() error = %v", err)
			}
			if leaf != filepath.Join(base, "pilot-test") {
				t.Fatalf("leaf = %q, want %q", leaf, filepath.Join(base, "pilot-test"))
			}

			memMaxBytes, err := os.ReadFile(filepath.Join(leaf, "memory.max"))
			if err != nil {
				t.Fatalf("read memory.max: %v", err)
			}
			gotBytes, err := strconv.ParseInt(strings.TrimSpace(string(memMaxBytes)), 10, 64)
			if err != nil {
				t.Fatalf("parse memory.max content %q: %v", memMaxBytes, err)
			}
			wantBytes := int64(tt.maxRSSMB) * 1024 * 1024
			if gotBytes != wantBytes {
				t.Errorf("memory.max = %d bytes, want %d bytes (%d MiB)", gotBytes, wantBytes, tt.maxRSSMB)
			}

			// cgroup.procs must NOT be written by createCgroupLeaf — only
			// addPidToCgroup should move a process. Regression guard against
			// the self-cap hazard: probing/creating a leaf must never risk
			// moving any real process into a memory-capped cgroup.
			if _, err := os.Stat(filepath.Join(leaf, "cgroup.procs")); !os.IsNotExist(err) {
				t.Errorf("cgroup.procs should not exist after createCgroupLeaf alone, stat err = %v", err)
			}
		})
	}
}

// TestAddPidToCgroup verifies the pid-move step writes the expected content,
// separately from leaf creation (GH-4401: keeping these steps distinct is
// what makes the availability probe safe to run against the pilot daemon's
// own process).
func TestAddPidToCgroup(t *testing.T) {
	base := t.TempDir()
	leaf, err := createCgroupLeaf(base, "pilot-test", 1024)
	if err != nil {
		t.Fatalf("createCgroupLeaf() error = %v", err)
	}

	const fakePid = 424242
	if err := addPidToCgroup(leaf, fakePid); err != nil {
		t.Fatalf("addPidToCgroup() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(leaf, "cgroup.procs"))
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	if strings.TrimSpace(string(got)) != strconv.Itoa(fakePid) {
		t.Errorf("cgroup.procs = %q, want %q", got, strconv.Itoa(fakePid))
	}
}

// TestApplyResourceLimits_NilOrDisabled_NeverTouchesFilesystem is a
// table-driven guard that disabled/nil/zero configs never attempt cgroup
// creation, and that the returned cleanup is always safe to call.
func TestApplyResourceLimits_NilOrDisabled_NeverTouchesFilesystem(t *testing.T) {
	base := t.TempDir()
	origBase := cgroupV2Base
	cgroupV2Base = filepath.Join(base, "pilot")
	defer func() { cgroupV2Base = origBase }()

	tests := []struct {
		name string
		cfg  *SubprocessLimitsConfig
	}{
		{name: "nil config", cfg: nil},
		{name: "disabled", cfg: &SubprocessLimitsConfig{Enabled: false, MaxRSSMB: 4096}},
		{name: "enabled but zero cap", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 0}},
		{name: "enabled but negative cap", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := applyResourceLimits(os.Getpid(), tt.cfg)
			if cleanup == nil {
				t.Fatal("applyResourceLimits() returned nil cleanup func")
			}
			cleanup() // must not panic

			if _, err := os.Stat(cgroupV2Base); !os.IsNotExist(err) {
				t.Errorf("cgroup base %s should not have been created, stat err = %v", cgroupV2Base, err)
			}
		})
	}
}

// TestApplyResourceLimits_EnabledButUnwritableBase verifies graceful
// degradation (nil-effect cleanup, no panic/error return) when the cgroup
// base can't be created — the production-equivalent case is "no root / no
// delegated subtree", reproduced here by pointing at a path with no write
// permission. GH-4401: this must degrade to telemetry-only, exactly like
// the old RLIMIT_AS path did on darwin, never fail the execution.
func TestApplyResourceLimits_EnabledButUnwritableBase(t *testing.T) {
	roParent := t.TempDir()
	if err := os.Chmod(roParent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(roParent, 0o700) }() // allow TempDir cleanup

	origBase := cgroupV2Base
	cgroupV2Base = filepath.Join(roParent, "unwritable", "pilot")
	defer func() { cgroupV2Base = origBase }()

	cfg := &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 4096}
	cleanup := applyResourceLimits(os.Getpid(), cfg)
	if cleanup == nil {
		t.Fatal("applyResourceLimits() returned nil cleanup func")
	}
	cleanup() // must not panic even though nothing was created
}

// TestCgroupV2MemoryAvailable_NeverMovesCallingProcess is the critical
// safety regression for the availability probe: it must never write the
// calling process's own pid into a memory-capped cgroup.procs, since doing
// so on a probe (rather than a real subprocess) would risk capping the
// pilot daemon itself. We assert only that the probe leaf is cleaned up and
// that the running test process is still alive and unaffected — the
// stronger structural guarantee (createCgroupLeaf never writes
// cgroup.procs) is covered by TestCreateCgroupLeaf above.
func TestCgroupV2MemoryAvailable_NeverMovesCallingProcess(t *testing.T) {
	base := t.TempDir()
	origBase := cgroupV2Base
	cgroupV2Base = filepath.Join(base, "pilot")
	defer func() { cgroupV2Base = origBase }()

	// Availability is host-dependent (needs real cgroup v2 delegation), so
	// this just needs to not panic/hang and must leave no probe leaf behind.
	_, _ = CgroupV2MemoryAvailable()

	entries, err := os.ReadDir(cgroupV2Base)
	if err != nil {
		if os.IsNotExist(err) {
			return // base was never created (mkdir failed on a non-cgroupfs temp dir tree is still fine)
		}
		t.Fatalf("read cgroup base: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pilot-probe-") {
			t.Errorf("probe leaf %s was not cleaned up", e.Name())
		}
	}
}
