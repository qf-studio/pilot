package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/teams"
)

// TestStartCommandFlags verifies all expected flags exist on the start command
func TestStartCommandFlags(t *testing.T) {
	cmd := newStartCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"daemon", "d"},
		{"dashboard", ""},
		{"project", "p"},
		{"replace", ""},
		{"no-gateway", ""},
		{"sequential", ""},
		{"telegram", ""},
		{"github", ""},
		{"linear", ""},
		{"slack", ""},
		{"team", ""},
		{"team-member", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestTaskCommandFlags verifies all expected flags exist on the task command
func TestTaskCommandFlags(t *testing.T) {
	cmd := newTaskCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"project", "p"},
		{"dry-run", ""},
		{"verbose", "v"},
		{"alerts", ""},
		{"team", ""},
		{"team-member", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestGitHubRunCommandFlags verifies all expected flags exist on the github run command
func TestGitHubRunCommandFlags(t *testing.T) {
	cmd := newGitHubRunCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"project", "p"},
		{"repo", ""},
		{"dry-run", ""},
		{"verbose", "v"},
		{"team", ""},
		{"team-member", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestFlagParsing verifies flags can be parsed correctly using ParseFlags
// (not Execute which also validates args)
func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name    string
		cmdFunc func() *cobra.Command
		args    []string
		wantErr bool
	}{
		{
			name:    "start with dashboard",
			cmdFunc: newStartCmd,
			args:    []string{"--dashboard"},
			wantErr: false,
		},
		{
			name:    "start with no-gateway and telegram",
			cmdFunc: newStartCmd,
			args:    []string{"--no-gateway", "--telegram=true"},
			wantErr: false,
		},
		{
			name:    "start with sequential",
			cmdFunc: newStartCmd,
			args:    []string{"--sequential"},
			wantErr: false,
		},
		{
			name:    "start with all adapter flags",
			cmdFunc: newStartCmd,
			args:    []string{"--telegram=true", "--github=true", "--linear=false", "--slack=true"},
			wantErr: false,
		},
		{
			name:    "task with dry-run",
			cmdFunc: newTaskCmd,
			args:    []string{"--dry-run"},
			wantErr: false,
		},
		{
			name:    "task with verbose",
			cmdFunc: newTaskCmd,
			args:    []string{"--verbose"},
			wantErr: false,
		},
		{
			name:    "task with all flags",
			cmdFunc: newTaskCmd,
			args:    []string{"--dry-run", "--verbose", "--alerts"},
			wantErr: false,
		},
		{
			name:    "github run with dry-run",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--dry-run"},
			wantErr: false,
		},
		{
			name:    "github run with repo",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--repo", "owner/repo"},
			wantErr: false,
		},
		{
			name:    "github run with all flags",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--dry-run", "--verbose", "--repo", "owner/repo"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmdFunc()
			err := cmd.ParseFlags(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestFlagDefaults verifies default values for important flags
func TestFlagDefaults(t *testing.T) {
	t.Run("start command defaults", func(t *testing.T) {
		cmd := newStartCmd()

		// Dashboard should default to false
		if flag := cmd.Flags().Lookup("dashboard"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dashboard default should be false, got %s", flag.DefValue)
			}
		}

		// sequential should default to false
		if flag := cmd.Flags().Lookup("sequential"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("sequential default should be false, got %s", flag.DefValue)
			}
		}
	})

	t.Run("task command defaults", func(t *testing.T) {
		cmd := newTaskCmd()

		// dry-run should default to false
		if flag := cmd.Flags().Lookup("dry-run"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dry-run default should be false, got %s", flag.DefValue)
			}
		}
	})

	t.Run("github run command defaults", func(t *testing.T) {
		cmd := newGitHubRunCmd()

		// dry-run should default to false
		if flag := cmd.Flags().Lookup("dry-run"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dry-run default should be false, got %s", flag.DefValue)
			}
		}
	})
}

// TestRemovedFlags verifies that removed flags are no longer present
func TestRemovedFlags(t *testing.T) {
	t.Run("start command removed flags", func(t *testing.T) {
		cmd := newStartCmd()

		removedFlags := []string{"no-pr", "direct-commit", "parallel"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})

	t.Run("task command removed flags", func(t *testing.T) {
		cmd := newTaskCmd()

		removedFlags := []string{"no-pr", "create-pr", "no-branch"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})

	t.Run("github run command removed flags", func(t *testing.T) {
		cmd := newGitHubRunCmd()

		removedFlags := []string{"no-pr", "create-pr"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})
}

func TestParseAutopilotBranch(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "valid metadata",
			body: "Some body text\n\n<!-- autopilot-meta branch:pilot/GH-123 -->\n",
			want: "pilot/GH-123",
		},
		{
			name: "metadata with context",
			body: "# Fix\n\n## Context\n- **PR**: #42\n- **Branch**: pilot/GH-99\n\n---\n\n<!-- autopilot-meta branch:pilot/GH-99 -->\n",
			want: "pilot/GH-99",
		},
		{
			name: "no metadata",
			body: "Some body text without metadata",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "malformed metadata - missing branch",
			body: "<!-- autopilot-meta -->",
			want: "",
		},
		{
			name: "malformed metadata - no closing",
			body: "<!-- autopilot-meta branch:pilot/GH-123",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAutopilotBranch(tt.body)
			if got != tt.want {
				t.Errorf("parseAutopilotBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseAutopilotPR(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "valid metadata with pr",
			body: "Some body text\n\n<!-- autopilot-meta branch:pilot/GH-10 pr:42 -->\n",
			want: 42,
		},
		{
			name: "metadata with context",
			body: "# Fix\n\n## Context\n- **PR**: #42\n\n---\n\n<!-- autopilot-meta branch:pilot/GH-99 pr:123 -->\n",
			want: 123,
		},
		{
			name: "missing pr field",
			body: "<!-- autopilot-meta branch:pilot/GH-10 -->",
			want: 0,
		},
		{
			name: "no metadata comment",
			body: "just a normal issue body",
			want: 0,
		},
		{
			name: "empty body",
			body: "",
			want: 0,
		},
		{
			name: "multiple metadata comments - first match wins",
			body: "<!-- autopilot-meta branch:pilot/GH-1 pr:100 -->\nSome text\n<!-- autopilot-meta branch:pilot/GH-2 pr:200 -->",
			want: 100,
		},
		{
			name: "malformed - no closing comment",
			body: "<!-- autopilot-meta branch:pilot/GH-10 pr:42",
			want: 0,
		},
		{
			name: "pr number only",
			body: "<!-- autopilot-meta pr:999 -->",
			want: 999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAutopilotPR(tt.body)
			if got != tt.want {
				t.Errorf("parseAutopilotPR() = %d, want %d", got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-635: wireProjectAccessChecker tests
// =============================================================================

func TestWireProjectAccessChecker_NilConfig(t *testing.T) {
	runner := executor.NewRunner()
	cleanup := wireProjectAccessChecker(runner, &config.Config{})
	if cleanup != nil {
		t.Error("expected nil cleanup when team config is nil")
	}
}

func TestWireProjectAccessChecker_Disabled(t *testing.T) {
	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     false,
			TeamID:      "team1",
			MemberEmail: "dev@test.com",
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when team config is disabled")
	}
}

func TestWireProjectAccessChecker_MissingTeamID(t *testing.T) {
	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "",
			MemberEmail: "dev@test.com",
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when team_id is missing")
	}
}

func TestWireProjectAccessChecker_MissingMemberEmail(t *testing.T) {
	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "team1",
			MemberEmail: "",
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when member_email is missing")
	}
}

func TestWireProjectAccessChecker_MissingMemoryPath(t *testing.T) {
	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "team1",
			MemberEmail: "dev@test.com",
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when memory path is missing")
	}
}

func TestWireProjectAccessChecker_TeamNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid DB with teams store
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	_, err = teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	_ = db.Close()

	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "nonexistent-team",
			MemberEmail: "dev@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when team is not found")
	}
}

func TestWireProjectAccessChecker_MemberNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	// Create DB with team but no matching member
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	store, err := teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := teams.NewService(store)
	team, _, err := svc.CreateTeam("Test Team", "owner@test.com")
	if err != nil {
		t.Fatalf("failed to create team: %v", err)
	}
	_ = db.Close()

	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      team.Name,
			MemberEmail: "nonexistent@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}
	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup != nil {
		t.Error("expected nil cleanup when member is not found")
	}
}

func TestWireProjectAccessChecker_FullWiring(t *testing.T) {
	tmpDir := t.TempDir()

	// Create DB with team, owner, and restricted developer
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	store, err := teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := teams.NewService(store)
	team, owner, err := svc.CreateTeam("Test Team", "owner@test.com")
	if err != nil {
		t.Fatalf("failed to create team: %v", err)
	}

	// Add developer restricted to /project/a
	_, err = svc.AddMember(team.ID, owner.ID, "dev@test.com", teams.RoleDeveloper, []string{"/project/a"})
	if err != nil {
		t.Fatalf("failed to add member: %v", err)
	}
	_ = db.Close()

	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "Test Team",
			MemberEmail: "dev@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}

	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup for valid config")
	}
	defer cleanup()

	// Verify the checker is wired by attempting to check access
	// The runner's projectAccessChecker should be set
	// We can't directly access the private field, but we know it was set
	// because wireProjectAccessChecker returned a non-nil cleanup.
}

func TestWireProjectAccessChecker_LookupByTeamID(t *testing.T) {
	tmpDir := t.TempDir()

	// Create DB with team
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	store, err := teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := teams.NewService(store)
	team, _, err := svc.CreateTeam("My Team", "owner@test.com")
	if err != nil {
		t.Fatalf("failed to create team: %v", err)
	}
	_ = db.Close()

	runner := executor.NewRunner()
	// Use team ID directly (not name)
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      team.ID,
			MemberEmail: "owner@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}

	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup when team found by ID")
	}
	defer cleanup()
}

func TestWireProjectAccessChecker_UnrestrictedMember(t *testing.T) {
	tmpDir := t.TempDir()

	// Create DB with unrestricted developer (no project restrictions)
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	store, err := teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := teams.NewService(store)
	team, owner, err := svc.CreateTeam("Open Team", "owner@test.com")
	if err != nil {
		t.Fatalf("failed to create team: %v", err)
	}
	// Developer with no project restrictions (empty list = all projects)
	_, err = svc.AddMember(team.ID, owner.ID, "dev@test.com", teams.RoleDeveloper, nil)
	if err != nil {
		t.Fatalf("failed to add member: %v", err)
	}
	_ = db.Close()

	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "Open Team",
			MemberEmail: "dev@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}

	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup for unrestricted member")
	}
	defer cleanup()
}

func TestWireProjectAccessChecker_ViewerDenied(t *testing.T) {
	tmpDir := t.TempDir()

	// Create DB with viewer (who lacks PermExecuteTasks)
	dbPath := filepath.Join(tmpDir, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	store, err := teams.NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := teams.NewService(store)
	team, owner, err := svc.CreateTeam("Team", "owner@test.com")
	if err != nil {
		t.Fatalf("failed to create team: %v", err)
	}
	_, err = svc.AddMember(team.ID, owner.ID, "viewer@test.com", teams.RoleViewer, nil)
	if err != nil {
		t.Fatalf("failed to add member: %v", err)
	}
	_ = db.Close()

	// Viewer should still get the checker wired — the checker itself will deny at runtime
	runner := executor.NewRunner()
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     true,
			TeamID:      "Team",
			MemberEmail: "viewer@test.com",
		},
		Memory: &config.MemoryConfig{
			Path: tmpDir,
		},
	}

	cleanup := wireProjectAccessChecker(runner, cfg)
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup — checker should be wired even for viewer")
	}
	defer cleanup()
}

// =============================================================================
// GH-635: applyTeamOverrides tests
// =============================================================================

func TestApplyTeamOverrides_NotChanged(t *testing.T) {
	cfg := &config.Config{}
	cmd := &cobra.Command{}
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("team-member", "", "")

	applyTeamOverrides(cfg, cmd, "my-team", "dev@test.com")

	if cfg.Team != nil {
		t.Error("expected Team to remain nil when --team flag not changed")
	}
}

func TestApplyTeamOverrides_TeamFlagSet(t *testing.T) {
	cfg := &config.Config{}
	cmd := &cobra.Command{}
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("team-member", "", "")
	_ = cmd.Flags().Set("team", "my-team")

	applyTeamOverrides(cfg, cmd, "my-team", "")

	if cfg.Team == nil {
		t.Fatal("expected Team to be created")
	}
	if !cfg.Team.Enabled {
		t.Error("expected Team.Enabled to be true")
	}
	if cfg.Team.TeamID != "my-team" {
		t.Errorf("expected TeamID 'my-team', got %q", cfg.Team.TeamID)
	}
}

func TestApplyTeamOverrides_BothFlagsSet(t *testing.T) {
	cfg := &config.Config{}
	cmd := &cobra.Command{}
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("team-member", "", "")
	_ = cmd.Flags().Set("team", "my-team")
	_ = cmd.Flags().Set("team-member", "dev@test.com")

	applyTeamOverrides(cfg, cmd, "my-team", "dev@test.com")

	if cfg.Team == nil {
		t.Fatal("expected Team to be created")
	}
	if cfg.Team.TeamID != "my-team" {
		t.Errorf("expected TeamID 'my-team', got %q", cfg.Team.TeamID)
	}
	if cfg.Team.MemberEmail != "dev@test.com" {
		t.Errorf("expected MemberEmail 'dev@test.com', got %q", cfg.Team.MemberEmail)
	}
}

func TestApplyTeamOverrides_OverridesExistingConfig(t *testing.T) {
	cfg := &config.Config{
		Team: &config.TeamConfig{
			Enabled:     false,
			TeamID:      "old-team",
			MemberEmail: "old@test.com",
		},
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("team", "", "")
	cmd.Flags().String("team-member", "", "")
	_ = cmd.Flags().Set("team", "new-team")

	applyTeamOverrides(cfg, cmd, "new-team", "")

	if !cfg.Team.Enabled {
		t.Error("expected Team.Enabled to be true after override")
	}
	if cfg.Team.TeamID != "new-team" {
		t.Errorf("expected TeamID 'new-team', got %q", cfg.Team.TeamID)
	}
	// MemberEmail should be preserved from existing config when --team-member not set
	if cfg.Team.MemberEmail != "old@test.com" {
		t.Errorf("expected MemberEmail preserved as 'old@test.com', got %q", cfg.Team.MemberEmail)
	}
}

// =============================================================================
// GH-711: applyInputOverrides tests
// =============================================================================

func TestApplyInputOverrides(t *testing.T) {
	tests := []struct {
		name          string
		setFlags      map[string]string // flags to mark as "changed"
		telegram      bool
		github        bool
		linear        bool
		slack         bool
		tunnel        bool
		checkTelegram *bool // expected Telegram.Enabled (nil = skip check)
		checkGitHub   *bool
		checkLinear   *bool
		checkSlack    *bool
		checkSocket   *bool // expected Slack.SocketMode
		checkTunnel   *bool
	}{
		{
			name:     "no flags changed — config untouched",
			setFlags: map[string]string{},
		},
		{
			name:        "slack flag enables slack and socket mode",
			setFlags:    map[string]string{"slack": "true"},
			slack:       true,
			checkSlack:  boolPtr(true),
			checkSocket: boolPtr(true),
		},
		{
			name:        "slack flag false disables slack and socket mode",
			setFlags:    map[string]string{"slack": "false"},
			slack:       false,
			checkSlack:  boolPtr(false),
			checkSocket: boolPtr(false),
		},
		{
			name:          "telegram flag enables telegram",
			setFlags:      map[string]string{"telegram": "true"},
			telegram:      true,
			checkTelegram: boolPtr(true),
		},
		{
			name:        "github flag enables github polling",
			setFlags:    map[string]string{"github": "true"},
			github:      true,
			checkGitHub: boolPtr(true),
		},
		{
			name:        "linear flag enables linear",
			setFlags:    map[string]string{"linear": "true"},
			linear:      true,
			checkLinear: boolPtr(true),
		},
		{
			name:        "tunnel flag enables tunnel",
			setFlags:    map[string]string{"tunnel": "true"},
			tunnel:      true,
			checkTunnel: boolPtr(true),
		},
		{
			name:     "multiple flags at once",
			setFlags: map[string]string{"slack": "true", "telegram": "true", "github": "true"},
			slack:    true, telegram: true, github: true,
			checkSlack:    boolPtr(true),
			checkSocket:   boolPtr(true),
			checkTelegram: boolPtr(true),
			checkGitHub:   boolPtr(true),
		},
		{
			name:        "slack on nil config creates default",
			setFlags:    map[string]string{"slack": "true"},
			slack:       true,
			checkSlack:  boolPtr(true),
			checkSocket: boolPtr(true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Adapters: &config.AdaptersConfig{}}
			cmd := &cobra.Command{}
			// Register all flags so Changed() works
			cmd.Flags().Bool("telegram", false, "")
			cmd.Flags().Bool("github", false, "")
			cmd.Flags().Bool("linear", false, "")
			cmd.Flags().Bool("slack", false, "")
			cmd.Flags().Bool("tunnel", false, "")

			for k, v := range tt.setFlags {
				_ = cmd.Flags().Set(k, v)
			}

			applyInputOverrides(cfg, cmd, tt.telegram, tt.github, tt.linear, tt.slack, tt.tunnel)

			if tt.checkTelegram != nil {
				if cfg.Adapters.Telegram == nil {
					t.Fatal("expected Telegram config to be created")
				}
				if cfg.Adapters.Telegram.Enabled != *tt.checkTelegram {
					t.Errorf("Telegram.Enabled = %v, want %v", cfg.Adapters.Telegram.Enabled, *tt.checkTelegram)
				}
			}
			if tt.checkGitHub != nil {
				if cfg.Adapters.GitHub == nil {
					t.Fatal("expected GitHub config to be created")
				}
				if cfg.Adapters.GitHub.Enabled != *tt.checkGitHub {
					t.Errorf("GitHub.Enabled = %v, want %v", cfg.Adapters.GitHub.Enabled, *tt.checkGitHub)
				}
			}
			if tt.checkLinear != nil {
				if cfg.Adapters.Linear == nil {
					t.Fatal("expected Linear config to be created")
				}
				if cfg.Adapters.Linear.Enabled != *tt.checkLinear {
					t.Errorf("Linear.Enabled = %v, want %v", cfg.Adapters.Linear.Enabled, *tt.checkLinear)
				}
			}
			if tt.checkSlack != nil {
				if cfg.Adapters.Slack == nil {
					t.Fatal("expected Slack config to be created")
				}
				if cfg.Adapters.Slack.Enabled != *tt.checkSlack {
					t.Errorf("Slack.Enabled = %v, want %v", cfg.Adapters.Slack.Enabled, *tt.checkSlack)
				}
			}
			if tt.checkSocket != nil {
				if cfg.Adapters.Slack == nil {
					t.Fatal("expected Slack config to be created for SocketMode check")
				}
				if cfg.Adapters.Slack.SocketMode != *tt.checkSocket {
					t.Errorf("Slack.SocketMode = %v, want %v", cfg.Adapters.Slack.SocketMode, *tt.checkSocket)
				}
			}
			if tt.checkTunnel != nil {
				if cfg.Tunnel == nil {
					t.Fatal("expected Tunnel config to be created")
				}
				if cfg.Tunnel.Enabled != *tt.checkTunnel {
					t.Errorf("Tunnel.Enabled = %v, want %v", cfg.Tunnel.Enabled, *tt.checkTunnel)
				}
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
