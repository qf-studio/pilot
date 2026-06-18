package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// fakeGhRunner builds a fake ghRunner that replies based on the subcommand.
func fakeGhRunner(t *testing.T, statusOK bool, username, token string, repos []ghRepoEntry) func(args ...string) ([]byte, error) {
	t.Helper()
	return func(args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("no args")
		}
		switch args[0] {
		case "auth":
			if len(args) >= 2 && args[1] == "status" {
				if !statusOK {
					return nil, fmt.Errorf("not authenticated")
				}
				return []byte("Logged in"), nil
			}
			if len(args) >= 2 && args[1] == "token" {
				if token == "" {
					return nil, fmt.Errorf("no token")
				}
				return []byte(token + "\n"), nil
			}
		case "api":
			return []byte(username + "\n"), nil
		case "repo":
			data, err := json.Marshal(repos)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
		return nil, fmt.Errorf("unknown gh command: %v", args)
	}
}

// writeTempConfig writes an empty default config to a temp dir and returns the path.
func writeTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig()
	if err := config.Save(cfg, path); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return path
}

// TestGhAuthStatus_Authenticated confirms (username, true) when gh succeeds.
func TestGhAuthStatus_Authenticated(t *testing.T) {
	orig := ghRunner
	t.Cleanup(func() { ghRunner = orig })
	ghRunner = fakeGhRunner(t, true, "alice", "fake-token", nil)

	user, ok := ghAuthStatus()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if user != "alice" {
		t.Fatalf("expected username=alice, got %q", user)
	}
}

// TestGhAuthStatus_Unauthenticated confirms (_, false) when gh is absent.
func TestGhAuthStatus_Unauthenticated(t *testing.T) {
	orig := ghRunner
	t.Cleanup(func() { ghRunner = orig })
	ghRunner = fakeGhRunner(t, false, "", "", nil)

	_, ok := ghAuthStatus()
	if ok {
		t.Fatal("expected ok=false")
	}
}

// TestGhAuthToken returns the token trimmed of trailing newline.
func TestGhAuthToken(t *testing.T) {
	orig := ghRunner
	t.Cleanup(func() { ghRunner = orig })
	ghRunner = fakeGhRunner(t, true, "alice", "fake-test-gh-token", nil)

	tok, err := ghAuthToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "fake-test-gh-token" {
		t.Fatalf("expected fake-test-gh-token, got %q", tok)
	}
}

// TestGhRepoList parses the JSON returned by the fake gh runner.
func TestGhRepoList(t *testing.T) {
	orig := ghRunner
	t.Cleanup(func() { ghRunner = orig })

	repos := []ghRepoEntry{
		{NameWithOwner: "alice/foo", Description: "a repo", IsPrivate: false},
		{NameWithOwner: "alice/bar", Description: "", IsPrivate: true},
	}
	ghRunner = fakeGhRunner(t, true, "alice", "fake-token", repos)

	got, err := ghRepoList(50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(got))
	}
	if got[0].NameWithOwner != "alice/foo" {
		t.Errorf("expected alice/foo, got %q", got[0].NameWithOwner)
	}
	if !got[1].IsPrivate {
		t.Error("expected bar to be private")
	}
}

// TestProjectAddFlags_Basic verifies the flag-driven path writes a project.
func TestProjectAddFlags_Basic(t *testing.T) {
	dir := t.TempDir()

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	if err := runProjectAddFlags(nil, "myapp", dir, "", "", false, false); err != nil {
		t.Fatalf("runProjectAddFlags: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}
	if cfg.Projects[0].Name != "myapp" {
		t.Errorf("expected name=myapp, got %q", cfg.Projects[0].Name)
	}
	if cfg.Projects[0].Path != dir {
		t.Errorf("expected path=%s, got %q", dir, cfg.Projects[0].Path)
	}
}

// TestProjectAddFlags_DuplicateName rejects adding the same name twice.
func TestProjectAddFlags_DuplicateName(t *testing.T) {
	dir := t.TempDir()

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	if err := runProjectAddFlags(nil, "dupe", dir, "", "", false, false); err != nil {
		t.Fatalf("first add: %v", err)
	}

	dir2 := t.TempDir()
	err := runProjectAddFlags(nil, "dupe", dir2, "", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate-name error, got: %v", err)
	}
}

// TestProjectAddFlags_DuplicatePath rejects a path that is already configured.
func TestProjectAddFlags_DuplicatePath(t *testing.T) {
	dir := t.TempDir()

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	if err := runProjectAddFlags(nil, "first", dir, "", "", false, false); err != nil {
		t.Fatalf("first add: %v", err)
	}

	err := runProjectAddFlags(nil, "second", dir, "", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("expected duplicate-path error, got: %v", err)
	}
}

// TestProjectAddFlags_NameRequired returns an error when --name is empty.
func TestProjectAddFlags_NameRequired(t *testing.T) {
	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	err := runProjectAddFlags(nil, "", "", "", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("expected --name required error, got: %v", err)
	}
}

// TestProjectAddFlags_GitHubParsed verifies owner/repo is split from --github.
func TestProjectAddFlags_GitHubParsed(t *testing.T) {
	dir := t.TempDir()

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	if err := runProjectAddFlags(nil, "ghtest", dir, "alice/myrepo", "", false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Projects[0].GitHub == nil {
		t.Fatal("expected GitHub to be set")
	}
	if cfg.Projects[0].GitHub.Owner != "alice" || cfg.Projects[0].GitHub.Repo != "myrepo" {
		t.Errorf("unexpected GitHub config: %+v", cfg.Projects[0].GitHub)
	}
}

// TestProjectAddFlags_InvalidGitHub rejects a --github value without a slash.
func TestProjectAddFlags_InvalidGitHub(t *testing.T) {
	dir := t.TempDir()

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	err := runProjectAddFlags(nil, "bad", dir, "noslash", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "invalid GitHub format") {
		t.Fatalf("expected invalid-format error, got: %v", err)
	}
}

// TestIsInteractiveTTY is false in a `go test` run (stdin is a pipe).
func TestIsInteractiveTTY(t *testing.T) {
	// In a standard `go test` run stdin is not a terminal.
	// If it somehow is, just skip — the function is correct either way.
	if isInteractiveTTY() {
		t.Skip("stdin is a TTY in this environment")
	}
}

// TestProjectAddWizard_FallbackWhenGhAbsent confirms that runProjectAddWizard
// prints a hint and returns nil (no error) when gh is unavailable.
func TestProjectAddWizard_FallbackWhenGhAbsent(t *testing.T) {
	orig := ghRunner
	t.Cleanup(func() { ghRunner = orig })
	ghRunner = fakeGhRunner(t, false, "", "", nil)

	cfgPath := writeTempConfig(t)
	oldCfg := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfg })

	err := runProjectAddWizard(nil)
	if err != nil {
		t.Fatalf("expected nil error on fallback, got: %v", err)
	}
}
