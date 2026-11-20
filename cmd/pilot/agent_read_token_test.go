package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// TestAgentReadTokenProvider_NilWithoutApp: with no GitHub App configured
// there is no provider, even when the daemon holds a config token and a
// GITHUB_TOKEN — the token of the daemon is never handed to the agent.
func TestAgentReadTokenProvider_NilWithoutApp(t *testing.T) {
	resetGitHubTokenTestState(t)
	if err := os.Setenv("GITHUB_TOKEN", "test-github-token"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	for name, cfg := range map[string]*config.Config{
		"nil config":   nil,
		"no adapters":  {},
		"no github":    {Adapters: &config.AdaptersConfig{}},
		"token no app": {Adapters: &config.AdaptersConfig{GitHub: &github.Config{Enabled: true, Token: "config-token"}}},
	} {
		if agentReadTokenProvider(cfg) != nil {
			t.Errorf("%s: agentReadTokenProvider() != nil, want nil with no GitHub App", name)
		}
	}
}

// TestAgentReadTokenProvider_AppMintFailureNoFallback: a failed App mint
// gives an error, not the config token that resolveGitHubToken falls back to.
func TestAgentReadTokenProvider_AppMintFailureNoFallback(t *testing.T) {
	resetGitHubTokenTestState(t)

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{
			Enabled: true,
			Token:   "config-token",
			Repo:    "owner/repo",
			App: &github.AppConfig{
				AppID:          123456,
				InstallationID: 78901234,
				PrivateKeyPath: "/nonexistent/github-app.pem",
			},
		},
	}}

	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	token, _, err := provider(context.Background(), "owner/repo")
	if err == nil {
		t.Fatal("provider() = nil error, want the mint error of the unreadable key")
	}
	if token != "" {
		t.Errorf("provider() token = %q, want empty (no fallback to the daemon token)", token)
	}
}

// testAgentAppConfig writes an RSA key made in the test and returns an App
// config that points at it.
func testAgentAppConfig(t *testing.T) *github.AppConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test-github-app.pem")
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing test private key: %v", err)
	}
	return &github.AppConfig{AppID: 123456, InstallationID: 78901234, PrivateKeyPath: path}
}

// TestAgentReadTokenProviderNeverReturnsTheFullToken: the provider returns
// the scoped read token of the configured repository — never the full token
// of the source, also not after a failed scoped call — and refuses each
// repository that is not in the GitHub config without a request.
func TestAgentReadTokenProviderNeverReturnsTheFullToken(t *testing.T) {
	resetGitHubTokenTestState(t)

	const fullToken = "test-full-installation-token"
	const scopedToken = "test-scoped-read-token"
	type scopedBody struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	var (
		mu         sync.Mutex
		scopedReqs []scopedBody
		fullMints  int64
		failScoped atomic.Bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(raw) == 0 {
			// The full mint: no body, every repository of the installation.
			atomic.AddInt64(&fullMints, 1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":                fullToken,
				"expires_at":           time.Now().Add(time.Hour),
				"repository_selection": "all",
				"permissions":          map[string]string{"contents": "write", "pull_requests": "write"},
			})
			return
		}
		var body scopedBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("scoped request body is not JSON: %v", err)
		}
		mu.Lock()
		scopedReqs = append(scopedReqs, body)
		mu.Unlock()
		if failScoped.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
			return
		}
		// The installation of this server holds qf-studio/pilot and
		// acme/widget.
		owners := map[string]string{"pilot": "qf-studio", "widget": "acme"}
		repos := make([]map[string]string, 0, len(body.Repositories))
		for _, name := range body.Repositories {
			repos = append(repos, map[string]string{"name": name, "full_name": owners[name] + "/" + name})
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":                scopedToken,
			"expires_at":           time.Now().Add(time.Hour),
			"repository_selection": "selected",
			"repositories":         repos,
			"permissions":          body.Permissions,
		})
	}))
	defer server.Close()

	appCfg := testAgentAppConfig(t)
	source, err := github.NewTokenSourceWithBaseURL(appCfg, server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	// Put the source where the provider reads it.
	ghAppTokenCache.once.Do(func() { ghAppTokenCache.source = source })

	ctx := context.Background()
	full, err := source.Token(ctx)
	if err != nil || full != fullToken {
		t.Fatalf("source.Token() = (%q, %v), want the full token first", full, err)
	}

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{GitHub: &github.Config{
			Enabled: true,
			Token:   "config-token",
			Repo:    "qf-studio/pilot",
			App:     appCfg,
		}},
		Projects: []*config.ProjectConfig{
			{Name: "widget", GitHub: &config.ProjectGitHubConfig{Owner: "acme", Repo: "widget"}},
		},
	}
	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	scopedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(scopedReqs)
	}
	wantPerms := map[string]string{"contents": "read", "issues": "read", "pull_requests": "read", "metadata": "read"}

	for _, tc := range []struct{ repo, name string }{
		{"qf-studio/pilot", "pilot"},
		{"QF-Studio/Pilot", "pilot"}, // the name of the config entry, not of the argument
		{"acme/widget", "widget"},
	} {
		before := scopedCount()
		token, expires, err := provider(ctx, tc.repo)
		if err != nil {
			t.Fatalf("provider(%q) error = %v", tc.repo, err)
		}
		if token == full {
			t.Fatalf("provider(%q) returned the full token of the source", tc.repo)
		}
		if token != scopedToken {
			t.Errorf("provider(%q) token = %q, want the scoped token", tc.repo, token)
		}
		if time.Until(expires) < 50*time.Minute {
			t.Errorf("provider(%q) expires = %v, want ~1h", tc.repo, expires)
		}
		if got := scopedCount(); got != before+1 {
			t.Fatalf("provider(%q) made %d scoped requests, want 1", tc.repo, got-before)
		}
		mu.Lock()
		body := scopedReqs[len(scopedReqs)-1]
		mu.Unlock()
		if !reflect.DeepEqual(body.Repositories, []string{tc.name}) {
			t.Errorf("provider(%q) posted repositories %v, want [%s]", tc.repo, body.Repositories, tc.name)
		}
		if !reflect.DeepEqual(body.Permissions, wantPerms) {
			t.Errorf("provider(%q) posted permissions %v, want %v", tc.repo, body.Permissions, wantPerms)
		}
	}

	// A failed scoped call gives an error, not the full token.
	failScoped.Store(true)
	before := scopedCount()
	token, _, err := provider(ctx, "qf-studio/pilot")
	if err == nil {
		t.Error("provider() after a 500 = nil error, want the error of the scoped call")
	}
	if token != "" {
		t.Errorf("provider() after a 500 token = %q, want empty", token)
	}
	if got := scopedCount(); got != before+1 {
		t.Errorf("provider() after a 500 made %d scoped requests, want 1", got-before)
	}
	failScoped.Store(false)

	// A repository outside the config gives an error and no request.
	for _, repo := range []string{
		"qf-studio/other", // not in the config
		"evil/pilot",      // another owner, a configured repository name
		"evil/widget",
		"pilot",                 // no owner
		"",                      // empty
		"/pilot",                // empty owner
		"qf-studio/",            // empty name
		"qf-studio/pilot/extra", // nested path
		" qf-studio/pilot",      // space
	} {
		before := scopedCount()
		token, _, err := provider(ctx, repo)
		if err == nil {
			t.Errorf("provider(%q) = nil error, want a refusal", repo)
		}
		if token != "" {
			t.Errorf("provider(%q) token = %q, want empty", repo, token)
		}
		if got := scopedCount(); got != before {
			t.Errorf("provider(%q) made %d scoped requests, want 0", repo, got-before)
		}
	}
	if n := atomic.LoadInt64(&fullMints); n != 1 {
		t.Errorf("full mints = %d, want 1 (only the one made by the test)", n)
	}
}

// TestScopedTokenChecksTheOwnerOfTheAnswer: the request to GitHub names the
// repository with no owner, so only the full_name of the answer shows that
// the token is for the repository of the owner asked for.
func TestScopedTokenChecksTheOwnerOfTheAnswer(t *testing.T) {
	resetGitHubTokenTestState(t)

	// answerFor is the full_name the server puts in its answer.
	var answerFor atomic.Value
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		fullName, _ := answerFor.Load().(string)
		_, name, _ := strings.Cut(fullName, "/")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":                "test-scoped-read-token",
			"expires_at":           time.Now().Add(time.Hour),
			"repository_selection": "selected",
			"repositories":         []map[string]string{{"name": name, "full_name": fullName}},
			"permissions":          github.ReadOnlyPermissions(),
		})
	}))
	defer server.Close()

	appCfg := testAgentAppConfig(t)
	source, err := github.NewTokenSourceWithBaseURL(appCfg, server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	ctx := context.Background()
	read := github.ReadOnlyPermissions()

	// The right name, the full_name of another owner.
	answerFor.Store("owner-a/name")
	token, expires, err := source.ScopedToken(ctx, "owner-b", "name", read)
	if err == nil {
		t.Error("ScopedToken(owner-b, name) with an answer for owner-a/name = nil error, want a refusal")
	}
	if token != "" || !expires.IsZero() {
		t.Errorf("ScopedToken(owner-b, name) = (%q, %v), want no token", token, expires)
	}

	// The right full_name in another letter case.
	answerFor.Store("Owner-B/Name")
	if token, _, err := source.ScopedToken(ctx, "owner-b", "name", read); err != nil || token != "test-scoped-read-token" {
		t.Errorf("ScopedToken(owner-b, name) with an answer for Owner-B/Name = (%q, %v), want the token", token, err)
	}

	// The provider for the config entry OWNER-B/NAME with an answer for
	// OWNER-A/NAME.
	ghAppTokenCache.once.Do(func() { ghAppTokenCache.source = source })
	cfg := &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{
		Enabled: true,
		Repo:    "OWNER-B/NAME",
		App:     appCfg,
	}}}
	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	answerFor.Store("OWNER-A/NAME")
	before := atomic.LoadInt64(&requests)
	token, _, err = provider(ctx, "OWNER-B/NAME")
	if err == nil {
		t.Error("provider(OWNER-B/NAME) with an answer for OWNER-A/NAME = nil error, want a refusal")
	}
	if token != "" {
		t.Errorf("provider(OWNER-B/NAME) token = %q, want empty", token)
	}
	if n := atomic.LoadInt64(&requests) - before; n != 1 {
		t.Errorf("provider(OWNER-B/NAME) made %d requests, want 1 (the refusal is of the answer)", n)
	}
}

// TestAgentTokenFileProviderSetAtStartup (TASK-651): the daemon start installs
// the read token provider that the token file of each run asks, once, built
// from the config.
func TestAgentTokenFileProviderSetAtStartup(t *testing.T) {
	const line = "executor.SetAgentReadTokenProvider(agentReadTokenProvider(cfg))"
	if body := githubFuncBody(t, "main.go", "func runPollingMode("); !strings.Contains(body, line) {
		t.Errorf("runPollingMode does not call %s", line)
	}
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "executor.SetAgentReadTokenProvider("); n != 1 {
		t.Errorf("SetAgentReadTokenProvider( occurs %d times in main.go, want 1", n)
	}
}

// TestAgentTokenDirsAreSweptAtStart (TASK-651): each daemon start removes the
// read token directories a killed daemon left, where it removes left
// worktrees. A left directory of the daemon user goes; a link with the name
// pattern is not followed and its target stays; with the option empty
// nothing is removed.
func TestAgentTokenDirsAreSweptAtStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file owners")
	}
	for _, sig := range []string{"func runPollingMode(", "func newStartCmd("} {
		body := githubFuncBody(t, "main.go", sig)
		sweep := strings.Index(body, "\tsweepAgentTokenDirsAtStart()\n")
		cleanup := strings.Index(body, "executor.CleanupOrphanedWorktrees(")
		if sweep < 0 {
			t.Errorf("%s does not call sweepAgentTokenDirsAtStart()", sig)
		} else if cleanup < 0 || sweep > cleanup {
			t.Errorf("%s: sweepAgentTokenDirsAtStart() is not called before the left worktrees are removed", sig)
		}
		if n := strings.Count(body, "sweepAgentTokenDirsAtStart()"); n != 1 {
			t.Errorf("%s calls sweepAgentTokenDirsAtStart() %d times, want once", sig, n)
		}
	}
	// The gateway start sweeps once ahead of its three runner shapes, so the
	// chat-only runner and the webhook runner are covered, not only the
	// runner of the polling adapters.
	gw := githubFuncBody(t, "main.go", "func newStartCmd(")
	sweep := strings.Index(gw, "\tsweepAgentTokenDirsAtStart()\n")
	for _, start := range []string{
		"\t\t\tif needsPollingInfra {\n",
		"} else if gatewayChatNeedsOwnRunner(chatEnabled, needsPollingInfra) {",
		"p.SetAgentTokenRepoLookup(",
	} {
		if i := strings.Index(gw, start); i < 0 {
			t.Errorf("newStartCmd: %q not found", start)
		} else if sweep < 0 || sweep > i {
			t.Errorf("newStartCmd: sweepAgentTokenDirsAtStart() is not called before %q", start)
		}
	}

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Cleanup(func() { executor.PreflightRunAsUser(nil, "", true, 0) })
	setOption := func(set bool) {
		if set {
			executor.PreflightRunAsUser(&executor.ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}, "", true, 0)
		} else {
			executor.PreflightRunAsUser(nil, "", true, 0)
		}
	}
	makeLeft := func(name string) string {
		dir := filepath.Join(tmp, name)
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "github-token"), []byte("test-read-token"), 0o640); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	left := makeLeft("pilot-ghtoken-111")
	target := t.TempDir()
	targetFile := filepath.Join(target, "keep")
	if err := os.WriteFile(targetFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "pilot-ghtoken-222")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	setOption(true)
	if n := sweepAgentTokenDirsAtStart(); n != 1 {
		t.Errorf("sweep removed %d, want 1", n)
	}
	if _, err := os.Lstat(left); !os.IsNotExist(err) {
		t.Errorf("left token directory still there: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was removed or replaced: %v", err)
	}
	if raw, err := os.ReadFile(targetFile); err != nil || string(raw) != "keep" {
		t.Errorf("link target file = %q, %v, want keep", raw, err)
	}

	setOption(false)
	kept := makeLeft("pilot-ghtoken-333")
	if n := sweepAgentTokenDirsAtStart(); n != 0 {
		t.Errorf("option empty: sweep removed %d, want 0", n)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("option empty: the directory was removed: %v", err)
	}
}

// TestAgentTokenRepoLookupByProjectPath (TASK-651): the repository of the read
// token is that of the project config entry of the task path, so two entries
// with the same repository name under two owners each get their own.
func TestAgentTokenRepoLookupByProjectPath(t *testing.T) {
	const line = "runner.SetAgentTokenRepoLookup(newProjectAgentTokenRepoLookup(cfg))"
	if body := githubFuncBody(t, "main.go", "func runPollingMode("); !strings.Contains(body, line) {
		t.Errorf("runPollingMode does not call %s", line)
	}
	cfg := &config.Config{Projects: []*config.ProjectConfig{
		{Name: "a", Path: "/srv/a/name", GitHub: &config.ProjectGitHubConfig{Owner: "OWNER-A", Repo: "NAME"}},
		{Name: "b", Path: "/srv/b/name", GitHub: &config.ProjectGitHubConfig{Owner: "OWNER-B", Repo: "NAME"}},
		{Name: "c", Path: "/srv/c"},
		{Name: "d", Path: "/srv/d", GitHub: &config.ProjectGitHubConfig{Repo: "NAME"}},
	}}
	lookup := newProjectAgentTokenRepoLookup(cfg)
	for path, want := range map[string]string{
		"/srv/a/name": "OWNER-A/NAME",
		"/srv/b/name": "OWNER-B/NAME",
		"/srv/c":      "",
		"/srv/d":      "",
		"/srv/none":   "",
		"":            "",
	} {
		if got := lookup(path); got != want {
			t.Errorf("lookup(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestAgentTokenRepoLookupSetOnEachRunner (TASK-651): each place in cmd/pilot
// that builds a Runner for task runs sets the repository lookup of the read
// token next to the Navigator lookup, not only the polling runner: with no
// lookup a run_as_user run of that runner gets no token.
func TestAgentTokenRepoLookupSetOnEachRunner(t *testing.T) {
	const (
		nav   = ".SetNavigatorEnabledLookup(newProjectNavigatorEnabledLookup(cfg))"
		token = ".SetAgentTokenRepoLookup(newProjectAgentTokenRepoLookup(cfg))"
	)
	// The Runners of task runs: the polling runner, the two gateway runners and
	// the webhook runner in main.go, the one-shot task runner in commands.go.
	for file, want := range map[string]int{"main.go": 4, "commands.go": 1} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		navN, tokenN := strings.Count(src, nav), strings.Count(src, token)
		if navN != want {
			t.Errorf("%s: %d Navigator lookup calls, want %d (update this test with the new runner)", file, navN, want)
		}
		if tokenN != navN {
			t.Errorf("%s: %d token repository lookup calls for %d Navigator lookup calls", file, tokenN, navN)
		}
		lines := strings.Split(src, "\n")
		for i, line := range lines {
			recv, ok := strings.CutSuffix(strings.TrimSpace(line), nav)
			if !ok {
				continue
			}
			next := ""
			if i+1 < len(lines) {
				next = strings.TrimSpace(lines[i+1])
			}
			if !strings.HasPrefix(next, recv+token) {
				t.Errorf("%s:%d: %s%s is not followed by %s%s", file, i+1, recv, nav, recv, token)
			}
		}
	}
}
