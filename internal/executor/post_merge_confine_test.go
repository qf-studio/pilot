package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// confineFixture is a worktree stand-in with an outside directory next to it.
type confineFixture struct {
	script  string // installed hook script
	root    string // the worktree
	outside string // a sibling directory holding secret.txt
	denyLog string
}

func newConfineFixture(t *testing.T) confineFixture {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the confinement hook needs jq")
	}
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	f := confineFixture{
		root:    filepath.Join(base, "wt"),
		outside: filepath.Join(base, "outside"),
		denyLog: filepath.Join(base, "denied.log"),
	}
	hooks := filepath.Join(base, "hooks")
	if err := WriteEmbeddedScripts(hooks); err != nil {
		t.Fatal(err)
	}
	f.script = filepath.Join(hooks, postMergeConfineScript)
	for _, d := range []string{filepath.Join(f.root, "sub"), f.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(f.root, "sub", "file.go"): "package sub\n",
		filepath.Join(f.outside, "secret.txt"):  "token\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(f.outside, filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outside, "secret.txt"), filepath.Join(f.root, "linkfile")); err != nil {
		t.Fatal(err)
	}
	return f
}

// run feeds one PreToolUse payload to the hook and returns its exit code and
// stderr.
func (f confineFixture) run(t *testing.T, tool string, input map[string]any) (int, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tool_name": tool, "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	return runHookCommand(t, shellCommand(f.script, f.root, f.denyLog), payload)
}

func shellCommand(parts ...string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
}

// runHookCommand runs a settings.json hook command the way the CLI does.
func runHookCommand(t *testing.T, command string, stdin []byte) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stderr.String()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), stderr.String()
	}
	t.Fatalf("run hook: %v", err)
	return -1, ""
}

func (f confineFixture) denied(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.denyLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestPostMergeCheckoutConfinement_PathDecisions(t *testing.T) {
	f := newConfineFixture(t)
	inFile := filepath.Join(f.root, "sub", "file.go")
	outFile := filepath.Join(f.outside, "secret.txt")
	tests := []struct {
		name     string
		tool     string
		input    map[string]any
		wantExit int
		wantPath string // recorded in the deny log when denied
	}{
		{"read inside, absolute", "Read", map[string]any{"file_path": inFile}, 0, ""},
		{"read inside, relative", "Read", map[string]any{"file_path": "sub/file.go"}, 0, ""},
		{"read inside, not yet existing", "Read", map[string]any{"file_path": filepath.Join(f.root, "new.go")}, 0, ""},
		{"read the root itself", "Read", map[string]any{"file_path": f.root}, 0, ""},
		{"read absolute outside", "Read", map[string]any{"file_path": outFile}, 2, outFile},
		{"read sibling sharing the root prefix", "Read", map[string]any{"file_path": f.root + "-evil/x"}, 2, f.root + "-evil/x"},
		{"read home-relative", "Read", map[string]any{"file_path": "~/.ssh/id_rsa"}, 2, "~/.ssh/id_rsa"},
		{"read without a path", "Read", map[string]any{}, 2, ""},
		{"grep default path", "Grep", map[string]any{"pattern": "x"}, 0, ""},
		{"grep inside", "Grep", map[string]any{"pattern": "x", "path": filepath.Join(f.root, "sub")}, 0, ""},
		{"grep outside", "Grep", map[string]any{"pattern": "x", "path": f.outside}, 2, f.outside},
		{"grep glob filter with absolute alternative", "Grep", map[string]any{"pattern": "x", "glob": "{*.go,/etc/*}"}, 2, "{*.go,/etc/*}"},
		{"glob relative pattern", "Glob", map[string]any{"pattern": "**/*.go"}, 0, ""},
		{"glob absolute pattern inside", "Glob", map[string]any{"pattern": filepath.Join(f.root, "sub") + "/*.go"}, 0, ""},
		{"glob absolute pattern outside", "Glob", map[string]any{"pattern": f.outside + "/*"}, 2, f.outside},
		{"glob path outside", "Glob", map[string]any{"pattern": "*", "path": f.outside}, 2, f.outside},
		{"glob absolute alternative", "Glob", map[string]any{"pattern": "{a,/etc/passwd}"}, 2, "{a,/etc/passwd}"},
		{"bash", "Bash", map[string]any{"command": "cat /etc/passwd"}, 2, ""},
		{"edit", "Edit", map[string]any{"file_path": inFile}, 2, ""},
		{"write", "Write", map[string]any{"file_path": inFile}, 2, ""},
		{"web fetch", "WebFetch", map[string]any{"url": "https://example.com"}, 2, ""},
		{"web search", "WebSearch", map[string]any{"query": "x"}, 2, ""},
		{"unknown tool", "mcp__x__y", map[string]any{}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(f.denyLog)
			exit, stderr := f.run(t, tt.tool, tt.input)
			if exit != tt.wantExit {
				t.Fatalf("exit = %d (stderr %q), want %d", exit, stderr, tt.wantExit)
			}
			log := f.denied(t)
			if tt.wantExit == 0 {
				if log != "" {
					t.Errorf("allowed call wrote to the deny log: %q", log)
				}
				return
			}
			if !strings.HasPrefix(log, tt.tool+"\t"+tt.wantPath) {
				t.Errorf("deny log = %q, want it to start with %q", log, tt.tool+"\t"+tt.wantPath)
			}
			if !strings.Contains(stderr, "denied") {
				t.Errorf("stderr = %q, want a denial reason for the model", stderr)
			}
		})
	}
}

func TestPostMergeCheckoutConfinement_SymlinkEscapeDenied(t *testing.T) {
	f := newConfineFixture(t)
	tests := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"read through a symlinked directory", "Read", map[string]any{"file_path": filepath.Join(f.root, "link", "secret.txt")}},
		{"read a symlinked file", "Read", map[string]any{"file_path": filepath.Join(f.root, "linkfile")}},
		{"read a symlink with a relative path", "Read", map[string]any{"file_path": "link/secret.txt"}},
		{"grep a symlinked directory", "Grep", map[string]any{"pattern": "x", "path": filepath.Join(f.root, "link")}},
		{"glob through a symlinked directory", "Glob", map[string]any{"pattern": "link/*"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if exit, stderr := f.run(t, tt.tool, tt.input); exit != 2 {
				t.Fatalf("exit = %d (stderr %q), want a deny (2): the symlink escapes the worktree", exit, stderr)
			}
		})
	}
}

func TestPostMergeCheckoutConfinement_TraversalDenied(t *testing.T) {
	f := newConfineFixture(t)
	wt := filepath.Base(f.root)
	// Joined by hand: filepath.Join would clean the .. segments away.
	tests := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{"escapes the worktree", "Read", map[string]any{"file_path": f.root + "/../outside/secret.txt"}},
		{"escapes, relative", "Read", map[string]any{"file_path": "../outside/secret.txt"}},
		// These resolve back inside the worktree: a .. segment is refused all
		// the same, because next to a symlink it means different things
		// lexically and physically.
		{"returns inside the worktree", "Read", map[string]any{"file_path": f.root + "/sub/../sub/file.go"}},
		{"returns inside via the root name", "Read", map[string]any{"file_path": f.root + "/../" + wt + "/sub/file.go"}},
		{"grep path", "Grep", map[string]any{"pattern": "x", "path": f.root + "/sub/.."}},
		{"glob pattern", "Glob", map[string]any{"pattern": "sub/../**/*.go"}},
		{"glob alternative", "Glob", map[string]any{"pattern": "{sub,..}/*.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if exit, stderr := f.run(t, tt.tool, tt.input); exit != 2 {
				t.Fatalf("exit = %d (stderr %q), want a deny (2): the path has a .. segment", exit, stderr)
			}
		})
	}
}

func TestPostMergeCheckoutConfinement_FailsClosed(t *testing.T) {
	f := newConfineFixture(t)
	payload := []byte(`{"tool_name":"Read","tool_input":{"file_path":"` + filepath.Join(f.root, "sub", "file.go") + `"}}`)
	t.Run("no root", func(t *testing.T) {
		if exit, _ := runHookCommand(t, shellCommand(f.script, "", f.denyLog), payload); exit != 2 {
			t.Errorf("exit = %d, want 2", exit)
		}
	})
	t.Run("malformed input", func(t *testing.T) {
		if exit, _ := runHookCommand(t, shellCommand(f.script, f.root, f.denyLog), []byte("not json")); exit != 2 {
			t.Errorf("exit = %d, want 2", exit)
		}
	})
}

// During the run the worktree carries the confinement settings and the hook
// they name does its job; afterwards the worktree and the environment are gone.
func TestPostMergeCheckoutConfinement_SettingsDuringRun(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the confinement hook needs jq")
	}
	repo, sha := initCheckoutRepo(t)
	// The merged commit carries its own settings; they must not survive.
	hostile := `{"hooks":{"PreToolUse":[{"matcher":".*","hooks":[{"type":"command","command":"true"}]}]}}`
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		if err := os.WriteFile(filepath.Join(repo, ".claude", name), []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-f", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "claude settings"}, {"rev-parse", "HEAD"}} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		if args[0] == "rev-parse" {
			sha = strings.TrimSpace(string(out))
		}
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("token"), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	var gotDir, gotHome, scriptPath string
	r := newPostMergeReviewerWithHomeRunner(func(_ context.Context, dir, home string, _ ...string) ([]byte, error) {
		gotDir, gotHome = dir, home

		raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("settings.json missing in the worktree during the run: %v", err)
		}
		var settings ClaudeSettings
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatalf("settings.json: %v", err)
		}
		entries := settings.Hooks["PreToolUse"]
		var command string
		for _, e := range entries {
			if e.Matcher != nil && *e.Matcher == "Read|Grep|Glob" && len(e.Hooks) == 1 {
				command = e.Hooks[0].Command
			}
		}
		if command == "" {
			t.Fatalf("no PreToolUse hook on Read|Grep|Glob in %s", raw)
		}
		if strings.Contains(string(raw), `"command":"true"`) || len(entries) != 2 {
			t.Errorf("hostile committed settings leaked into %s", raw)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.local.json")); !os.IsNotExist(err) {
			t.Errorf("committed settings.local.json still present (err = %v)", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
			t.Errorf("review HOME carries no settings file: %v", err)
		}
		fields := strings.Fields(command)
		scriptPath = strings.Trim(fields[0], "'")
		if strings.HasPrefix(scriptPath, dir) {
			t.Errorf("hook script %s lives under the checkout %s", scriptPath, dir)
		}

		denyIn, _ := json.Marshal(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": outside}})
		if exit, stderr := runHookCommand(t, command, denyIn); exit != 2 {
			t.Errorf("Read of %s: exit = %d (stderr %q), want a deny", outside, exit, stderr)
		}
		allowIn, _ := json.Marshal(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(dir, "server", "handler.go")}})
		if exit, stderr := runHookCommand(t, command, allowIn); exit != 0 {
			t.Errorf("Read inside the worktree: exit = %d (stderr %q), want allow", exit, stderr)
		}
		return []byte("VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"), nil
	})
	r.log = slog.New(slog.NewTextHandler(&logs, nil))

	if _, err := r.Review(context.Background(), checkoutInput(repo, sha)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	warn := 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "tool=Read") && strings.Contains(line, "path="+outside) {
			warn++
		}
	}
	if warn != 1 {
		t.Errorf("want one Warn line naming tool=Read path=%s, got %d in:\n%s", outside, warn, logs.String())
	}
	for _, gone := range []string{gotDir, filepath.Join(gotDir, ".claude", "settings.json"), gotHome, scriptPath} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the review (stat err = %v)", gone, err)
		}
	}
}

func TestPostMergeCheckoutConfinement_Environ(t *testing.T) {
	home := t.TempDir()
	realHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realHome, ".claude", postMergeCredentialsFile), []byte("fake-login"), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"GITHUB_TOKEN=test-github-token", "GH_TOKEN=test-github-token",
		"TELEGRAM_BOT_TOKEN=test-telegram-bot-token", "SLACK_BOT_TOKEN=test-slack-bot-token",
		"LINEAR_API_KEY=fake-api-key", "AWS_SECRET_ACCESS_KEY=fake-aws-secret",
	}
	tests := []struct {
		name          string
		base          []string
		wantKept      []string
		wantCopied    bool
		wantConfigDir string
	}{
		{"api key authenticates", append([]string{"HOME=" + realHome, "PATH=/bin", "ANTHROPIC_API_KEY=fake-api-key"}, secrets...), []string{"PATH=/bin", "ANTHROPIC_API_KEY=fake-api-key"}, false, filepath.Join(home, ".claude")},
		{"login file authenticates", append([]string{"HOME=" + realHome, "PATH=/bin"}, secrets...), []string{"PATH=/bin"}, true, filepath.Join(home, ".claude")},
		{"ambient config dir is replaced", append([]string{"HOME=" + realHome, "CLAUDE_CONFIG_DIR=" + filepath.Join(realHome, ".claude"), "PATH=/bin"}, secrets...), []string{"PATH=/bin"}, true, filepath.Join(home, ".claude")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.RemoveAll(filepath.Join(home, ".claude"))
			got := postMergeReviewEnviron(tt.base, home)
			set := map[string]bool{}
			for _, kv := range got {
				set[kv] = true
				name, _, _ := strings.Cut(kv, "=")
				for _, s := range secrets {
					if strings.HasPrefix(s, name+"=") {
						t.Errorf("secret %s reached the review subprocess", name)
					}
				}
				if name == "HOME" && kv != "HOME="+home {
					t.Errorf("HOME = %q, want the review home", kv)
				}
			}
			for _, want := range append(tt.wantKept, "HOME="+home, "CLAUDE_CONFIG_DIR="+tt.wantConfigDir) {
				if !set[want] {
					t.Errorf("environment lacks %q: %v", want, got)
				}
			}
			_, err := os.Stat(filepath.Join(home, ".claude", postMergeCredentialsFile))
			if copied := err == nil; copied != tt.wantCopied {
				t.Errorf("login file copied = %v, want %v", copied, tt.wantCopied)
			}
		})
	}
}

// A review that runs out of turns is recorded as exhausted: one Warn line with
// the turns used and the files read, no verdict, nothing to post.
func TestPostMergeCheckoutMaxTurns_Exhausted(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	producer := "server/handler.go"
	narration := streamLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": "Checking one more counterpart before I answer."},
	}}})
	tests := []struct {
		name      string
		stream    string
		wantTurns int
	}{
		{"result event carries no answer", streamLine(t, streamToolUse("Read", producer)) + narration +
			streamLine(t, map[string]any{"type": "result", "subtype": "error_max_turns", "num_turns": 13}), 13},
		{"stream ends without a result event", streamLine(t, streamToolUse("Read", producer)) + narration, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			var gotArgs []string
			r := newPostMergeReviewerWithDirRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
				gotArgs = args
				return []byte(tt.stream), nil
			})
			r.log = slog.New(slog.NewTextHandler(&logs, nil))
			got, err := r.Review(context.Background(), checkoutInput(repo, sha))
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if v, _ := flagValue(gotArgs, "--max-turns"); v != "12" {
				t.Errorf("--max-turns = %q, want 12", v)
			}
			if got.Verdict != PostMergeExhausted || got.Verdict.Postable() {
				t.Errorf("verdict = %q, want exhausted and not postable", got.Verdict)
			}
			if len(got.Defects) != 0 || got.Notes != "" {
				t.Errorf("narration leaked into the review: %+v", got)
			}
			if !got.Checkout || got.FilesRead != 1 || got.Turns != tt.wantTurns {
				t.Errorf("Checkout=%v FilesRead=%d Turns=%d, want true, 1, %d", got.Checkout, got.FilesRead, got.Turns, tt.wantTurns)
			}
			out := logs.String()
			for _, want := range []string{"level=WARN", `msg="post-merge review exhausted"`, "files_read=1"} {
				if !strings.Contains(out, want) {
					t.Errorf("log lacks %q:\n%s", want, out)
				}
			}
			if !strings.Contains(out, "turns="+strconv.Itoa(tt.wantTurns)) {
				t.Errorf("log lacks turns=%d:\n%s", tt.wantTurns, out)
			}
		})
	}

	t.Run("an answer in the last assistant text is still read", func(t *testing.T) {
		answer := streamLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
			{"type": "text", "text": "VERDICT: APPROVE\nDEFECTS:\nnone\nNOTES:\n"},
		}}})
		r := newPostMergeReviewerWithDirRunner(func(context.Context, string, ...string) ([]byte, error) {
			return []byte(streamLine(t, streamToolUse("Read", producer)) + answer), nil
		})
		got, err := r.Review(context.Background(), checkoutInput(repo, sha))
		if err != nil || got.Verdict != PostMergeApprove {
			t.Fatalf("verdict = %v, err = %v, want APPROVE", got, err)
		}
	})
}

// The sweep needs a git repo for the worktree entries and an age to compare.
func TestPostMergeCheckoutSweep_OldDirectoryRemoved(t *testing.T) {
	repo, sha := initCheckoutRepo(t)
	t.Setenv("TMPDIR", t.TempDir())
	tmp := os.TempDir()

	stale, err := createPostMergeCheckout(context.Background(), repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := createPostMergeCheckout(context.Background(), repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.cleanup()
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	if err := os.Chtimes(stale.path, old, old); err != nil {
		t.Fatal(err)
	}
	// A directory of another family is not the sweep's to remove.
	other := filepath.Join(tmp, "pilot-worktree-GH-1-123")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	if n := worktreeCount(t, repo); n != 3 {
		t.Fatalf("worktrees before the sweep = %d, want 3", n)
	}

	removed, pruned := SweepStalePostMergeCheckouts(context.Background(), tmp, PostMergeSweepThreshold(5*time.Minute), now, []string{repo})

	if removed != 1 || pruned != 1 {
		t.Errorf("removed=%d pruned=%d, want 1 and 1", removed, pruned)
	}
	if _, err := os.Stat(stale.path); !os.IsNotExist(err) {
		t.Errorf("stale directory %s survived the sweep (stat err = %v)", stale.path, err)
	}
	if _, err := os.Stat(fresh.path); err != nil {
		t.Errorf("fresh directory removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated directory removed: %v", err)
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v: %s", err, out)
	}
	if strings.Contains(string(out), stale.path) {
		t.Errorf("git still lists the stale worktree:\n%s", out)
	}
	if !strings.Contains(string(out), fresh.path) {
		t.Errorf("git lost the fresh worktree:\n%s", out)
	}
}

func TestPostMergeCheckoutSweep_Threshold(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"configured timeout plus an hour", 10 * time.Minute, 70 * time.Minute},
		{"default timeout plus an hour", 0, defaultPostMergeReviewTimeout + time.Hour},
	}
	for _, tt := range tests {
		if got := PostMergeSweepThreshold(tt.timeout); got != tt.want {
			t.Errorf("%s: threshold = %s, want %s", tt.name, got, tt.want)
		}
	}

	ages := []struct {
		age  time.Duration
		gone bool
	}{
		{10 * time.Minute, false},
		{65 * time.Minute, false}, // just inside the threshold of a 5m review: kept
		{2 * time.Hour, true},
	}
	tmp := t.TempDir()
	now := time.Now()
	var dirs []string
	for i, a := range ages {
		d := filepath.Join(tmp, postMergeCheckoutPrefix+"age"+string(rune('a'+i)))
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-a.age + time.Minute)
		if err := os.Chtimes(d, mt, mt); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	SweepStalePostMergeCheckouts(context.Background(), tmp, PostMergeSweepThreshold(5*time.Minute), now, nil)
	for i, a := range ages {
		_, err := os.Stat(dirs[i])
		if gone := os.IsNotExist(err); gone != a.gone {
			t.Errorf("age %s: removed = %v, want %v", a.age, gone, a.gone)
		}
	}
}

func TestPostMergeCheckoutSweep_SymlinkNotFollowed(t *testing.T) {
	tmp := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, postMergeCheckoutPrefix+"link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	if removed, _ := SweepStalePostMergeCheckouts(context.Background(), tmp, time.Hour, time.Now(), nil); removed != 0 {
		t.Errorf("removed = %d, want 0 for a symlink", removed)
	}
	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Errorf("symlink target was swept: %v", err)
	}
}
