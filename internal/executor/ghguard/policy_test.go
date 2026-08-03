package ghguard

import "testing"

func TestClassify(t *testing.T) {
	ctx := TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"}

	tests := []struct {
		name string
		argv []string
		ctx  TaskContext
		want Decision
	}{
		// --- Allow unconditional (read-only) ---
		{"issue view", []string{"issue", "view", "4671"}, ctx, Allow},
		{"issue view sibling", []string{"issue", "view", "9001"}, ctx, Allow},
		{"issue list", []string{"issue", "list"}, ctx, Allow},
		{"pr view", []string{"pr", "view", "42"}, ctx, Allow},
		{"pr list", []string{"pr", "list"}, ctx, Allow},
		{"pr checks", []string{"pr", "checks", "42"}, ctx, Allow},
		{"pr diff", []string{"pr", "diff", "42"}, ctx, Allow},
		{"run view", []string{"run", "view", "12345"}, ctx, Allow},
		{"run list", []string{"run", "list"}, ctx, Allow},
		{"auth status", []string{"auth", "status"}, ctx, Allow},

		// --- Allow own-artifact ---
		{"pr create implicit head", []string{"pr", "create", "--title", "x", "--body", "y"}, ctx, Allow},
		{"pr create explicit own head", []string{"pr", "create", "--head", "pilot/GH-4671", "--title", "x"}, ctx, Allow},
		{"pr create explicit other head", []string{"pr", "create", "--head", "pilot/GH-9001", "--title", "x"}, ctx, Deny},
		{"issue comment own issue", []string{"issue", "comment", "4671", "--body", "status update"}, ctx, Allow},
		{"issue comment own issue hash", []string{"issue", "comment", "#4671", "--body", "status update"}, ctx, Allow},
		{"issue comment own issue url", []string{"issue", "comment", "https://github.com/qf-studio/pilot/issues/4671", "--body", "hi"}, ctx, Allow},
		{"issue comment sibling issue", []string{"issue", "comment", "4649", "--body", "closing this out"}, ctx, Deny},
		{"issue comment missing target", []string{"issue", "comment", "--body", "hi"}, ctx, Deny},
		{"pr comment implicit", []string{"pr", "comment", "--body", "status update"}, ctx, Allow},
		{"pr comment own branch", []string{"pr", "comment", "pilot/GH-4671", "--body", "status update"}, ctx, Allow},
		{"pr comment explicit number", []string{"pr", "comment", "4652", "--body", "hi"}, ctx, Deny},
		{"pr comment other branch", []string{"pr", "comment", "pilot/GH-9001", "--body", "hi"}, ctx, Deny},
		{"api get default method", []string{"api", "repos/qf-studio/pilot/issues/4671"}, ctx, Allow},
		{"api explicit get", []string{"api", "-X", "GET", "repos/qf-studio/pilot/issues"}, ctx, Allow},
		{"api post", []string{"api", "-X", "POST", "repos/qf-studio/pilot/issues/4649/comments"}, ctx, Deny},
		{"api patch long flag", []string{"api", "--method", "PATCH", "repos/qf-studio/pilot/issues/4649"}, ctx, Deny},
		{"api delete", []string{"api", "-X", "DELETE", "repos/qf-studio/pilot/issues/4649"}, ctx, Deny},

		// --- Deny always: lifecycle/metadata mutations ---
		{"issue close", []string{"issue", "close", "4649"}, ctx, Deny},
		{"issue close own", []string{"issue", "close", "4671"}, ctx, Deny},
		{"issue reopen", []string{"issue", "reopen", "4649"}, ctx, Deny},
		{"issue edit add-label", []string{"issue", "edit", "4649", "--add-label", "pilot-superseded"}, ctx, Deny},
		{"issue edit remove-label", []string{"issue", "edit", "4649", "--remove-label", "pilot"}, ctx, Deny},
		{"issue lock", []string{"issue", "lock", "4649"}, ctx, Deny},
		{"issue transfer", []string{"issue", "transfer", "4649", "other/repo"}, ctx, Deny},
		{"issue delete", []string{"issue", "delete", "4649"}, ctx, Deny},
		{"pr close", []string{"pr", "close", "42"}, ctx, Deny},
		{"pr reopen", []string{"pr", "reopen", "42"}, ctx, Deny},
		{"pr edit", []string{"pr", "edit", "42", "--add-label", "x"}, ctx, Deny},
		{"pr merge", []string{"pr", "merge", "42"}, ctx, Deny},
		{"pr review", []string{"pr", "review", "42", "--approve"}, ctx, Deny},

		// --- Deny always: cross-repo ---
		{"issue view other repo -R", []string{"issue", "view", "1", "-R", "other/repo"}, ctx, Deny},
		{"issue comment other repo --repo", []string{"issue", "comment", "4671", "--repo", "other/repo", "--body", "hi"}, ctx, Deny},
		{"pr create other repo", []string{"pr", "create", "--repo", "other/repo", "--title", "x"}, ctx, Deny},
		{"same repo via -R", []string{"issue", "view", "4671", "-R", "qf-studio/pilot"}, ctx, Allow},
		{"same repo via url", []string{"issue", "view", "4671", "--repo", "https://github.com/qf-studio/pilot"}, ctx, Allow},

		// --- Deny always: out-of-scope command groups ---
		{"release create", []string{"release", "create", "v1.0.0"}, ctx, Deny},
		{"repo edit", []string{"repo", "edit", "--description", "x"}, ctx, Deny},
		{"repo delete", []string{"repo", "delete", "qf-studio/pilot"}, ctx, Deny},
		{"secret set", []string{"secret", "set", "FOO"}, ctx, Deny},
		{"variable set", []string{"variable", "set", "FOO"}, ctx, Deny},
		{"workflow run", []string{"workflow", "run", "ci.yml"}, ctx, Deny},
		{"label create", []string{"label", "create", "x"}, ctx, Deny},
		{"gist create", []string{"gist", "create", "x"}, ctx, Deny},
		{"ssh-key add", []string{"ssh-key", "add", "id_rsa.pub"}, ctx, Deny},
		{"auth login", []string{"auth", "login"}, ctx, Deny},
		{"auth token", []string{"auth", "token"}, ctx, Deny},

		// --- Default deny: unrecognized subcommands ---
		{"unknown top-level", []string{"totally-unknown-subcommand"}, ctx, Deny},
		{"unknown issue subcommand", []string{"issue", "develop", "4671"}, ctx, Deny},
		{"empty argv", []string{}, ctx, Deny},

		// --- Incomplete task context: fail closed for mutations, open for reads ---
		{"read allowed with empty ctx", []string{"issue", "view", "1"}, TaskContext{}, Allow},
		{"pr create denied with empty ctx", []string{"pr", "create", "--title", "x"}, TaskContext{}, Deny},
		{"issue comment denied with empty ctx", []string{"issue", "comment", "1", "--body", "x"}, TaskContext{}, Deny},
		{"pr comment implicit denied with empty ctx", []string{"pr", "comment", "--body", "x"}, TaskContext{}, Deny},
		{"api get allowed with empty ctx", []string{"api", "repos/x/y/issues"}, TaskContext{}, Allow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.argv, tt.ctx)
			if got.Decision != tt.want {
				t.Errorf("Classify(%v) = %s (%q), want %s", tt.argv, got.Decision, got.Reason, tt.want)
			}
			if got.Reason == "" {
				t.Errorf("Classify(%v) returned empty Reason", tt.argv)
			}
		})
	}
}

func TestExtractFlagValue(t *testing.T) {
	tests := []struct {
		name      string
		argv      []string
		names     []string
		wantValue string
		wantOK    bool
	}{
		{"long space-separated", []string{"pr", "create", "--head", "my-branch"}, []string{"-H", "--head"}, "my-branch", true},
		{"long equals form", []string{"pr", "create", "--head=my-branch"}, []string{"-H", "--head"}, "my-branch", true},
		{"short space-separated", []string{"issue", "view", "1", "-R", "owner/repo"}, []string{"-R", "--repo"}, "owner/repo", true},
		{"short concatenated", []string{"issue", "view", "1", "-Rowner/repo"}, []string{"-R", "--repo"}, "owner/repo", true},
		{"not present", []string{"issue", "view", "1"}, []string{"-R", "--repo"}, "", false},
		{"flag with no value truncated", []string{"pr", "create", "--head"}, []string{"-H", "--head"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := extractFlagValue(tt.argv, tt.names)
			if ok != tt.wantOK || v != tt.wantValue {
				t.Errorf("extractFlagValue(%v, %v) = (%q, %v), want (%q, %v)", tt.argv, tt.names, v, ok, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestNormalizeIssueRef(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"4671", "4671", true},
		{"#4671", "4671", true},
		{"https://github.com/qf-studio/pilot/issues/4671", "4671", true},
		{"https://github.com/qf-studio/pilot/pull/42", "42", true},
		{"not-a-number", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeIssueRef(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("normalizeIssueRef(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestRepoMatches(t *testing.T) {
	tests := []struct {
		target   string
		taskRepo string
		want     bool
	}{
		{"qf-studio/pilot", "qf-studio/pilot", true},
		{"QF-Studio/Pilot", "qf-studio/pilot", true},
		{"https://github.com/qf-studio/pilot", "qf-studio/pilot", true},
		{"https://github.com/qf-studio/pilot.git", "qf-studio/pilot", true},
		{"other/repo", "qf-studio/pilot", false},
		{"qf-studio/pilot", "", false},
	}
	for _, tt := range tests {
		got := repoMatches(tt.target, tt.taskRepo)
		if got != tt.want {
			t.Errorf("repoMatches(%q, %q) = %v, want %v", tt.target, tt.taskRepo, got, tt.want)
		}
	}
}
