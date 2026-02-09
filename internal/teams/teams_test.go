package teams

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "pilot-teams-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	_ = tmpFile.Close()

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		t.Fatalf("failed to open database: %v", err)
	}

	cleanup := func() {
		_ = db.Close()
		_ = os.Remove(tmpFile.Name())
	}

	return db, cleanup
}

func TestRolePermissions(t *testing.T) {
	tests := []struct {
		role     Role
		perm     Permission
		expected bool
	}{
		// Owner has all permissions
		{RoleOwner, PermManageTeam, true},
		{RoleOwner, PermManageMembers, true},
		{RoleOwner, PermExecuteTasks, true},
		{RoleOwner, PermViewTasks, true},

		// Admin has most permissions but not manage_team
		{RoleAdmin, PermManageTeam, false},
		{RoleAdmin, PermManageMembers, true},
		{RoleAdmin, PermExecuteTasks, true},
		{RoleAdmin, PermViewAuditLog, true},

		// Developer can execute tasks
		{RoleDeveloper, PermManageTeam, false},
		{RoleDeveloper, PermManageMembers, false},
		{RoleDeveloper, PermExecuteTasks, true},
		{RoleDeveloper, PermCreateTasks, true},
		{RoleDeveloper, PermViewTasks, true},

		// Viewer is read-only
		{RoleViewer, PermManageTeam, false},
		{RoleViewer, PermExecuteTasks, false},
		{RoleViewer, PermViewTasks, true},
		{RoleViewer, PermViewProjects, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.perm), func(t *testing.T) {
			got := tt.role.HasPermission(tt.perm)
			if got != tt.expected {
				t.Errorf("Role(%s).HasPermission(%s) = %v, want %v", tt.role, tt.perm, got, tt.expected)
			}
		})
	}
}

func TestRoleCanManage(t *testing.T) {
	tests := []struct {
		role     Role
		other    Role
		expected bool
	}{
		{RoleOwner, RoleAdmin, true},
		{RoleOwner, RoleDeveloper, true},
		{RoleOwner, RoleViewer, true},
		{RoleOwner, RoleOwner, false}, // Can't manage equal role

		{RoleAdmin, RoleDeveloper, true},
		{RoleAdmin, RoleViewer, true},
		{RoleAdmin, RoleAdmin, false},
		{RoleAdmin, RoleOwner, false},

		{RoleDeveloper, RoleViewer, true},
		{RoleDeveloper, RoleDeveloper, false},
		{RoleDeveloper, RoleAdmin, false},

		{RoleViewer, RoleViewer, false},
		{RoleViewer, RoleDeveloper, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.other), func(t *testing.T) {
			got := tt.role.CanManage(tt.other)
			if got != tt.expected {
				t.Errorf("Role(%s).CanManage(%s) = %v, want %v", tt.role, tt.other, got, tt.expected)
			}
		})
	}
}

func TestRoleIsValid(t *testing.T) {
	tests := []struct {
		role     Role
		expected bool
	}{
		{RoleOwner, true},
		{RoleAdmin, true},
		{RoleDeveloper, true},
		{RoleViewer, true},
		{Role("invalid"), false},
		{Role(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			got := tt.role.IsValid()
			if got != tt.expected {
				t.Errorf("Role(%s).IsValid() = %v, want %v", tt.role, got, tt.expected)
			}
		})
	}
}

func TestStore_CreateAndGetTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")

	// Create team
	if err := store.CreateTeam(team); err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	// Get team
	got, err := store.GetTeam(team.ID)
	if err != nil {
		t.Fatalf("GetTeam failed: %v", err)
	}

	if got.Name != team.Name {
		t.Errorf("got name %q, want %q", got.Name, team.Name)
	}
}

func TestStore_AddAndListMembers(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Add another member
	member := NewMember(team.ID, "dev@example.com", RoleDeveloper, owner.ID)
	if err := store.AddMember(member); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	// List members
	members, err := store.ListMembers(team.ID)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("got %d members, want 2", len(members))
	}
}

func TestStore_GetMemberByEmail(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Get by email
	got, err := store.GetMemberByEmail(team.ID, "owner@example.com")
	if err != nil {
		t.Fatalf("GetMemberByEmail failed: %v", err)
	}

	if got == nil {
		t.Fatal("expected member, got nil")
	}

	if got.Role != RoleOwner {
		t.Errorf("got role %s, want owner", got.Role)
	}
}

func TestStore_UpdateMember(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	member := NewMember(team.ID, "dev@example.com", RoleDeveloper, owner.ID)
	_ = store.AddMember(member)

	// Update role
	member.Role = RoleAdmin
	if err := store.UpdateMember(member); err != nil {
		t.Fatalf("UpdateMember failed: %v", err)
	}

	// Verify
	got, _ := store.GetMember(member.ID)
	if got.Role != RoleAdmin {
		t.Errorf("got role %s, want admin", got.Role)
	}
}

func TestStore_AuditLog(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Add audit entry
	entry := &AuditEntry{
		ID:         "audit-1",
		TeamID:     team.ID,
		ActorID:    owner.ID,
		ActorEmail: owner.Email,
		Action:     AuditMemberAdded,
		Resource:   "member",
		ResourceID: "member-1",
		Details:    map[string]interface{}{"email": "new@example.com"},
	}
	entry.CreatedAt = owner.JoinedAt

	if err := store.AddAuditEntry(entry); err != nil {
		t.Fatalf("AddAuditEntry failed: %v", err)
	}

	// Get audit log
	entries, err := store.GetAuditLog(team.ID, 10)
	if err != nil {
		t.Fatalf("GetAuditLog failed: %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("got %d entries, want 1", len(entries))
	}

	if entries[0].Action != AuditMemberAdded {
		t.Errorf("got action %s, want member.added", entries[0].Action)
	}
}

func TestService_CreateTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, err := service.CreateTeam("Test Team", "owner@example.com")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	if team.Name != "Test Team" {
		t.Errorf("got name %q, want %q", team.Name, "Test Team")
	}

	if owner.Role != RoleOwner {
		t.Errorf("got role %s, want owner", owner.Role)
	}
}

func TestService_AddMember_PermissionDenied(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Add a viewer
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	// Viewer tries to add member - should fail
	_, err := service.AddMember(team.ID, viewer.ID, "new@example.com", RoleDeveloper, nil)
	if err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestService_RemoveMember_LastOwner(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Try to remove the only owner - should fail
	err := service.RemoveMember(team.ID, owner.ID, owner.ID)
	if err != ErrLastOwner {
		t.Errorf("expected ErrLastOwner, got %v", err)
	}
}

func TestService_UpdateMemberRole_SelfChange(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Try to change own role - should fail
	err := service.UpdateMemberRole(team.ID, owner.ID, owner.ID, RoleAdmin)
	if err != ErrSelfRoleChange {
		t.Errorf("expected ErrSelfRoleChange, got %v", err)
	}
}

func TestService_CheckProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Add developer with restricted projects
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, []string{"/project/a", "/project/b"})

	// Check allowed project
	err := service.CheckProjectAccess(dev.ID, "/project/a", PermExecuteTasks)
	if err != nil {
		t.Errorf("expected access to /project/a, got error: %v", err)
	}

	// Check disallowed project
	err = service.CheckProjectAccess(dev.ID, "/project/c", PermExecuteTasks)
	if err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied for /project/c, got %v", err)
	}
}

func TestService_GetTeamsForUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	// Create two teams
	team1, owner1, _ := service.CreateTeam("Team 1", "owner@example.com")
	team2, owner2, _ := service.CreateTeam("Team 2", "other@example.com")

	// Add user to both teams
	_, _ = service.AddMember(team1.ID, owner1.ID, "user@example.com", RoleDeveloper, nil)
	_, _ = service.AddMember(team2.ID, owner2.ID, "user@example.com", RoleViewer, nil)

	// Get teams for user
	memberships, err := service.GetTeamsForUser("user@example.com")
	if err != nil {
		t.Fatalf("GetTeamsForUser failed: %v", err)
	}

	if len(memberships) != 2 {
		t.Errorf("got %d memberships, want 2", len(memberships))
	}
}

// =============================================================================
// Role Tests - Extended Coverage
// =============================================================================

func TestRoleLevel(t *testing.T) {
	tests := []struct {
		name     string
		role     Role
		expected int
	}{
		{"owner level", RoleOwner, 4},
		{"admin level", RoleAdmin, 3},
		{"developer level", RoleDeveloper, 2},
		{"viewer level", RoleViewer, 1},
		{"invalid role level", Role("invalid"), 0},
		{"empty role level", Role(""), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.role.Level()
			if got != tt.expected {
				t.Errorf("Role(%q).Level() = %d, want %d", tt.role, got, tt.expected)
			}
		})
	}
}

func TestRolePermissions_AllRoles(t *testing.T) {
	tests := []struct {
		name             string
		role             Role
		expectedPermLen  int
		mustHavePerms    []Permission
		mustNotHavePerms []Permission
	}{
		{
			name:            "owner has all permissions",
			role:            RoleOwner,
			expectedPermLen: 10,
			mustHavePerms: []Permission{
				PermManageTeam, PermManageMembers, PermManageBilling,
				PermManageProjects, PermExecuteTasks, PermViewProjects,
				PermCreateTasks, PermCancelTasks, PermViewTasks, PermViewAuditLog,
			},
		},
		{
			name:            "admin permissions",
			role:            RoleAdmin,
			expectedPermLen: 8,
			mustHavePerms: []Permission{
				PermManageMembers, PermManageProjects, PermExecuteTasks,
				PermViewProjects, PermCreateTasks, PermCancelTasks,
				PermViewTasks, PermViewAuditLog,
			},
			mustNotHavePerms: []Permission{PermManageTeam, PermManageBilling},
		},
		{
			name:            "developer permissions",
			role:            RoleDeveloper,
			expectedPermLen: 5,
			mustHavePerms: []Permission{
				PermExecuteTasks, PermViewProjects, PermCreateTasks,
				PermCancelTasks, PermViewTasks,
			},
			mustNotHavePerms: []Permission{
				PermManageTeam, PermManageMembers, PermManageBilling,
				PermManageProjects, PermViewAuditLog,
			},
		},
		{
			name:            "viewer permissions",
			role:            RoleViewer,
			expectedPermLen: 2,
			mustHavePerms:   []Permission{PermViewProjects, PermViewTasks},
			mustNotHavePerms: []Permission{
				PermManageTeam, PermManageMembers, PermManageBilling,
				PermManageProjects, PermExecuteTasks, PermCreateTasks,
				PermCancelTasks, PermViewAuditLog,
			},
		},
		{
			name:            "invalid role has no permissions",
			role:            Role("invalid"),
			expectedPermLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perms := tt.role.Permissions()
			if len(perms) != tt.expectedPermLen {
				t.Errorf("Role(%q).Permissions() length = %d, want %d", tt.role, len(perms), tt.expectedPermLen)
			}

			for _, perm := range tt.mustHavePerms {
				if !tt.role.HasPermission(perm) {
					t.Errorf("Role(%q) should have permission %q", tt.role, perm)
				}
			}

			for _, perm := range tt.mustNotHavePerms {
				if tt.role.HasPermission(perm) {
					t.Errorf("Role(%q) should not have permission %q", tt.role, perm)
				}
			}
		})
	}
}

func TestHasPermissionInvalidRole(t *testing.T) {
	invalidRole := Role("nonexistent")
	if invalidRole.HasPermission(PermViewTasks) {
		t.Error("invalid role should not have any permissions")
	}
}

// =============================================================================
// Store Tests - Extended Coverage
// =============================================================================

func TestStore_GetTeamByName(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Unique Team Name", "owner@example.com")
	if err := store.CreateTeam(team); err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	tests := []struct {
		name       string
		teamName   string
		wantFound  bool
		wantTeamID string
	}{
		{"existing team", "Unique Team Name", true, team.ID},
		{"non-existing team", "Does Not Exist", false, ""},
		{"empty name", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetTeamByName(tt.teamName)
			if err != nil {
				t.Fatalf("GetTeamByName failed: %v", err)
			}
			if tt.wantFound && got == nil {
				t.Error("expected team to be found, got nil")
			}
			if !tt.wantFound && got != nil {
				t.Error("expected nil, got team")
			}
			if tt.wantFound && got != nil && got.ID != tt.wantTeamID {
				t.Errorf("got team ID %q, want %q", got.ID, tt.wantTeamID)
			}
		})
	}
}

func TestStore_GetTeamNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	got, err := store.GetTeam("nonexistent-id")
	if err != nil {
		t.Fatalf("GetTeam should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent team")
	}
}

func TestStore_UpdateTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Original Name", "owner@example.com")
	if err := store.CreateTeam(team); err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	// Update team
	team.Name = "Updated Name"
	team.Settings.MaxConcurrentTasks = 5
	team.Settings.DefaultBranch = "develop"
	team.Settings.AllowedProjects = []string{"/project/a"}

	if err := store.UpdateTeam(team); err != nil {
		t.Fatalf("UpdateTeam failed: %v", err)
	}

	// Verify
	got, err := store.GetTeam(team.ID)
	if err != nil {
		t.Fatalf("GetTeam failed: %v", err)
	}

	if got.Name != "Updated Name" {
		t.Errorf("got name %q, want %q", got.Name, "Updated Name")
	}
	if got.Settings.MaxConcurrentTasks != 5 {
		t.Errorf("got MaxConcurrentTasks %d, want 5", got.Settings.MaxConcurrentTasks)
	}
	if got.Settings.DefaultBranch != "develop" {
		t.Errorf("got DefaultBranch %q, want %q", got.Settings.DefaultBranch, "develop")
	}
	if len(got.Settings.AllowedProjects) != 1 || got.Settings.AllowedProjects[0] != "/project/a" {
		t.Errorf("got AllowedProjects %v, want [/project/a]", got.Settings.AllowedProjects)
	}
}

func TestStore_DeleteTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Team To Delete", "owner@example.com")
	if err := store.CreateTeam(team); err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}
	if err := store.AddMember(owner); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	// Delete team
	if err := store.DeleteTeam(team.ID); err != nil {
		t.Fatalf("DeleteTeam failed: %v", err)
	}

	// Verify team is gone
	got, err := store.GetTeam(team.ID)
	if err != nil {
		t.Fatalf("GetTeam should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after deletion")
	}
}

func TestStore_ListTeams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create multiple teams
	teamNames := []string{"Alpha Team", "Beta Team", "Gamma Team"}
	for _, name := range teamNames {
		team, _ := NewTeam(name, "owner@example.com")
		if err := store.CreateTeam(team); err != nil {
			t.Fatalf("CreateTeam failed for %s: %v", name, err)
		}
	}

	// List teams
	teams, err := store.ListTeams()
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	if len(teams) != 3 {
		t.Errorf("got %d teams, want 3", len(teams))
	}

	// Verify sorted by name
	if teams[0].Name != "Alpha Team" || teams[1].Name != "Beta Team" || teams[2].Name != "Gamma Team" {
		t.Error("teams not sorted by name")
	}
}

func TestStore_GetMember_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	got, err := store.GetMember("nonexistent-id")
	if err != nil {
		t.Fatalf("GetMember should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent member")
	}
}

func TestStore_GetMemberByEmail_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	got, err := store.GetMemberByEmail(team.ID, "nonexistent@example.com")
	if err != nil {
		t.Fatalf("GetMemberByEmail should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent email")
	}
}

func TestStore_GetMembersByEmail(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create two teams
	team1, _ := NewTeam("Team 1", "owner1@example.com")
	team2, _ := NewTeam("Team 2", "owner2@example.com")
	_ = store.CreateTeam(team1)
	_ = store.CreateTeam(team2)

	// Add same user to both teams
	member1 := NewMember(team1.ID, "shared@example.com", RoleDeveloper, "")
	member2 := NewMember(team2.ID, "shared@example.com", RoleViewer, "")
	_ = store.AddMember(member1)
	_ = store.AddMember(member2)

	// Get all memberships
	members, err := store.GetMembersByEmail("shared@example.com")
	if err != nil {
		t.Fatalf("GetMembersByEmail failed: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("got %d members, want 2", len(members))
	}
}

func TestStore_RemoveMember(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	member := NewMember(team.ID, "dev@example.com", RoleDeveloper, owner.ID)
	_ = store.AddMember(member)

	// Remove member
	if err := store.RemoveMember(member.ID); err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}

	// Verify member is gone
	got, err := store.GetMember(member.ID)
	if err != nil {
		t.Fatalf("GetMember should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestStore_CountMembersByRole(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Add members with various roles
	dev1 := NewMember(team.ID, "dev1@example.com", RoleDeveloper, owner.ID)
	dev2 := NewMember(team.ID, "dev2@example.com", RoleDeveloper, owner.ID)
	viewer := NewMember(team.ID, "viewer@example.com", RoleViewer, owner.ID)
	_ = store.AddMember(dev1)
	_ = store.AddMember(dev2)
	_ = store.AddMember(viewer)

	tests := []struct {
		role     Role
		expected int
	}{
		{RoleOwner, 1},
		{RoleDeveloper, 2},
		{RoleViewer, 1},
		{RoleAdmin, 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			count, err := store.CountMembersByRole(team.ID, tt.role)
			if err != nil {
				t.Fatalf("CountMembersByRole failed: %v", err)
			}
			if count != tt.expected {
				t.Errorf("got count %d, want %d", count, tt.expected)
			}
		})
	}
}

func TestStore_ProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	// Set project access
	access := &ProjectAccess{
		TeamID:      team.ID,
		ProjectPath: "/project/alpha",
		DefaultRole: RoleDeveloper,
	}
	if err := store.SetProjectAccess(access); err != nil {
		t.Fatalf("SetProjectAccess failed: %v", err)
	}

	// Get project access
	got, err := store.GetProjectAccess(team.ID, "/project/alpha")
	if err != nil {
		t.Fatalf("GetProjectAccess failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected project access, got nil")
	}
	if got.DefaultRole != RoleDeveloper {
		t.Errorf("got role %s, want developer", got.DefaultRole)
	}

	// Update project access (upsert)
	access.DefaultRole = RoleViewer
	if err := store.SetProjectAccess(access); err != nil {
		t.Fatalf("SetProjectAccess (upsert) failed: %v", err)
	}

	got, _ = store.GetProjectAccess(team.ID, "/project/alpha")
	if got.DefaultRole != RoleViewer {
		t.Errorf("after upsert: got role %s, want viewer", got.DefaultRole)
	}
}

func TestStore_GetProjectAccess_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	got, err := store.GetProjectAccess(team.ID, "/nonexistent/path")
	if err != nil {
		t.Fatalf("GetProjectAccess should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent project access")
	}
}

func TestStore_ListProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	// Add multiple project accesses
	projects := []string{"/project/alpha", "/project/beta", "/project/gamma"}
	for _, path := range projects {
		access := &ProjectAccess{
			TeamID:      team.ID,
			ProjectPath: path,
			DefaultRole: RoleDeveloper,
		}
		_ = store.SetProjectAccess(access)
	}

	// List project access
	accesses, err := store.ListProjectAccess(team.ID)
	if err != nil {
		t.Fatalf("ListProjectAccess failed: %v", err)
	}

	if len(accesses) != 3 {
		t.Errorf("got %d accesses, want 3", len(accesses))
	}

	// Verify sorted by project_path
	if accesses[0].ProjectPath != "/project/alpha" {
		t.Error("project accesses not sorted by path")
	}
}

func TestStore_RemoveProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	access := &ProjectAccess{
		TeamID:      team.ID,
		ProjectPath: "/project/to-remove",
		DefaultRole: RoleDeveloper,
	}
	_ = store.SetProjectAccess(access)

	// Remove project access
	if err := store.RemoveProjectAccess(team.ID, "/project/to-remove"); err != nil {
		t.Fatalf("RemoveProjectAccess failed: %v", err)
	}

	// Verify removal
	got, _ := store.GetProjectAccess(team.ID, "/project/to-remove")
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestStore_MemberWithProjects(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Add member with project restrictions
	member := NewMember(team.ID, "restricted@example.com", RoleDeveloper, owner.ID)
	member.Projects = []string{"/project/a", "/project/b", "/project/c"}

	if err := store.AddMember(member); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	// Retrieve and verify
	got, err := store.GetMember(member.ID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}

	if len(got.Projects) != 3 {
		t.Errorf("got %d projects, want 3", len(got.Projects))
	}

	// Update projects
	got.Projects = []string{"/project/x"}
	if err := store.UpdateMember(got); err != nil {
		t.Fatalf("UpdateMember failed: %v", err)
	}

	got, _ = store.GetMember(member.ID)
	if len(got.Projects) != 1 || got.Projects[0] != "/project/x" {
		t.Errorf("after update: got projects %v, want [/project/x]", got.Projects)
	}
}

// =============================================================================
// Service Tests - Extended Coverage
// =============================================================================

func TestService_GetTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, _, _ := service.CreateTeam("Test Team", "owner@example.com")

	tests := []struct {
		name      string
		teamID    string
		wantErr   error
		wantFound bool
	}{
		{"existing team", team.ID, nil, true},
		{"nonexistent team", "nonexistent-id", ErrTeamNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.GetTeam(tt.teamID)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("GetTeam() error = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("GetTeam() unexpected error: %v", err)
				}
				if tt.wantFound && got == nil {
					t.Error("expected team, got nil")
				}
			}
		})
	}
}

func TestService_GetTeamByName(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	_, _, _ = service.CreateTeam("Unique Name", "owner@example.com")

	got, err := service.GetTeamByName("Unique Name")
	if err != nil {
		t.Fatalf("GetTeamByName failed: %v", err)
	}
	if got == nil {
		t.Error("expected team, got nil")
	}

	notFound, _ := service.GetTeamByName("Does Not Exist")
	if notFound != nil {
		t.Error("expected nil for nonexistent name")
	}
}

func TestService_ListTeams(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	_, _, _ = service.CreateTeam("Team A", "owner@example.com")
	_, _, _ = service.CreateTeam("Team B", "owner@example.com")

	teams, err := service.ListTeams()
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}

	if len(teams) != 2 {
		t.Errorf("got %d teams, want 2", len(teams))
	}
}

func TestService_UpdateTeamSettings(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)

	tests := []struct {
		name     string
		actorID  string
		settings Settings
		wantErr  error
	}{
		{
			name:    "owner can update",
			actorID: owner.ID,
			settings: Settings{
				MaxConcurrentTasks: 10,
				DefaultBranch:      "develop",
			},
			wantErr: nil,
		},
		{
			name:     "developer cannot update",
			actorID:  dev.ID,
			settings: Settings{MaxConcurrentTasks: 5},
			wantErr:  ErrPermissionDenied,
		},
		{
			name:     "nonexistent actor",
			actorID:  "nonexistent",
			settings: Settings{},
			wantErr:  ErrMemberNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.UpdateTeamSettings(team.ID, tt.actorID, tt.settings)
			if err != tt.wantErr {
				t.Errorf("UpdateTeamSettings() error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	// Verify settings were updated
	got, _ := service.GetTeam(team.ID)
	if got.Settings.MaxConcurrentTasks != 10 {
		t.Errorf("settings not updated: got %d, want 10", got.Settings.MaxConcurrentTasks)
	}
}

func TestService_UpdateTeamSettings_TeamNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Delete the team, then try to update settings
	_ = store.DeleteTeam(team.ID)

	err := service.UpdateTeamSettings(team.ID, owner.ID, Settings{})
	if err != ErrTeamNotFound {
		t.Errorf("expected ErrTeamNotFound, got %v", err)
	}
}

func TestService_DeleteTeam(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)

	tests := []struct {
		name    string
		actorID string
		wantErr error
	}{
		{"admin cannot delete", admin.ID, ErrPermissionDenied},
		{"nonexistent actor", "nonexistent", ErrMemberNotFound},
		{"owner can delete", owner.ID, nil},
	}

	for _, tt := range tests {
		if tt.wantErr == nil {
			// Skip success case until last
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			err := service.DeleteTeam(team.ID, tt.actorID)
			if err != tt.wantErr {
				t.Errorf("DeleteTeam() error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	// Test successful deletion
	t.Run("owner can delete", func(t *testing.T) {
		err := service.DeleteTeam(team.ID, owner.ID)
		if err != nil {
			t.Errorf("DeleteTeam() unexpected error: %v", err)
		}
		// Verify deletion
		_, err = service.GetTeam(team.ID)
		if err != ErrTeamNotFound {
			t.Error("team should be deleted")
		}
	})
}

func TestService_DeleteTeam_TeamNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Delete team first via store
	_ = store.DeleteTeam(team.ID)

	// Now try via service
	err := service.DeleteTeam(team.ID, owner.ID)
	if err != ErrTeamNotFound {
		t.Errorf("expected ErrTeamNotFound, got %v", err)
	}
}

func TestService_AddMember(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)

	tests := []struct {
		name     string
		actorID  string
		email    string
		role     Role
		projects []string
		wantErr  error
	}{
		{
			name:    "owner adds developer",
			actorID: owner.ID,
			email:   "dev1@example.com",
			role:    RoleDeveloper,
			wantErr: nil,
		},
		{
			name:    "owner adds admin",
			actorID: owner.ID,
			email:   "admin2@example.com",
			role:    RoleAdmin,
			wantErr: nil,
		},
		{
			name:    "admin adds developer",
			actorID: admin.ID,
			email:   "dev2@example.com",
			role:    RoleDeveloper,
			wantErr: nil,
		},
		{
			name:    "admin cannot add admin",
			actorID: admin.ID,
			email:   "admin3@example.com",
			role:    RoleAdmin,
			wantErr: ErrPermissionDenied,
		},
		{
			name:    "add with invalid role",
			actorID: owner.ID,
			email:   "invalid@example.com",
			role:    Role("invalid"),
			wantErr: ErrInvalidRole,
		},
		{
			name:    "duplicate member",
			actorID: owner.ID,
			email:   "dev1@example.com",
			role:    RoleDeveloper,
			wantErr: ErrAlreadyMember,
		},
		{
			name:     "add with project restrictions",
			actorID:  owner.ID,
			email:    "restricted@example.com",
			role:     RoleDeveloper,
			projects: []string{"/project/a"},
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			member, err := service.AddMember(team.ID, tt.actorID, tt.email, tt.role, tt.projects)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("AddMember() error = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("AddMember() unexpected error: %v", err)
				}
				if member == nil {
					t.Error("expected member, got nil")
				}
				if member != nil && member.Role != tt.role {
					t.Errorf("got role %s, want %s", member.Role, tt.role)
				}
			}
		})
	}
}

func TestService_AddMember_ActorNotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, _, _ := service.CreateTeam("Test Team", "owner@example.com")

	_, err := service.AddMember(team.ID, "nonexistent", "new@example.com", RoleDeveloper, nil)
	if err != ErrMemberNotFound {
		t.Errorf("expected ErrMemberNotFound, got %v", err)
	}
}

func TestService_RemoveMember(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name     string
		actorID  string
		memberID string
		wantErr  error
	}{
		{"viewer cannot remove", viewer.ID, dev.ID, ErrPermissionDenied},
		{"admin removes viewer", admin.ID, viewer.ID, nil},
		{"admin cannot remove admin", admin.ID, admin.ID, ErrPermissionDenied}, // Can't remove self (equal role)
		{"owner removes developer", owner.ID, dev.ID, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.RemoveMember(team.ID, tt.actorID, tt.memberID)
			if err != tt.wantErr {
				t.Errorf("RemoveMember() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestService_RemoveMember_WithMultipleOwners(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner1, _ := service.CreateTeam("Test Team", "owner1@example.com")

	// Add second owner
	owner2, _ := service.AddMember(team.ID, owner1.ID, "owner2@example.com", RoleOwner, nil)

	// Now owner1 can remove owner2
	err := service.RemoveMember(team.ID, owner1.ID, owner2.ID)
	if err != nil {
		t.Errorf("RemoveMember() with multiple owners should succeed: %v", err)
	}
}

func TestService_UpdateMemberRole(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)

	// Test owner promotes dev to admin
	t.Run("owner promotes dev to admin", func(t *testing.T) {
		err := service.UpdateMemberRole(team.ID, owner.ID, dev.ID, RoleAdmin)
		if err != nil {
			t.Errorf("UpdateMemberRole() error = %v, want nil", err)
		}
	})

	// Test owner demotes admin to viewer
	t.Run("owner demotes admin to viewer", func(t *testing.T) {
		err := service.UpdateMemberRole(team.ID, owner.ID, admin.ID, RoleViewer)
		if err != nil {
			t.Errorf("UpdateMemberRole() error = %v, want nil", err)
		}
	})

	// Test self role change - need fresh admin for this
	t.Run("self role change", func(t *testing.T) {
		admin2, _ := service.AddMember(team.ID, owner.ID, "admin2@example.com", RoleAdmin, nil)
		err := service.UpdateMemberRole(team.ID, admin2.ID, admin2.ID, RoleDeveloper)
		if err != ErrSelfRoleChange {
			t.Errorf("UpdateMemberRole() error = %v, want %v", err, ErrSelfRoleChange)
		}
	})

	// Test invalid role
	t.Run("invalid role", func(t *testing.T) {
		dev2, _ := service.AddMember(team.ID, owner.ID, "dev2@example.com", RoleDeveloper, nil)
		err := service.UpdateMemberRole(team.ID, owner.ID, dev2.ID, Role("invalid"))
		if err != ErrInvalidRole {
			t.Errorf("UpdateMemberRole() error = %v, want %v", err, ErrInvalidRole)
		}
	})
}

func TestService_UpdateMemberRole_LastOwnerDemotion(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)

	// Admin tries to demote the only owner (even if they had permission, this should fail)
	// First, let's make admin an owner so they have permission
	_ = service.UpdateMemberRole(team.ID, owner.ID, admin.ID, RoleOwner)

	// Now there are 2 owners, admin can demote owner
	err := service.UpdateMemberRole(team.ID, admin.ID, owner.ID, RoleAdmin)
	if err != nil {
		t.Errorf("should allow demoting owner when another owner exists: %v", err)
	}

	// Now admin is the only owner, they can't demote themselves
	err = service.UpdateMemberRole(team.ID, admin.ID, admin.ID, RoleDeveloper)
	if err != ErrSelfRoleChange {
		t.Errorf("expected ErrSelfRoleChange, got %v", err)
	}
}

func TestService_UpdateMemberRole_AdminCannotPromoteToAdmin(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	admin, _ := service.AddMember(team.ID, owner.ID, "admin@example.com", RoleAdmin, nil)
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)

	// Admin cannot promote developer to admin (equal role)
	err := service.UpdateMemberRole(team.ID, admin.ID, dev.ID, RoleAdmin)
	if err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestService_UpdateMemberProjects(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name     string
		actorID  string
		memberID string
		projects []string
		wantErr  error
	}{
		{
			name:     "owner updates dev projects",
			actorID:  owner.ID,
			memberID: dev.ID,
			projects: []string{"/project/a", "/project/b"},
			wantErr:  nil,
		},
		{
			name:     "viewer cannot update",
			actorID:  viewer.ID,
			memberID: dev.ID,
			projects: []string{"/project/c"},
			wantErr:  ErrPermissionDenied,
		},
		{
			name:     "nonexistent member",
			actorID:  owner.ID,
			memberID: "nonexistent",
			projects: []string{},
			wantErr:  ErrMemberNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.UpdateMemberProjects(team.ID, tt.actorID, tt.memberID, tt.projects)
			if err != tt.wantErr {
				t.Errorf("UpdateMemberProjects() error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	// Verify projects were updated
	got, _ := service.GetMember(dev.ID)
	if len(got.Projects) != 2 || got.Projects[0] != "/project/a" {
		t.Errorf("projects not updated correctly: %v", got.Projects)
	}
}

func TestService_GetMember(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	got, err := service.GetMember(owner.ID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected member, got nil")
	}
	if got.Email != "owner@example.com" {
		t.Errorf("got email %q, want %q", got.Email, "owner@example.com")
	}

	// Nonexistent
	notFound, err := service.GetMember("nonexistent")
	if err != nil {
		t.Fatalf("GetMember should not error: %v", err)
	}
	if notFound != nil {
		t.Error("expected nil for nonexistent member")
	}
	_ = team // used for setup
}

func TestService_GetMemberByEmail(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, _, _ := service.CreateTeam("Test Team", "owner@example.com")

	got, err := service.GetMemberByEmail(team.ID, "owner@example.com")
	if err != nil {
		t.Fatalf("GetMemberByEmail failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected member, got nil")
	}

	// Nonexistent
	notFound, err := service.GetMemberByEmail(team.ID, "nonexistent@example.com")
	if err != nil {
		t.Fatalf("GetMemberByEmail should not error: %v", err)
	}
	if notFound != nil {
		t.Error("expected nil for nonexistent email")
	}
}

func TestService_ListMembers(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	_, _ = service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)

	members, err := service.ListMembers(team.ID)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}

	if len(members) != 2 {
		t.Errorf("got %d members, want 2", len(members))
	}
}

func TestService_CheckPermission(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name     string
		memberID string
		perm     Permission
		wantErr  error
	}{
		{"owner has manage team", owner.ID, PermManageTeam, nil},
		{"owner has view tasks", owner.ID, PermViewTasks, nil},
		{"viewer has view tasks", viewer.ID, PermViewTasks, nil},
		{"viewer lacks execute tasks", viewer.ID, PermExecuteTasks, ErrPermissionDenied},
		{"nonexistent member", "nonexistent", PermViewTasks, ErrMemberNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.CheckPermission(tt.memberID, tt.perm)
			if err != tt.wantErr {
				t.Errorf("CheckPermission() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestService_CheckProjectAccess_NoRestrictions(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil) // No project restrictions

	// Should have access to any project
	err := service.CheckProjectAccess(dev.ID, "/any/project", PermExecuteTasks)
	if err != nil {
		t.Errorf("expected access to any project, got error: %v", err)
	}
}

func TestService_CheckProjectAccess_InsufficientPermission(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	// Viewer lacks execute permission
	err := service.CheckProjectAccess(viewer.ID, "/project/a", PermExecuteTasks)
	if err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestService_SetProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name        string
		actorID     string
		projectPath string
		defaultRole Role
		wantErr     error
	}{
		{"owner sets project access", owner.ID, "/project/a", RoleDeveloper, nil},
		{"viewer cannot set access", viewer.ID, "/project/b", RoleDeveloper, ErrPermissionDenied},
		{"nonexistent actor", "nonexistent", "/project/c", RoleDeveloper, ErrMemberNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.SetProjectAccess(team.ID, tt.actorID, tt.projectPath, tt.defaultRole)
			if err != tt.wantErr {
				t.Errorf("SetProjectAccess() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestService_RemoveProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	// Setup: add project access
	_ = service.SetProjectAccess(team.ID, owner.ID, "/project/to-remove", RoleDeveloper)

	tests := []struct {
		name        string
		actorID     string
		projectPath string
		wantErr     error
	}{
		{"viewer cannot remove", viewer.ID, "/project/to-remove", ErrPermissionDenied},
		{"nonexistent actor", "nonexistent", "/project/to-remove", ErrMemberNotFound},
		{"owner removes access", owner.ID, "/project/to-remove", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.RemoveProjectAccess(team.ID, tt.actorID, tt.projectPath)
			if err != tt.wantErr {
				t.Errorf("RemoveProjectAccess() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestService_ListProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Add project accesses
	_ = service.SetProjectAccess(team.ID, owner.ID, "/project/a", RoleDeveloper)
	_ = service.SetProjectAccess(team.ID, owner.ID, "/project/b", RoleViewer)

	accesses, err := service.ListProjectAccess(team.ID)
	if err != nil {
		t.Fatalf("ListProjectAccess failed: %v", err)
	}

	if len(accesses) != 2 {
		t.Errorf("got %d accesses, want 2", len(accesses))
	}
}

func TestService_GetAuditLog(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name    string
		actorID string
		limit   int
		wantErr error
	}{
		{"owner can view audit log", owner.ID, 10, nil},
		{"viewer cannot view audit log", viewer.ID, 10, ErrPermissionDenied},
		{"nonexistent actor", "nonexistent", 10, ErrMemberNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := service.GetAuditLog(team.ID, tt.actorID, tt.limit)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("GetAuditLog() error = %v, want %v", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("GetAuditLog() unexpected error: %v", err)
				}
				if entries == nil {
					t.Error("expected entries, got nil")
				}
			}
		})
	}
}

func TestService_LogTaskEvent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Log a task event
	err := service.LogTaskEvent(team.ID, owner.ID, owner.Email, "task-123", AuditTaskCreated, map[string]interface{}{
		"title": "Test Task",
	})
	if err != nil {
		t.Fatalf("LogTaskEvent failed: %v", err)
	}

	// Verify audit log
	entries, _ := service.GetAuditLog(team.ID, owner.ID, 10)
	found := false
	for _, e := range entries {
		if e.Action == AuditTaskCreated && e.ResourceID == "task-123" {
			found = true
			break
		}
	}
	if !found {
		t.Error("task event not found in audit log")
	}
}

// =============================================================================
// Factory Function Tests
// =============================================================================

func TestNewTeam(t *testing.T) {
	team, owner := NewTeam("Test Team", "owner@test.com")

	if team.Name != "Test Team" {
		t.Errorf("got name %q, want %q", team.Name, "Test Team")
	}
	if team.ID == "" {
		t.Error("team ID should not be empty")
	}
	if team.Settings.MaxConcurrentTasks != 2 {
		t.Errorf("default MaxConcurrentTasks should be 2, got %d", team.Settings.MaxConcurrentTasks)
	}
	if team.Settings.DefaultBranch != "main" {
		t.Errorf("default DefaultBranch should be 'main', got %q", team.Settings.DefaultBranch)
	}

	if owner.Role != RoleOwner {
		t.Errorf("owner role should be 'owner', got %s", owner.Role)
	}
	if owner.Email != "owner@test.com" {
		t.Errorf("owner email should be 'owner@test.com', got %q", owner.Email)
	}
	if owner.TeamID != team.ID {
		t.Error("owner should belong to the team")
	}
}

func TestNewMember(t *testing.T) {
	member := NewMember("team-123", "dev@test.com", RoleDeveloper, "inviter-id")

	if member.TeamID != "team-123" {
		t.Errorf("got team ID %q, want %q", member.TeamID, "team-123")
	}
	if member.Email != "dev@test.com" {
		t.Errorf("got email %q, want %q", member.Email, "dev@test.com")
	}
	if member.Role != RoleDeveloper {
		t.Errorf("got role %s, want developer", member.Role)
	}
	if member.InvitedBy != "inviter-id" {
		t.Errorf("got invited by %q, want %q", member.InvitedBy, "inviter-id")
	}
	if member.ID == "" {
		t.Error("member ID should not be empty")
	}
}

// =============================================================================
// Edge Cases and Error Scenarios
// =============================================================================

func TestService_GetTeamsForUser_NoMemberships(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	// User with no memberships
	memberships, err := service.GetTeamsForUser("nobody@example.com")
	if err != nil {
		t.Fatalf("GetTeamsForUser failed: %v", err)
	}
	if len(memberships) != 0 {
		t.Errorf("expected 0 memberships, got %d", len(memberships))
	}
}

func TestService_GetTeamsForUser_TeamDeleted(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	_, _ = service.AddMember(team.ID, owner.ID, "user@example.com", RoleDeveloper, nil)

	// Verify membership exists before deletion
	memberships, err := service.GetTeamsForUser("user@example.com")
	if err != nil {
		t.Fatalf("GetTeamsForUser failed: %v", err)
	}
	if len(memberships) != 1 {
		t.Errorf("before deletion: expected 1 membership, got %d", len(memberships))
	}

	// Delete team directly via store (cascade will remove members)
	_ = store.DeleteTeam(team.ID)

	// After deletion, user should have no memberships (cascade delete)
	memberships, err = service.GetTeamsForUser("user@example.com")
	if err != nil {
		t.Fatalf("GetTeamsForUser failed: %v", err)
	}
	if len(memberships) != 0 {
		t.Errorf("after deletion: expected 0 memberships, got %d", len(memberships))
	}
}

func TestStore_ListTeams_Empty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	teams, err := store.ListTeams()
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("expected 0 teams, got %d", len(teams))
	}
}

func TestStore_ListMembers_Empty(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Empty Team", "owner@example.com")
	_ = store.CreateTeam(team)
	// Don't add any members

	members, err := store.ListMembers(team.ID)
	if err != nil {
		t.Fatalf("ListMembers failed: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("expected 0 members, got %d", len(members))
	}
}

func TestStore_AuditLog_WithNilDetails(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, owner := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)
	_ = store.AddMember(owner)

	// Add entry with nil details
	entry := &AuditEntry{
		ID:         "audit-nil",
		TeamID:     team.ID,
		ActorID:    owner.ID,
		ActorEmail: owner.Email,
		Action:     AuditProjectRemoved,
		Resource:   "project",
		ResourceID: "",
		Details:    nil,
		CreatedAt:  owner.JoinedAt,
	}

	if err := store.AddAuditEntry(entry); err != nil {
		t.Fatalf("AddAuditEntry with nil details failed: %v", err)
	}

	entries, err := store.GetAuditLog(team.ID, 10)
	if err != nil {
		t.Fatalf("GetAuditLog failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestStore_MemberWithName(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	member := NewMember(team.ID, "named@example.com", RoleDeveloper, "")
	member.Name = "John Doe"

	if err := store.AddMember(member); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	got, err := store.GetMember(member.ID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}
	if got.Name != "John Doe" {
		t.Errorf("got name %q, want %q", got.Name, "John Doe")
	}

	// Update name
	got.Name = "Jane Doe"
	if err := store.UpdateMember(got); err != nil {
		t.Fatalf("UpdateMember failed: %v", err)
	}

	got, _ = store.GetMember(member.ID)
	if got.Name != "Jane Doe" {
		t.Errorf("after update: got name %q, want %q", got.Name, "Jane Doe")
	}
}

// GH-634: Tests for GitHub username identity mapping

func TestStore_MemberWithGitHubUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	member := NewMember(team.ID, "dev@example.com", RoleDeveloper, "")
	member.GitHubUser = "octocat"

	if err := store.AddMember(member); err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}

	// Verify persisted via GetMember
	got, err := store.GetMember(member.ID)
	if err != nil {
		t.Fatalf("GetMember failed: %v", err)
	}
	if got.GitHubUser != "octocat" {
		t.Errorf("got GitHubUser %q, want %q", got.GitHubUser, "octocat")
	}

	// Update GitHub username
	got.GitHubUser = "octocat-v2"
	if err := store.UpdateMember(got); err != nil {
		t.Fatalf("UpdateMember failed: %v", err)
	}

	got, _ = store.GetMember(member.ID)
	if got.GitHubUser != "octocat-v2" {
		t.Errorf("after update: got GitHubUser %q, want %q", got.GitHubUser, "octocat-v2")
	}
}

func TestStore_GetMemberByGitHubUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	team, _ := NewTeam("Test Team", "owner@example.com")
	_ = store.CreateTeam(team)

	member := NewMember(team.ID, "dev@example.com", RoleDeveloper, "")
	member.GitHubUser = "octocat"
	_ = store.AddMember(member)

	// Found
	got, err := store.GetMemberByGitHubUser(team.ID, "octocat")
	if err != nil {
		t.Fatalf("GetMemberByGitHubUser failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected member, got nil")
	}
	if got.ID != member.ID {
		t.Errorf("got member ID %q, want %q", got.ID, member.ID)
	}

	// Not found — wrong team
	got, err = store.GetMemberByGitHubUser("wrong-team", "octocat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got member %q", got.ID)
	}

	// Not found — wrong username
	got, err = store.GetMemberByGitHubUser(team.ID, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got member %q", got.ID)
	}
}

func TestStore_GetMembersByGitHubUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Two teams, same GitHub user
	team1, _ := NewTeam("Team A", "owner1@example.com")
	team2, _ := NewTeam("Team B", "owner2@example.com")
	_ = store.CreateTeam(team1)
	_ = store.CreateTeam(team2)

	m1 := NewMember(team1.ID, "dev@example.com", RoleDeveloper, "")
	m1.GitHubUser = "octocat"
	_ = store.AddMember(m1)

	m2 := NewMember(team2.ID, "dev@example.com", RoleDeveloper, "")
	m2.GitHubUser = "octocat"
	_ = store.AddMember(m2)

	members, err := store.GetMembersByGitHubUser("octocat")
	if err != nil {
		t.Fatalf("GetMembersByGitHubUser failed: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("got %d members, want 2", len(members))
	}

	// No matches
	members, err = store.GetMembersByGitHubUser("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("got %d members, want 0", len(members))
	}
}

func TestService_ResolveGitHubIdentity(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := NewService(store)

	team, owner, err := svc.CreateTeam("Test Team", "owner@example.com")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	// Add a member with GitHub username
	dev, err := svc.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
	dev.GitHubUser = "octocat"
	if err := store.UpdateMember(dev); err != nil {
		t.Fatalf("UpdateMember failed: %v", err)
	}

	tests := []struct {
		name     string
		ghUser   string
		email    string
		wantID   string
		wantErr  bool
	}{
		{
			name:   "resolve by GitHub username",
			ghUser: "octocat",
			email:  "",
			wantID: dev.ID,
		},
		{
			name:   "resolve by email fallback",
			ghUser: "unknown-user",
			email:  "dev@example.com",
			wantID: dev.ID,
		},
		{
			name:   "GitHub username takes priority over email",
			ghUser: "octocat",
			email:  "wrong@example.com",
			wantID: dev.ID,
		},
		{
			name:   "no match returns empty",
			ghUser: "unknown-user",
			email:  "unknown@example.com",
			wantID: "",
		},
		{
			name:   "empty inputs return empty",
			ghUser: "",
			email:  "",
			wantID: "",
		},
		{
			name:   "resolve owner by email",
			ghUser: "",
			email:  "owner@example.com",
			wantID: owner.ID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.ResolveGitHubIdentity(tt.ghUser, tt.email)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.wantID {
				t.Errorf("got memberID %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestService_GetMemberByGitHubUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	svc := NewService(store)

	team, owner, err := svc.CreateTeam("Test Team", "owner@example.com")
	if err != nil {
		t.Fatalf("CreateTeam failed: %v", err)
	}

	dev, err := svc.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
	dev.GitHubUser = "octocat"
	_ = store.UpdateMember(dev)

	got, err := svc.GetMemberByGitHubUser(team.ID, "octocat")
	if err != nil {
		t.Fatalf("GetMemberByGitHubUser failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected member, got nil")
	}
	if got.ID != dev.ID {
		t.Errorf("got member %q, want %q", got.ID, dev.ID)
	}

	// Not found
	got, err = svc.GetMemberByGitHubUser(team.ID, "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unknown user, got %q", got.ID)
	}
}
