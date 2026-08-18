package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
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
			// GH-4953: GetCurrentVersion now always consults the tags list
			// too (not just the latest Release), so this mocks the realistic
			// GitHub behavior of an empty (not 404) tags array when the
			// release tag is also the highest tag.
			name: "from latest release",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/releases/latest":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"id":       1,
						"tag_name": "v1.2.3",
					})
				case "/repos/owner/repo/tags":
					_ = json.NewEncoder(w).Encode([]map[string]interface{}{
						{"name": "v1.2.3"},
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
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

// TestReleaser_GetCurrentVersionWithSource_TagBeatsStaleRelease is a
// table-driven regression test for GH-4953: a tag pushed without a covering
// GitHub Release must still count toward the baseline. Live incident: the
// sdk repo's latest Release was v0.34.1 but tag v0.35.0 already existed
// (pushed as a base-guard tag, no Release object); the releaser used to read
// only the Release and cut the next patch as v0.34.2 — a version Go module
// resolution ranks BELOW the v0.35.0 already shipped.
func TestReleaser_GetCurrentVersionWithSource_TagBeatsStaleRelease(t *testing.T) {
	tests := []struct {
		name          string
		latestRelease string // empty = 404 (no release)
		tags          []string
		wantVersion   SemVer
		wantSourceHas string // substring the source description must contain
		wantNextPatch SemVer // currentVersion.Bump(BumpPatch)
	}{
		{
			name:          "tag without covering release beats stale release (sdk PR#120 incident)",
			latestRelease: "v0.34.1",
			tags:          []string{"v0.34.1", "v0.35.0"},
			wantVersion:   SemVer{Major: 0, Minor: 35, Patch: 0},
			wantSourceHas: "max tag",
			wantNextPatch: SemVer{Major: 0, Minor: 35, Patch: 1},
		},
		{
			name:          "ledger and tags agree - release flow unchanged",
			latestRelease: "v1.2.3",
			tags:          []string{"v1.2.3", "v1.2.2", "v1.2.1"},
			wantVersion:   SemVer{Major: 1, Minor: 2, Patch: 3},
			wantSourceHas: "latest release",
			wantNextPatch: SemVer{Major: 1, Minor: 2, Patch: 4},
		},
		{
			name:          "tag-only repo, no release object at all",
			latestRelease: "",
			tags:          []string{"v2.0.0", "v1.0.0"},
			wantVersion:   SemVer{Major: 2, Minor: 0, Patch: 0},
			wantSourceHas: "max tag",
			wantNextPatch: SemVer{Major: 2, Minor: 0, Patch: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/releases/latest":
					if tt.latestRelease == "" {
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"message": "Not Found"}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"id":       1,
						"tag_name": tt.latestRelease,
					})
				case "/repos/owner/repo/tags":
					tags := make([]map[string]interface{}, len(tt.tags))
					for i, name := range tt.tags {
						tags[i] = map[string]interface{}{"name": name}
					}
					_ = json.NewEncoder(w).Encode(tags)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := github.NewClientWithBaseURL("test-token", server.URL)
			r := NewReleaser(client, "owner", "repo", DefaultReleaseConfig())

			got, source, err := r.GetCurrentVersionWithSource(context.Background(), "owner", "repo")
			if err != nil {
				t.Fatalf("GetCurrentVersionWithSource() error = %v", err)
			}
			if got != tt.wantVersion {
				t.Errorf("GetCurrentVersionWithSource() version = %v, want %v", got, tt.wantVersion)
			}
			if !strings.Contains(source, tt.wantSourceHas) {
				t.Errorf("GetCurrentVersionWithSource() source = %q, want to contain %q", source, tt.wantSourceHas)
			}

			nextPatch := got.Bump(BumpPatch)
			if nextPatch != tt.wantNextPatch {
				t.Errorf("Bump(BumpPatch) from baseline = %v, want %v", nextPatch, tt.wantNextPatch)
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

func TestReleaser_CreateTagForRepo(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/refs") {
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ref":"refs/tags/v2.0.0","object":{"sha":"def456"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL("test-token", server.URL)
	config := &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	r := NewReleaser(client, "default-owner", "default-repo", config)

	prState := &PRState{PRNumber: 422, HeadSHA: "def456"}
	newVersion := SemVer{Major: 2, Minor: 0, Patch: 0}

	// Call with a different owner/repo than the releaser default
	tagName, err := r.CreateTagForRepo(context.Background(), "qf-studio", "auth-service", prState, newVersion)
	if err != nil {
		t.Fatalf("CreateTagForRepo() error = %v", err)
	}

	if tagName != "v2.0.0" {
		t.Errorf("CreateTagForRepo() = %q, want %q", tagName, "v2.0.0")
	}

	// Verify the API call targeted the correct repo, not the default
	if capturedPath != "/repos/qf-studio/auth-service/git/refs" {
		t.Errorf("API path = %q, want %q", capturedPath, "/repos/qf-studio/auth-service/git/refs")
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
