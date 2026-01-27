package teams

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "pilot-teams-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	_ = tmpFile.Close()

	db, err := sql.Open("sqlite3", tmpFile.Name())
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
