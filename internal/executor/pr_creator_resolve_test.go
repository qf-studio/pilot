package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingPRCreator records the last CreatePR call and returns url.
type recordingPRCreator struct {
	url   string
	calls int

	source, target, title, body string
}

func (c *recordingPRCreator) CreatePR(_ context.Context, source, target, title, body string) (string, error) {
	c.calls++
	c.source, c.target, c.title, c.body = source, target, title, body
	return c.url, nil
}

// TestResolvePRCreator covers the four-tier ladder (GH-5583).
func TestResolvePRCreator(t *testing.T) {
	projDir := t.TempDir()
	otherDir := t.TempDir()
	projCreator := &recordingPRCreator{}
	ghCreator := &recordingPRCreator{}
	sharedCreator := &recordingPRCreator{}

	tests := []struct {
		name         string
		register     map[string]PRCreator
		shared       PRCreator
		task         *Task
		wantKind     string
		wantCreator  PRCreator
		wantNilCreat bool
	}{
		{
			name:        "project registration wins over a stale shared slot",
			register:    map[string]PRCreator{ProjectPRCreatorKey(projDir): projCreator},
			shared:      sharedCreator,
			task:        &Task{ID: "APP-1", ProjectPath: projDir, SourceAdapter: "linear"},
			wantKind:    prCreatorKindProject,
			wantCreator: projCreator,
		},
		{
			name: "project registration wins over github-sdk registration",
			register: map[string]PRCreator{
				ProjectPRCreatorKey(projDir): projCreator,
				"github:o/r":                 ghCreator,
			},
			task:        &Task{ID: "GH-1", ProjectPath: projDir, SourceAdapter: "github", SourceRepo: "o/r"},
			wantKind:    prCreatorKindProject,
			wantCreator: projCreator,
		},
		{
			name:        "github task with no project registration is unchanged (github-sdk)",
			register:    map[string]PRCreator{ProjectPRCreatorKey(otherDir): projCreator, "github:o/r": ghCreator},
			shared:      sharedCreator,
			task:        &Task{ID: "GH-2", ProjectPath: projDir, SourceAdapter: "github", SourceRepo: "o/r"},
			wantKind:    prCreatorKindGitHubSDK,
			wantCreator: ghCreator,
		},
		{
			name:         "github task, no registrations, shared slot ignored (gh-cli)",
			shared:       sharedCreator,
			task:         &Task{ID: "GH-3", ProjectPath: projDir, SourceAdapter: "github", SourceRepo: "o/r"},
			wantKind:     prCreatorKindGHCLI,
			wantNilCreat: true,
		},
		{
			name:        "non-github adapter with no project registration uses shared slot",
			register:    map[string]PRCreator{ProjectPRCreatorKey(otherDir): projCreator},
			shared:      sharedCreator,
			task:        &Task{ID: "APP-2", ProjectPath: projDir, SourceAdapter: "linear"},
			wantKind:    prCreatorKindShared,
			wantCreator: sharedCreator,
		},
		{
			name:         "non-github adapter, nothing registered, no shared slot (gh-cli)",
			task:         &Task{ID: "APP-3", ProjectPath: projDir, SourceAdapter: "linear"},
			wantKind:     prCreatorKindGHCLI,
			wantNilCreat: true,
		},
		{
			name:         "no source adapter ignores the shared slot (gh-cli)",
			shared:       sharedCreator,
			task:         &Task{ID: "T-1", ProjectPath: projDir},
			wantKind:     prCreatorKindGHCLI,
			wantNilCreat: true,
		},
		{
			name:         "empty project path never matches a project key",
			register:     map[string]PRCreator{"project:": projCreator},
			task:         &Task{ID: "APP-4", SourceAdapter: "linear"},
			wantKind:     prCreatorKindGHCLI,
			wantNilCreat: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{}
			for k, c := range tt.register {
				r.RegisterPRCreator(k, c)
			}
			if tt.shared != nil {
				r.SetPRCreator(tt.shared)
			}
			got, kind := r.resolvePRCreator(tt.task)
			if kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", kind, tt.wantKind)
			}
			if tt.wantNilCreat {
				if got != nil {
					t.Fatalf("creator = %v, want nil", got)
				}
				return
			}
			if got != tt.wantCreator {
				t.Fatalf("creator = %v, want %v", got, tt.wantCreator)
			}
		})
	}
}

// TestProjectPRCreatorKey_ResolvesSymlinks: a symlinked project path and its
// target must produce the same key, so a registration made from the configured
// path matches a task carrying either form.
func TestProjectPRCreatorKey_ResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if a, b := ProjectPRCreatorKey(link), ProjectPRCreatorKey(real); a != b {
		t.Fatalf("symlink key %q != target key %q", a, b)
	}
	if got := ProjectPRCreatorKey(""); got != "" {
		t.Fatalf("empty path key = %q, want empty", got)
	}
	// A not-yet-existing path falls back to the cleaned form instead of failing.
	if got := ProjectPRCreatorKey("/nonexistent-gh5583/x/"); got != "project:/nonexistent-gh5583/x" {
		t.Fatalf("nonexistent path key = %q", got)
	}

	r := &Runner{}
	c := &recordingPRCreator{}
	r.RegisterPRCreator(ProjectPRCreatorKey(link), c)
	got, kind := r.resolvePRCreator(&Task{ID: "APP-1", ProjectPath: real, SourceAdapter: "linear"})
	if kind != prCreatorKindProject || got != c {
		t.Fatalf("symlinked registration not found via target path: kind=%q", kind)
	}
}

func TestMRSourceSuffix(t *testing.T) {
	tests := []struct {
		name string
		task *Task
		kind string
		want string
	}{
		{"shared slot keeps Closes for any adapter", &Task{ID: "APP-1", SourceAdapter: "linear", SourceIssueID: "uuid-1"}, prCreatorKindShared, "\n\nCloses #uuid-1"},
		{"shared slot, no issue id", &Task{ID: "APP-1", SourceAdapter: "gitlab"}, prCreatorKindShared, ""},
		{"project path, gitlab source keeps Closes", &Task{ID: "GL-7", SourceAdapter: "gitlab", SourceIssueID: "7"}, prCreatorKindProject, "\n\nCloses #7"},
		{"project path, linear source gets Source URL", &Task{ID: "APP-123", SourceAdapter: "linear", SourceIssueID: "uuid-1"}, prCreatorKindProject, "\n\nSource: https://linear.app/issue/APP-123"},
		{"project path, jira source falls back to adapter+id", &Task{ID: "JIRA-PROJ-42", SourceAdapter: "jira", SourceIssueID: "10001"}, prCreatorKindProject, "\n\nSource: jira JIRA-PROJ-42"},
		{"project path, github source gets issue URL", &Task{ID: "GH-9", SourceAdapter: "github", SourceRepo: "o/r", SourceIssueID: "9"}, prCreatorKindProject, "\n\nSource: https://github.com/o/r/issues/9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mrSourceSuffix(tt.task, tt.kind)
			if got != tt.want {
				t.Fatalf("mrSourceSuffix = %q, want %q", got, tt.want)
			}
			if tt.kind == prCreatorKindProject && tt.task.SourceAdapter != "gitlab" && strings.Contains(got, "Closes #") {
				t.Fatalf("non-gitlab project-keyed body must not carry a closing keyword: %q", got)
			}
		})
	}
}

// TestFinalizeDecomposedParentPR_ProjectKeyedCreatorWinsOverSharedSlot drives a
// real finalize path: a Linear-sourced task whose ProjectPath has a
// project-keyed registration opens the MR through that creator, never the stale
// shared slot, with a Source: line instead of Closes #.
func TestFinalizeDecomposedParentPR_ProjectKeyedCreatorWinsOverSharedSlot(t *testing.T) {
	setUpFakeGhPATH(t, []byte(`[]`), []byte(`[]`))
	repoDir := setupDecomposedParentRepoGH4031(t, "pilot/APP-55")

	r := newSilentRunnerTask359()
	stale := &recordingPRCreator{url: "https://gitlab.example.com/wrong/repo/-/merge_requests/1"}
	r.SetPRCreator(stale)
	project := &recordingPRCreator{url: "https://gitlab.example.com/example-group/service/-/merge_requests/42"}
	r.RegisterPRCreator(ProjectPRCreatorKey(repoDir), project)

	task := &Task{
		ID: "APP-55", Title: "add feature", Description: "d", Branch: "pilot/APP-55",
		BaseBranch: "main", CreatePR: true, ProjectPath: repoDir,
		SourceAdapter: "linear", SourceIssueID: "uuid-55",
	}
	result := &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "placeholder"}
	r.finalizeDecomposedParentPR(context.Background(), task, NewGitOperations(repoDir), result)

	if !result.Success {
		t.Fatalf("Success=false: %s", result.Error)
	}
	if stale.calls != 0 {
		t.Errorf("stale shared creator called %d times, want 0", stale.calls)
	}
	if project.calls != 1 || project.source != "pilot/APP-55" || project.target != "main" {
		t.Errorf("project creator calls=%d source=%q target=%q", project.calls, project.source, project.target)
	}
	if !strings.Contains(project.body, "Source: https://linear.app/issue/APP-55") || strings.Contains(project.body, "Closes #") {
		t.Errorf("body = %q, want Linear Source line and no Closes #", project.body)
	}
	if result.PRUrl != project.url {
		t.Errorf("PRUrl = %q, want %q", result.PRUrl, project.url)
	}
}

// TestFinalizeEpicBranchPR_RoutesThroughResolvePRCreator (GH-5589): the epic
// parent PR must use the resolved creator — never `gh pr create` — so an epic on
// a GitLab-hosted project opens an MR. The fake gh binary records any
// `pr create` invocation, so a leaked gh call is detected.
func TestFinalizeEpicBranchPR_RoutesThroughResolvePRCreator(t *testing.T) {
	tests := []struct {
		name          string
		adapter       string
		issueID       string
		wantInBody    string
		wantNotInBody string
	}{
		{"linear-sourced gets Source line, no Closes", "linear", "uuid-77", "Source: https://linear.app/issue/APP-77", "Closes #"},
		{"gitlab-sourced keeps Closes #<iid>", "gitlab", "12", "Closes #12", "Source:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitleFile := setUpFakeGhPRCreatePATH(t)
			branch := "pilot/APP-77"
			dir := initRepoWithRemoteAndFeatureBranch(t, branch)

			r := newSilentRunnerTask359()
			stale := &recordingPRCreator{url: "https://gitlab.example.com/wrong/repo/-/merge_requests/1"}
			r.SetPRCreator(stale)
			project := &recordingPRCreator{url: "https://gitlab.example.com/example-group/service/-/merge_requests/42"}
			r.RegisterPRCreator(ProjectPRCreatorKey(dir), project)

			task := &Task{
				ID: "APP-77", Title: "add feature", Description: "d", Branch: branch,
				BaseBranch: "main", CreatePR: true, ProjectPath: dir,
				SourceAdapter: tt.adapter, SourceIssueID: tt.issueID,
			}
			result := &ExecutionResult{TaskID: task.ID, Success: true, IsEpic: true}
			r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, nil)

			if !result.Success {
				t.Fatalf("Success=false: %s", result.Error)
			}
			if _, err := os.Stat(capturedTitleFile); err == nil {
				t.Error("gh pr create was invoked; epic must route through the project creator")
			}
			if stale.calls != 0 {
				t.Errorf("stale shared creator called %d times, want 0", stale.calls)
			}
			if project.calls != 1 || project.source != branch || project.target != "main" {
				t.Errorf("project creator calls=%d source=%q target=%q", project.calls, project.source, project.target)
			}
			if !strings.HasPrefix(project.title, "APP-77: ") {
				t.Errorf("title = %q, want APP-77: prefix", project.title)
			}
			if !strings.Contains(project.body, tt.wantInBody) || strings.Contains(project.body, tt.wantNotInBody) {
				t.Errorf("body = %q, want %q and no %q", project.body, tt.wantInBody, tt.wantNotInBody)
			}
			if result.PRUrl != project.url {
				t.Errorf("PRUrl = %q, want %q", result.PRUrl, project.url)
			}
		})
	}
}
