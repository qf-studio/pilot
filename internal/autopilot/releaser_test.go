package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// makeCommit creates a test commit with the given message
func makeCommit(msg string) *github.Commit {
	return &github.Commit{
		SHA: "abc123",
		Commit: struct {
			Message string `json:"message"`
			Author  struct {
				Name  string    `json:"name"`
				Email string    `json:"email"`
				Date  time.Time `json:"date"`
			} `json:"author"`
		}{
			Message: msg,
		},
	}
}

func TestParseSemVer(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    SemVer
		wantErr bool
	}{
		{
			name:  "v prefix",
			input: "v1.2.3",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "no prefix",
			input: "1.2.3",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "V prefix uppercase",
			input: "V1.2.3",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "with pre-release suffix",
			input: "v1.2.3-beta",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "with pre-release and build",
			input: "v1.2.3-beta.1+build.123",
			want:  SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:  "zero version",
			input: "v0.0.0",
			want:  SemVer{Major: 0, Minor: 0, Patch: 0},
		},
		{
			name:  "large numbers",
			input: "v10.20.30",
			want:  SemVer{Major: 10, Minor: 20, Patch: 30},
		},
		{
			name:    "invalid - too few parts",
			input:   "v1.2",
			wantErr: true,
		},
		{
			name:    "invalid - too many parts",
			input:   "v1.2.3.4",
			wantErr: true,
		},
		{
			name:    "invalid - non-numeric major",
			input:   "va.2.3",
			wantErr: true,
		},
		{
			name:    "invalid - non-numeric minor",
			input:   "v1.b.3",
			wantErr: true,
		},
		{
			name:    "invalid - non-numeric patch",
			input:   "v1.2.c",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSemVer(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSemVer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseSemVer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemVer_String(t *testing.T) {
	tests := []struct {
		name   string
		ver    SemVer
		prefix string
		want   string
	}{
		{
			name:   "with v prefix",
			ver:    SemVer{Major: 1, Minor: 2, Patch: 3},
			prefix: "v",
			want:   "v1.2.3",
		},
		{
			name:   "no prefix",
			ver:    SemVer{Major: 1, Minor: 2, Patch: 3},
			prefix: "",
			want:   "1.2.3",
		},
		{
			name:   "zero version",
			ver:    SemVer{Major: 0, Minor: 0, Patch: 0},
			prefix: "v",
			want:   "v0.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ver.String(tt.prefix)
			if got != tt.want {
				t.Errorf("SemVer.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemVer_Bump(t *testing.T) {
	tests := []struct {
		name     string
		ver      SemVer
		bumpType BumpType
		want     SemVer
	}{
		{
			name:     "bump major",
			ver:      SemVer{Major: 1, Minor: 2, Patch: 3},
			bumpType: BumpMajor,
			want:     SemVer{Major: 2, Minor: 0, Patch: 0},
		},
		{
			name:     "bump minor",
			ver:      SemVer{Major: 1, Minor: 2, Patch: 3},
			bumpType: BumpMinor,
			want:     SemVer{Major: 1, Minor: 3, Patch: 0},
		},
		{
			name:     "bump patch",
			ver:      SemVer{Major: 1, Minor: 2, Patch: 3},
			bumpType: BumpPatch,
			want:     SemVer{Major: 1, Minor: 2, Patch: 4},
		},
		{
			name:     "bump none - no change",
			ver:      SemVer{Major: 1, Minor: 2, Patch: 3},
			bumpType: BumpNone,
			want:     SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:     "bump major from zero",
			ver:      SemVer{Major: 0, Minor: 0, Patch: 0},
			bumpType: BumpMajor,
			want:     SemVer{Major: 1, Minor: 0, Patch: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ver.Bump(tt.bumpType)
			if got != tt.want {
				t.Errorf("SemVer.Bump() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectBumpType(t *testing.T) {
	tests := []struct {
		name     string
		messages []string
		want     BumpType
	}{
		{
			name:     "feat - minor bump",
			messages: []string{"feat: add new feature"},
			want:     BumpMinor,
		},
		{
			name:     "fix - patch bump",
			messages: []string{"fix: resolve bug"},
			want:     BumpPatch,
		},
		{
			name:     "breaking change marker - major bump",
			messages: []string{"feat!: breaking change"},
			want:     BumpMajor,
		},
		{
			name:     "feature with scope",
			messages: []string{"feat(api): add new endpoint"},
			want:     BumpMinor,
		},
		{
			name:     "chore - no bump",
			messages: []string{"chore: update dependencies"},
			want:     BumpNone,
		},
		{
			name:     "docs - no bump",
			messages: []string{"docs: update readme"},
			want:     BumpNone,
		},
		{
			name:     "multiple commits - highest wins",
			messages: []string{"fix: small fix", "feat: new feature", "chore: cleanup"},
			want:     BumpMinor,
		},
		{
			name:     "breaking with other commits",
			messages: []string{"feat: new feature", "fix!: breaking fix"},
			want:     BumpMajor,
		},
		{
			name:     "perf - patch bump",
			messages: []string{"perf: improve speed"},
			want:     BumpPatch,
		},
		{
			name:     "non-conventional commit - no bump",
			messages: []string{"Update something"},
			want:     BumpNone,
		},
		{
			name:     "empty commits",
			messages: []string{},
			want:     BumpNone,
		},
		{
			name:     "multiline commit message - first line only",
			messages: []string{"feat: add feature\n\nThis is a longer description"},
			want:     BumpMinor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commits := make([]*github.Commit, len(tt.messages))
			for i, msg := range tt.messages {
				commits[i] = makeCommit(msg)
			}
			got := DetectBumpType(commits)
			if got != tt.want {
				t.Errorf("DetectBumpType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerateChangelog(t *testing.T) {
	tests := []struct {
		name     string
		messages []string
		prNumber int
		contains []string
	}{
		{
			name:     "features and fixes",
			messages: []string{"feat: add new feature", "fix: resolve bug"},
			prNumber: 42,
			contains: []string{"## Features", "add new feature", "## Bug Fixes", "resolve bug"},
		},
		{
			name:     "no commits - fallback message",
			messages: []string{},
			prNumber: 123,
			contains: []string{"Release from PR #123"},
		},
		{
			name:     "non-conventional commits",
			messages: []string{"Update something"},
			prNumber: 42,
			contains: []string{"## Other Changes", "Update something"},
		},
		{
			name:     "chore goes to other",
			messages: []string{"chore: update deps"},
			prNumber: 42,
			contains: []string{"## Other Changes", "update deps"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commits := make([]*github.Commit, len(tt.messages))
			for i, msg := range tt.messages {
				commits[i] = makeCommit(msg)
			}
			got := GenerateChangelog(commits, tt.prNumber)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("GenerateChangelog() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestReleaser_ShouldRelease(t *testing.T) {
	tests := []struct {
		name     string
		config   *ReleaseConfig
		bumpType BumpType
		want     bool
	}{
		{
			name:     "enabled with on_merge and minor bump",
			config:   &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			bumpType: BumpMinor,
			want:     true,
		},
		{
			name:     "enabled with on_merge and patch bump",
			config:   &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			bumpType: BumpPatch,
			want:     true,
		},
		{
			name:     "enabled with on_merge and major bump",
			config:   &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			bumpType: BumpMajor,
			want:     true,
		},
		{
			name:     "enabled with on_merge but no bump",
			config:   &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			bumpType: BumpNone,
			want:     false,
		},
		{
			name:     "disabled",
			config:   &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			bumpType: BumpMinor,
			want:     false,
		},
		{
			name:     "wrong trigger",
			config:   &ReleaseConfig{Enabled: true, Trigger: "manual"},
			bumpType: BumpMinor,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Releaser{config: tt.config}
			got := r.ShouldRelease(tt.bumpType)
			if got != tt.want {
				t.Errorf("Releaser.ShouldRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReleaser_GetCurrentVersion(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    SemVer
		wantErr bool
	}{
		{
			name: "from latest release",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/releases/latest" {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"id":       1,
						"tag_name": "v1.2.3",
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			want: SemVer{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name: "fallback to tags",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/releases/latest" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message": "Not Found"}`))
					return
				}
				if r.URL.Path == "/repos/owner/repo/tags" {
					_ = json.NewEncoder(w).Encode([]map[string]interface{}{
						{"name": "v1.0.0"},
						{"name": "v2.0.0"},
						{"name": "v1.5.0"},
					})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			want: SemVer{Major: 2, Minor: 0, Patch: 0},
		},
		{
			name: "no releases or tags - zero version",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/releases/latest" {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"message": "Not Found"}`))
					return
				}
				if r.URL.Path == "/repos/owner/repo/tags" {
					_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			want: SemVer{Major: 0, Minor: 0, Patch: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := github.NewClientWithBaseURL("test-token", server.URL)
			r := NewReleaser(client, "owner", "repo", DefaultReleaseConfig())

			got, err := r.GetCurrentVersion(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCurrentVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("GetCurrentVersion() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReleaser_CreateTag(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/git/refs" && r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ref": capturedBody["ref"],
				"object": map[string]string{
					"sha": "abc123",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL("test-token", server.URL)
	config := &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	r := NewReleaser(client, "owner", "repo", config)

	prState := &PRState{PRNumber: 42, HeadSHA: "abc123"}
	newVersion := SemVer{Major: 1, Minor: 0, Patch: 0}

	tagName, err := r.CreateTag(context.Background(), prState, newVersion)
	if err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}

	if tagName != "v1.0.0" {
		t.Errorf("CreateTag() = %v, want v1.0.0", tagName)
	}

	if capturedBody["ref"] != "refs/tags/v1.0.0" {
		t.Errorf("CreateTag() ref = %v, want refs/tags/v1.0.0", capturedBody["ref"])
	}

	if capturedBody["sha"] != "abc123" {
		t.Errorf("CreateTag() sha = %v, want abc123", capturedBody["sha"])
	}
}

func TestNewReleaser(t *testing.T) {
	client := github.NewClient("test-token")
	config := DefaultReleaseConfig()

	r := NewReleaser(client, "owner", "repo", config)

	if r == nil {
		t.Fatal("NewReleaser() returned nil")
	}
	if r.owner != "owner" {
		t.Errorf("NewReleaser() owner = %v, want owner", r.owner)
	}
	if r.repo != "repo" {
		t.Errorf("NewReleaser() repo = %v, want repo", r.repo)
	}
	if r.config != config {
		t.Error("NewReleaser() config mismatch")
	}
}
