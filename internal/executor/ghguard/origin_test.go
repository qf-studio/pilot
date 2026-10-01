package ghguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestIsTestProcessArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"empty", nil, false},
		{"compiled test binary", []string{"/tmp/go-build123/b001/autopilot.test", "-test.timeout=10m0s"}, true},
		{"test binary no flags", []string{"/tmp/x/executor.test"}, true},
		{"go test", []string{"/usr/bin/go", "test", "./..."}, true},
		{"test flag only", []string{"./bin", "-test.run", "TestFoo"}, true},
		{"claude bash tool shell", []string{"/bin/bash", "-c", "gh issue close 1"}, false},
		{"plain go build", []string{"go", "build", "./..."}, false},
		{"pilot itself", []string{"/usr/local/bin/pilot", "start"}, false},
		{"arg merely mentions test", []string{"/bin/sh", "-c", "make test"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTestProcessArgv(tt.argv); got != tt.want {
				t.Errorf("IsTestProcessArgv(%v) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}

// GH-5553: ParentIsTestProcess is exercised against a real parent process
// whose command line the test controls. The test writes a shell script and
// runs it; the script runs this test binary (the "gh-guard shim" stand-in)
// as a non-exec'd child, so the child's parent is the script's shell. The
// parent's argv is `sh <dir>/parent.sh [-test.v]`, so the presence of the
// `-test.v` flag alone flips IsTestProcessArgv.

const originHelperEnv = "GHGUARD_ORIGIN_HELPER"

// TestOriginHelperProcess is not a real test: when re-invoked by
// runWithParent it prints what each detection path reports for its parent.
func TestOriginHelperProcess(t *testing.T) {
	if os.Getenv(originHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	ppid := os.Getppid()
	fmt.Printf("RESULT public=%t proc=%t ps=%t\n",
		ParentIsTestProcess(),
		IsTestProcessArgv(procArgv(ppid)),
		IsTestProcessArgv(psArgv(ppid)))
}

type parentVerdict struct{ public, proc, ps bool }

// runWithParent returns what the helper child observed about a parent shell
// started with parentArgs after the script path.
func runWithParent(t *testing.T, parentArgs ...string) parentVerdict {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	script := filepath.Join(t.TempDir(), "parent.sh")
	// Not exec'd: the shell must stay alive as the child's parent.
	body := "#!" + sh + "\n\"$GHGUARD_HELPER_BIN\" -test.run=TestOriginHelperProcess\nrc=$?\nexit $rc\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command(sh, append([]string{script}, parentArgs...)...) //nolint:gosec // test-controlled argv
	cmd.Env = append(os.Environ(), originHelperEnv+"=1", "GHGUARD_HELPER_BIN="+os.Args[0])
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper run: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`RESULT public=(\w+) proc=(\w+) ps=(\w+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("no RESULT line in helper output:\n%s", out)
	}
	return parentVerdict{m[1] == "true", m[2] == "true", m[3] == "true"}
}

func TestParentIsTestProcess_PublicEntryPoint(t *testing.T) {
	if got := runWithParent(t, "-test.v"); !got.public {
		t.Errorf("parent argv carries -test.v: ParentIsTestProcess = false, want true")
	}
	if got := runWithParent(t, "--plain"); got.public {
		t.Errorf("parent argv is plain: ParentIsTestProcess = true, want false")
	}
}

func TestParentIsTestProcess_PsFallback(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	if got := runWithParent(t, "-test.v"); !got.ps {
		t.Errorf("ps path: parent with -test.v not detected")
	}
	if got := runWithParent(t, "--plain"); got.ps {
		t.Errorf("ps path: plain parent detected as test process")
	}
}

func TestProcessArgv_UnknownPidFailsSafe(t *testing.T) {
	// A pid that cannot exist: both paths must yield "not a test process".
	const pid = 1 << 30
	if IsTestProcessArgv(processArgv(pid)) {
		t.Errorf("unreadable parent reported as test process")
	}
}
