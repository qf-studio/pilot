//go:build linux

package executor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qf-studio/pilot/internal/logging"
)

// cgroupV2Base is the parent cgroup under which per-subprocess memory-limited
// leaves are created. Overridable in tests.
var cgroupV2Base = "/sys/fs/cgroup/pilot"

// applyResourceLimits creates a per-subprocess cgroup v2 leaf with memory.max
// set to cfg.MaxRSSMB and moves pid into it. It returns a cleanup func that
// removes the cgroup leaf; callers MUST invoke it only after the subprocess
// has been reaped (cmd.Wait() returned) — cgroup.procs must be empty before
// rmdir succeeds.
//
// GH-4401: this replaces the RLIMIT_AS (virtual address space) cap that
// shipped with GH-3028. RLIMIT_AS limits virtual memory, not RSS — Node/V8
// reserves far more VA than it ever touches (pointer cage, code ranges,
// io_uring/undici buffers), so a 4GB VA cap made every mmap inside Claude
// Code's fetch path fail instantly, surfacing as a generic undici
// "fetch failed" ~25ms after a successful TLS handshake, and broke 100% of
// executor task executions on Linux (never fired on darwin, where the old
// code was a documented no-op). cgroup v2 memory.max enforces actual
// resident memory with reclaim before any kill, which is what an "RSS cap"
// always meant to do — it does not touch address-space reservations, so
// Node's mmap-heavy allocator behaves identically to the uncapped case.
//
// Best-effort: cgroup v2 requires either root or a delegated subtree. Any
// failure (not mounted, no permission, memory controller not delegated)
// degrades to telemetry-only mode — a warning is logged once per subprocess
// and execution proceeds uncapped except for the cooperative NODE_OPTIONS
// heap bound applied alongside this call in backend_claudecode.go.
func applyResourceLimits(pid int, cfg *SubprocessLimitsConfig) func() {
	noop := func() {}
	if cfg == nil || !cfg.Enabled || cfg.MaxRSSMB <= 0 {
		return noop
	}

	log := logging.WithComponent("executor.resource_limits")
	leaf, err := createCgroupLeaf(cgroupV2Base, fmt.Sprintf("pilot-%d", pid), cfg.MaxRSSMB)
	if err != nil {
		log.Warn("Failed to create cgroup v2 memory limit for subprocess (telemetry-only mode)",
			slog.Int("pid", pid),
			slog.Int("max_rss_mb", cfg.MaxRSSMB),
			slog.Any("error", err),
		)
		return noop
	}

	if err := addPidToCgroup(leaf, pid); err != nil {
		log.Warn("Failed to move subprocess into cgroup v2 leaf (telemetry-only mode)",
			slog.Int("pid", pid),
			slog.String("cgroup", leaf),
			slog.Any("error", err),
		)
		_ = os.RemoveAll(leaf)
		return noop
	}

	log.Debug("Applied cgroup v2 memory.max to subprocess",
		slog.Int("pid", pid),
		slog.String("cgroup", leaf),
		slog.Int("max_rss_mb", cfg.MaxRSSMB),
	)
	return func() {
		// RemoveAll (not Remove): on a real cgroupfs an empty (no live
		// processes) leaf's interface pseudo-files don't block rmdir, so
		// this is equivalent to Remove there — but RemoveAll also cleans up
		// correctly in tests that simulate the leaf with a plain directory
		// containing real files, where a bare Remove would fail ENOTEMPTY.
		if err := os.RemoveAll(leaf); err != nil && !os.IsNotExist(err) {
			log.Debug("Failed to remove subprocess cgroup (non-fatal)",
				slog.String("cgroup", leaf),
				slog.Any("error", err),
			)
		}
	}
}

// createCgroupLeaf creates base/name, enables the memory controller on base
// (best-effort — already-enabled is not an error), and sets memory.max to
// maxRSSMB. It does NOT move any process into the leaf; callers that want
// enforcement must call addPidToCgroup separately. Returns the leaf path.
func createCgroupLeaf(base, name string, maxRSSMB int) (string, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup base %s: %w", base, err)
	}

	// Enable the memory controller for children of base. A failure here
	// isn't fatal by itself — it may already be enabled, or the kernel may
	// report EBUSY transiently — but if the subtree isn't actually
	// delegated to us, the memory.max write below will fail too, and that's
	// the error we surface.
	_ = os.WriteFile(filepath.Join(base, "cgroup.subtree_control"), []byte("+memory"), 0o644)

	leaf := filepath.Join(base, name)
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return "", fmt.Errorf("create cgroup leaf %s: %w", leaf, err)
	}

	maxBytes := int64(maxRSSMB) * 1024 * 1024
	memoryMax := filepath.Join(leaf, "memory.max")
	if err := os.WriteFile(memoryMax, []byte(strconv.FormatInt(maxBytes, 10)), 0o644); err != nil {
		_ = os.RemoveAll(leaf)
		return "", fmt.Errorf("write %s: %w", memoryMax, err)
	}

	return leaf, nil
}

// addPidToCgroup moves pid into leaf's cgroup.procs, activating enforcement.
func addPidToCgroup(leaf string, pid int) error {
	procs := filepath.Join(leaf, "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", procs, err)
	}
	return nil
}

// CgroupV2MemoryAvailable reports whether this host can delegate cgroup v2
// memory-controller writes to the pilot cgroup base, without moving any real
// process. Used by the doctor/health preflight (GH-4401) to warn operators
// when subprocess_limits.enabled=true will silently degrade to
// telemetry-only mode because cgroup v2 isn't mounted or delegated.
//
// This never writes to any cgroup.procs file — creating (and immediately
// removing) a throwaway probe leaf is sufficient to prove delegation, and
// avoids the hazard of accidentally moving the calling process itself into a
// memory-capped cgroup.
func CgroupV2MemoryAvailable() (bool, string) {
	controllers, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil {
		return false, fmt.Sprintf("cgroup v2 not mounted at /sys/fs/cgroup: %v", err)
	}
	if !strings.Contains(string(controllers), "memory") {
		return false, "cgroup v2 mounted but memory controller not available on this host"
	}

	probeName := fmt.Sprintf("pilot-probe-%d", os.Getpid())
	leaf, err := createCgroupLeaf(cgroupV2Base, probeName, 4096)
	if err != nil {
		return false, fmt.Sprintf("cgroup v2 memory controller not writable/delegated at %s: %v", cgroupV2Base, err)
	}
	_ = os.RemoveAll(leaf)
	return true, "cgroup v2 memory controller delegated at " + cgroupV2Base
}
