package executor

import (
	"testing"
)

func TestExtractDirectoriesFromText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want map[string]bool
	}{
		{
			name: "go files",
			text: "Modify internal/executor/runner.go and internal/executor/monitor.go",
			want: map[string]bool{"internal/executor": true},
		},
		{
			name: "python files",
			text: "Update orchestrator/planner.py to add new logic",
			want: map[string]bool{"orchestrator": true},
		},
		{
			name: "typescript files",
			text: "Edit src/components/App.tsx and src/utils/helpers.ts",
			want: map[string]bool{"src/components": true, "src/utils": true},
		},
		{
			name: "multiple extensions",
			text: "Change internal/gateway/server.go, docs/api/spec.yaml, scripts/deploy.sh",
			want: map[string]bool{"internal/gateway": true, "docs/api": true, "scripts": true},
		},
		{
			name: "same directory collapses",
			text: "internal/alerts/engine.go internal/alerts/dispatcher.go internal/alerts/channels.go",
			want: map[string]bool{"internal/alerts": true},
		},
		{
			name: "no file paths returns empty",
			text: "Add rate limiting to API endpoints. Improve performance and caching.",
			want: map[string]bool{},
		},
		{
			name: "directory-only reference with trailing slash",
			text: "All changes are in internal/comms/ directory",
			want: map[string]bool{"internal/comms": true},
		},
		{
			name: "mixed file paths and directory references",
			text: "Update internal/executor/runner.go and also touch internal/config/",
			want: map[string]bool{"internal/executor": true, "internal/config": true},
		},
		{
			name: "rust and java files",
			text: "Modify src/lib/parser.rs and src/main/App.java",
			want: map[string]bool{"src/lib": true, "src/main": true},
		},
		{
			name: "css and html files",
			text: "Update styles/theme.css and templates/index.html",
			want: map[string]bool{"styles": true, "templates": true},
		},
		{
			name: "json and toml config files",
			text: "Edit configs/app.json and configs/settings.toml",
			want: map[string]bool{"configs": true},
		},
		{
			name: "root-level package.json folds into RootScopeKey",
			text: "Add a package.json with the base dependencies",
			want: map[string]bool{RootScopeKey: true},
		},
		{
			name: "root-level Makefile folds into RootScopeKey",
			text: "Add a Makefile with build and test targets",
			want: map[string]bool{RootScopeKey: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDirectoriesFromText(tt.text)
			if len(got) != len(tt.want) {
				t.Errorf("ExtractDirectoriesFromText() returned %d dirs, want %d\n  got:  %v\n  want: %v",
					len(got), len(tt.want), got, tt.want)
				return
			}
			for dir := range tt.want {
				if !got[dir] {
					t.Errorf("ExtractDirectoriesFromText() missing directory %q\n  got: %v", dir, got)
				}
			}
		})
	}
}

func TestIssuesOverlap(t *testing.T) {
	tests := []struct {
		name  string
		bodyA string
		bodyB string
		want  bool
	}{
		{
			name:  "overlapping same directory",
			bodyA: "Modify internal/executor/runner.go to add logging",
			bodyB: "Update internal/executor/monitor.go for metrics",
			want:  true,
		},
		{
			name:  "disjoint directories",
			bodyA: "Change internal/gateway/server.go",
			bodyB: "Update internal/alerts/engine.go",
			want:  false,
		},
		{
			name:  "one body has no paths",
			bodyA: "Add rate limiting to API endpoints",
			bodyB: "Update internal/executor/runner.go",
			want:  false,
		},
		{
			name:  "both bodies have no paths",
			bodyA: "Improve performance",
			bodyB: "Add caching layer",
			want:  false,
		},
		{
			name:  "overlap via directory-only reference",
			bodyA: "All work in internal/config/",
			bodyB: "Edit internal/config/loader.go",
			want:  true,
		},
		{
			name:  "partial path overlap is not a match",
			bodyA: "Edit internal/executor/runner.go",
			bodyB: "Edit internal/executor-v2/runner.go",
			want:  false,
		},
		{
			name:  "multiple dirs with one overlap",
			bodyA: "Change internal/gateway/server.go and internal/alerts/engine.go",
			bodyB: "Update internal/alerts/dispatcher.go and internal/config/loader.go",
			want:  true,
		},
		{
			// GH-3714: root-level files share no directory, so this only
			// overlaps because RootScopeKey folds them together.
			name:  "both bodies name root-level package.json",
			bodyA: "Add a package.json with the base dependencies",
			bodyB: "Update package.json to add the lint script",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IssuesOverlap(tt.bodyA, tt.bodyB)
			if got != tt.want {
				t.Errorf("IssuesOverlap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRootConfigFileMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want map[string]bool
	}{
		{
			name: "no root config files",
			text: "Add rate limiting to API endpoints",
			want: map[string]bool{},
		},
		{
			name: "single root config file",
			text: "Add a package.json with the base dependencies",
			want: map[string]bool{"package.json": true},
		},
		{
			name: "scaffold body names two distinct root config files",
			text: "Bootstrap the project: add package.json and tsconfig.json",
			want: map[string]bool{"package.json": true, "tsconfig.json": true},
		},
		{
			name: "repeated mention counts once",
			text: "package.json needs updating; also update package.json again",
			want: map[string]bool{"package.json": true},
		},
		{
			name: "go and rust root manifests",
			text: "Add go.mod, go.sum, Cargo.toml, and Cargo.lock",
			want: map[string]bool{"go.mod": true, "go.sum": true, "Cargo.toml": true, "Cargo.lock": true},
		},
		{
			name: "lockfiles and configs",
			text: "Add pnpm-lock.yaml, yarn.lock, vitest.config.ts, eslint.config.js, and a Makefile",
			want: map[string]bool{
				"pnpm-lock.yaml":   true,
				"yarn.lock":        true,
				"vitest.config.ts": true,
				"eslint.config.js": true,
				"Makefile":         true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RootConfigFileMentions(tt.text)
			if len(got) != len(tt.want) {
				t.Errorf("RootConfigFileMentions() returned %d mentions, want %d\n  got:  %v\n  want: %v",
					len(got), len(tt.want), got, tt.want)
				return
			}
			for f := range tt.want {
				if !got[f] {
					t.Errorf("RootConfigFileMentions() missing %q\n  got: %v", f, got)
				}
			}
		})
	}
}
