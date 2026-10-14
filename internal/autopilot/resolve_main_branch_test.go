package autopilot

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestResolveMainBranchName_UsesProjectDefaultBranch covers GH-5695: the
// environment branch wins, then the project's branch_from/default_branch, and
// only then the literal "main". Controller and AutoMerger must agree.
func TestResolveMainBranchName_UsesProjectDefaultBranch(t *testing.T) {
	const emptyEnv = "-empty-"
	tests := []struct {
		name          string
		envBranch     string // "" = built-in default environment (Branch "main"); emptyEnv = env with no branch
		projectBranch string
		want          string
		wantLog       string
	}{
		{name: "project branch outranks built-in env default", projectBranch: "mainline", want: "mainline", wantLog: "source=project"},
		{name: "explicit env branch wins over project", envBranch: "develop", projectBranch: "mainline", want: "develop", wantLog: "source=environment"},
		{name: "built-in env default without project", want: "main", wantLog: "source=environment-default"},
		{name: "empty env branch uses project", envBranch: emptyEnv, projectBranch: "mainline", want: "mainline", wantLog: "source=project"},
		{name: "nothing configured falls back to main", envBranch: emptyEnv, want: "main", wantLog: "falling back"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			switch tt.envBranch {
			case "":
			case emptyEnv:
				cfg.activeEnvName = "test-env"
				cfg.activeEnvConfig = &EnvironmentConfig{}
			default:
				cfg.activeEnvName = "test-env"
				cfg.activeEnvConfig = &EnvironmentConfig{Branch: tt.envBranch}
			}

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			c := NewController(cfg, github.NewClient(testutil.FakeGitHubToken), approval.NewManager(nil), "owner", "repo",
				WithLogger(log), WithProjectBaseBranch(tt.projectBranch))

			if got := c.resolveMainBranchName(); got != tt.want {
				t.Errorf("Controller.resolveMainBranchName() = %q, want %q", got, tt.want)
			}
			if got := c.resolveMainBranchName(); got != tt.want {
				t.Errorf("second call = %q, want %q", got, tt.want)
			}
			if got := c.autoMerger.resolveMainBranchName(); got != tt.want {
				t.Errorf("AutoMerger.resolveMainBranchName() = %q, want %q", got, tt.want)
			}
			if n := strings.Count(buf.String(), tt.wantLog); n != 1 {
				t.Errorf("controller log contains %q %d times, want exactly once:\n%s", tt.wantLog, n, buf.String())
			}
		})
	}
}
