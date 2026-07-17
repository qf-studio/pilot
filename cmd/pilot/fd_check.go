package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/spf13/cobra"
)

// defaultProcRoot is the real /proc filesystem. Tests substitute a fabricated
// directory tree so checkDBFD can be exercised without a live daemon.
const defaultProcRoot = "/proc"

// fdCheckResult is the outcome of comparing the DB path Pilot's own config
// resolves to against what the live daemon process(es) actually have open.
type fdCheckResult struct {
	ConfiguredPath string         `json:"configured_db_path"`
	DaemonPIDs     []int          `json:"daemon_pids"`
	OpenDBPath     map[int]string `json:"open_db_paths,omitempty"`
	Mismatched     []int          `json:"mismatched_pids,omitempty"`
	Inconclusive   []int          `json:"inconclusive_pids,omitempty"`
}

// findDaemonPIDs scans procRoot for processes named "pilot" via
// /proc/<pid>/comm (world-readable, unlike /proc/<pid>/exe or fd/ — no
// ptrace permission needed just to discover candidates), excluding selfPID
// so the fd-check invocation never mistakes itself for the daemon.
func findDaemonPIDs(procRoot string, selfPID int) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procRoot, err)
	}

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid dir
		}
		if pid == selfPID {
			continue
		}
		commBytes, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "comm"))
		if err != nil {
			continue // process exited mid-scan, or comm unreadable — skip
		}
		if strings.TrimSpace(string(commBytes)) == "pilot" {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

// findOpenDBPath scans /proc/<pid>/fd for an open file descriptor whose
// target has the given base name (e.g. "pilot.db"), returning the resolved
// target path. Returns ok=false if the fd table can't be read (permission
// denied — a different UID owns the process) or no matching fd exists.
func findOpenDBPath(procRoot string, pid int, dbBaseName string) (path string, ok bool) {
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue
		}
		if filepath.Base(target) == dbBaseName {
			return target, true
		}
	}
	return "", false
}

// checkDBFD implements the GH-4393-5 assertion: the DB path Pilot's config
// resolves to (resolvedDBPath) must match the path the *live* daemon process
// actually has open, per its /proc/<pid>/fd table.
//
// GH-4393: the box daemon silently opened a shadow ledger at a config path
// that had never existed on that host, while config on disk and the daemon's
// own logs looked entirely correct for 3 hours. A check that only re-reads
// config or the lock file can be fooled the same way the daemon itself was;
// the live fd table is the one source of truth that reflects what the
// process actually has open right now, so this is meant to be polled
// externally (by pilot-board-remote, on every status refresh) rather than
// trusted to the daemon's own self-report.
func checkDBFD(cfg *config.Config, procRoot string) (*fdCheckResult, error) {
	expected := resolvedDBPath(cfg)
	dbBaseName := filepath.Base(expected)

	pids, err := findDaemonPIDs(procRoot, os.Getpid())
	if err != nil {
		return nil, err
	}

	res := &fdCheckResult{
		ConfiguredPath: expected,
		DaemonPIDs:     pids,
		OpenDBPath:     map[int]string{},
	}

	for _, pid := range pids {
		path, ok := findOpenDBPath(procRoot, pid, dbBaseName)
		if !ok {
			res.Inconclusive = append(res.Inconclusive, pid)
			continue
		}
		res.OpenDBPath[pid] = path
		if filepath.Clean(path) != filepath.Clean(expected) {
			res.Mismatched = append(res.Mismatched, pid)
		}
	}

	return res, nil
}

func newFDCheckCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "fd-check",
		Short: "Assert the configured DB path matches the live daemon's open file descriptor",
		Long: `Cheap /proc-based assertion that the SQLite ledger path Pilot's config
resolves to is the same file the running daemon process actually has open.

GH-4393: a config-supplied absolute storage path silently bypassed a
directory shim, and the daemon wrote to a shadow ledger for 3 hours before
anyone noticed — config and logs looked correct throughout. This reads the
daemon's live /proc/<pid>/fd table, which can't be fooled by a stale symlink
or a config that was edited but never reloaded. It is meant to be polled
externally (e.g. by pilot-board-remote, on every status refresh) rather than
trusted to the daemon's own self-report.

Exit codes:
  0  no daemon process running, or every daemon's open DB matches config
  1  MISMATCH — a daemon process has a different DB file open than configured
  2  INCONCLUSIVE — a daemon process was found but its fd table could not be read (permission denied)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				cfg = config.DefaultConfig()
			}

			res, err := checkDBFD(cfg, defaultProcRoot)
			if err != nil {
				return fmt.Errorf("fd-check: %w", err)
			}

			if jsonOutput {
				data, err := json.MarshalIndent(res, "", "  ")
				if err != nil {
					return fmt.Errorf("fd-check: marshal result: %w", err)
				}
				fmt.Println(string(data))
			} else {
				printFDCheckResult(res)
			}

			if len(res.Mismatched) > 0 {
				os.Exit(1)
			}
			if len(res.Inconclusive) > 0 {
				os.Exit(2)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output machine-readable JSON")

	return cmd
}

func printFDCheckResult(res *fdCheckResult) {
	fmt.Printf("Configured DB path: %s\n", res.ConfiguredPath)
	if len(res.DaemonPIDs) == 0 {
		fmt.Println("○ no running pilot daemon process found — nothing to assert")
		return
	}
	for _, pid := range res.DaemonPIDs {
		switch {
		case containsInt(res.Mismatched, pid):
			fmt.Printf("✗ MISMATCH pid %d: daemon has %q open, configured path is %q — SPLIT-BRAIN RISK (GH-4393)\n", pid, res.OpenDBPath[pid], res.ConfiguredPath)
		case containsInt(res.Inconclusive, pid):
			fmt.Printf("! INCONCLUSIVE pid %d: could not read fd table (permission denied?) — cannot verify\n", pid)
		default:
			fmt.Printf("✓ pid %d: open DB path matches configured path\n", pid)
		}
	}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
