package quality

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/procgroup"
)

type builtCommand struct {
	dir  string
	env  []string
	name string
	args []string
}

// recordingBuilder runs commands like the default and records each call. With
// from and to set, a read of a path under from reads the same path under to.
type recordingBuilder struct {
	mu         sync.Mutex
	builds     []builtCommand
	configured []time.Duration
	// startedAtConfigure records, per Configure call, whether the command had
	// already started.
	startedAtConfigure []bool
	kills              []syscall.Signal
	reads              []string
	stats              []string
	readDirs           []string
	evals              []string
	from, to           string
}

func (b *recordingBuilder) redirect(path string) string {
	if b.from == "" {
		return path
	}
	if rel, err := filepath.Rel(b.from, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(b.to, rel)
	}
	return path
}

func (b *recordingBuilder) Build(ctx context.Context, dir string, env []string, name string, args ...string) (*exec.Cmd, error) {
	b.mu.Lock()
	b.builds = append(b.builds, builtCommand{dir: dir, env: env, name: name, args: args})
	b.mu.Unlock()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd, nil
}

func (b *recordingBuilder) Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	b.mu.Lock()
	b.configured = append(b.configured, waitDelay)
	b.startedAtConfigure = append(b.startedAtConfigure, cmd.Process != nil)
	b.mu.Unlock()
	procgroup.Configure(cmd, waitDelay)
	cmd.Cancel = func() error {
		return b.Kill(cmd, syscall.SIGKILL)
	}
}

func (b *recordingBuilder) Kill(cmd *exec.Cmd, sig syscall.Signal) error {
	b.mu.Lock()
	b.kills = append(b.kills, sig)
	b.mu.Unlock()
	return procgroup.Kill(cmd, sig)
}

func (b *recordingBuilder) ReadFile(path string) ([]byte, error) {
	b.mu.Lock()
	b.reads = append(b.reads, path)
	b.mu.Unlock()
	return os.ReadFile(b.redirect(path))
}

func (b *recordingBuilder) Stat(path string) (os.FileInfo, error) {
	b.mu.Lock()
	b.stats = append(b.stats, path)
	b.mu.Unlock()
	return os.Stat(b.redirect(path))
}

func (b *recordingBuilder) ReadDir(path string) ([]os.DirEntry, error) {
	b.mu.Lock()
	b.readDirs = append(b.readDirs, path)
	b.mu.Unlock()
	return os.ReadDir(b.redirect(path))
}

func (b *recordingBuilder) EvalSymlinks(path string) (string, error) {
	b.mu.Lock()
	b.evals = append(b.evals, path)
	b.mu.Unlock()
	return filepath.EvalSymlinks(path)
}

func (b *recordingBuilder) builtNames() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for _, c := range b.builds {
		names = append(names, c.name)
	}
	return names
}

func useRecordingBuilder(t *testing.T) *recordingBuilder {
	t.Helper()
	b := &recordingBuilder{}
	SetCommandBuilder(b)
	t.Cleanup(func() { SetCommandBuilder(nil) })
	return b
}

func TestExecuteCommandUsesTheBuilder(t *testing.T) {
	t.Run("gate run builds sh in the project dir with the scope env", func(t *testing.T) {
		b := useRecordingBuilder(t)
		dir := t.TempDir()
		gate := &Gate{Name: "build", Type: GateCustom, Timeout: 10 * time.Second}
		runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, dir)
		scope := &changeScope{packages: []string{"example.com/m/a"}, reason: "changed"}

		code, out, err := runner.executeCommand(context.Background(), gate, `echo "$PILOT_CHANGED_PACKAGES"`, scope)
		if err != nil || code != 0 {
			t.Fatalf("executeCommand = (%d, %q, %v), want exit 0", code, out, err)
		}
		if !strings.Contains(out, "example.com/m/a") {
			t.Errorf("output %q lacks the scope packages", out)
		}
		if len(b.builds) != 1 {
			t.Fatalf("Build called %d times, want 1", len(b.builds))
		}
		c := b.builds[0]
		if c.name != "sh" || c.dir != dir || len(c.args) != 2 || c.args[0] != "-c" {
			t.Errorf("Build(dir=%q, name=%q, args=%q), want sh -c in %q", c.dir, c.name, c.args, dir)
		}
		for _, kv := range scope.env() {
			if !slices.Contains(c.env, kv) {
				t.Errorf("env lacks %q", kv)
			}
		}
		if len(b.configured) != 1 || b.configured[0] != gateWaitDelay {
			t.Errorf("Configure calls = %v, want one with %v", b.configured, gateWaitDelay)
		}
		if len(b.kills) != 0 {
			t.Errorf("Kill called %v on a gate that finished in time", b.kills)
		}
	})

	t.Run("gate past its time limit is ended through Kill", func(t *testing.T) {
		b := useRecordingBuilder(t)
		gate := &Gate{Name: "slow", Type: GateCustom, Timeout: 300 * time.Millisecond}
		runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())

		start := time.Now()
		_, _, err := runner.executeCommand(context.Background(), gate, "sleep 5", &changeScope{full: true})
		if !errors.Is(err, ErrGateTimeout) {
			t.Fatalf("err = %v, want ErrGateTimeout", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("executeCommand took %v, want < 3s", elapsed)
		}
		if !slices.Equal(b.kills, []syscall.Signal{syscall.SIGKILL}) {
			t.Errorf("Kill calls = %v, want one SIGKILL", b.kills)
		}
	})

	t.Run("scope builds git and go and reads test files", func(t *testing.T) {
		dir := newScopeRepo(t)
		writeFiles(t, dir, map[string]string{"mem/store.go": "package mem\n\nvar X = 1\n"})
		git(t, dir, "commit", "-q", "-am", "mem")
		b := useRecordingBuilder(t)

		got, full, reason := ChangedGoPackages(context.Background(), dir, "main")
		if full {
			t.Fatalf("scope is full (%s), want exec and mem", reason)
		}
		if !slices.Equal(got, imp("exec", "mem")) {
			t.Errorf("got %v, want exec and mem", got)
		}
		names := b.builtNames()
		if !slices.Contains(names, "git") || !slices.Contains(names, "go") {
			t.Errorf("Build names = %v, want git and go", names)
		}
		read := false
		for _, p := range b.reads {
			if filepath.Base(p) == "exec_test.go" {
				read = true
			}
		}
		if !read {
			t.Errorf("ReadFile calls = %v, want exec/exec_test.go", b.reads)
		}
	})

	t.Run("nil restores the default", func(t *testing.T) {
		b := &recordingBuilder{}
		SetCommandBuilder(b)
		if CurrentCommandBuilder() != CommandBuilder(b) {
			t.Fatal("CurrentCommandBuilder does not return the builder that was set")
		}
		SetCommandBuilder(nil)
		if _, ok := CurrentCommandBuilder().(defaultCommandBuilder); !ok {
			t.Errorf("after SetCommandBuilder(nil) the builder is %T, want defaultCommandBuilder", CurrentCommandBuilder())
		}
	})
}
