package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runGuard runs the generated guard script the way Claude Code does: hook JSON
// on stdin. It returns the exit code and stderr.
func runGuard(t *testing.T, script, project, boundary string, hook map[string]any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(hook)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, project, boundary)
	cmd.Stdin = bytes.NewReader(raw)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	default:
		t.Fatalf("run guard: %v", err)
		return -1, ""
	}
}

func writeHook(tool, key, path string) map[string]any {
	return map[string]any{"tool_name": tool, "tool_input": map[string]any{key: path}}
}

func TestWriteBoundaryGuardScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard script is POSIX sh")
	}
	for _, bin := range []string{"jq", "realpath"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}

	project := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".agent", "system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".agent", "escape")); err != nil {
		t.Fatal(err)
	}
	// A boundary dir that is itself a symlink out of the project.
	if err := os.Symlink(outside, filepath.Join(project, "linked-boundary")); err != nil {
		t.Fatal(err)
	}

	settings, cleanup, err := setupWriteBoundary(project, ".agent")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := filepath.Join(filepath.Dir(settings), writeBoundaryGuardName)

	tests := []struct {
		name     string
		boundary string
		hook     map[string]any
		want     int
	}{
		{"inside, absolute", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "system", "a.md")), 0},
		{"inside, new nested dir", ".agent", writeHook("Edit", "file_path", filepath.Join(project, ".agent", "new", "deep", "a.md")), 0},
		{"inside, relative", ".agent", writeHook("Write", "file_path", ".agent/README.md"), 0},
		{"inside, missing parent under a real dir", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "system", "missing", "a.md")), 0},
		{"missing tail with dotdot", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "missing", "..", "..", "go.mod")), 2},
		{"symlink escape with missing tail", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "escape", "new", "deep", "pwned.txt")), 2},
		{"outside", ".agent", writeHook("Write", "file_path", filepath.Join(project, "src", "main.go")), 2},
		{"outside, relative", ".agent", writeHook("Write", "file_path", "go.mod"), 2},
		{"outside, absolute elsewhere", ".agent", writeHook("Write", "file_path", filepath.Join(outside, "x")), 2},
		{"dotdot escape", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "..", "go.mod")), 2},
		{"sibling with boundary as prefix", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent-evil", "x")), 2},
		{"boundary itself", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent")), 2},
		{"symlink escaping the boundary", ".agent", writeHook("Write", "file_path", filepath.Join(project, ".agent", "escape", "pwned.txt")), 2},
		{"boundary dir symlinked out of the project", "linked-boundary", writeHook("Write", "file_path", filepath.Join(project, "linked-boundary", "x")), 2},
		{"notebook path inside", ".agent", writeHook("NotebookEdit", "notebook_path", filepath.Join(project, ".agent", "n.ipynb")), 0},
		{"notebook path outside", ".agent", writeHook("NotebookEdit", "notebook_path", filepath.Join(project, "n.ipynb")), 2},
		{"no path in input", ".agent", map[string]any{"tool_name": "Write", "tool_input": map[string]any{}}, 2},
		{"deny-all boundary", WriteBoundaryDenyAll, writeHook("Write", "file_path", filepath.Join(project, ".agent", "a.md")), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := runGuard(t, script, project, tt.boundary, tt.hook)
			if code != tt.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tt.want, stderr)
			}
			if tt.want == 2 && strings.TrimSpace(stderr) == "" {
				t.Error("blocked call must explain itself on stderr")
			}
		})
	}
}

func TestSetupWriteBoundary_CleanupRemovesFiles(t *testing.T) {
	settings, cleanup, err := setupWriteBoundary(t.TempDir(), ".agent")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(settings)
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("run dir still exists after cleanup: %v", err)
	}
}

func TestShellSingleQuote(t *testing.T) {
	if got, want := shellSingleQuote("a'b c"), `'a'\''b c'`; got != want {
		t.Errorf("shellSingleQuote = %s, want %s", got, want)
	}
}
