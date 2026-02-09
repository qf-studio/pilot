package teams

import (
	"testing"
)

func TestServiceAdapter_CheckPermission(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)
	adapter := NewServiceAdapter(service)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")
	dev, _ := service.AddMember(team.ID, owner.ID, "dev@example.com", RoleDeveloper, nil)
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name     string
		memberID string
		perm     string
		wantErr  bool
	}{
		{"owner can execute", owner.ID, "execute_tasks", false},
		{"developer can execute", dev.ID, "execute_tasks", false},
		{"viewer cannot execute", viewer.ID, "execute_tasks", true},
		{"owner can manage team", owner.ID, "manage_team", false},
		{"developer cannot manage team", dev.ID, "manage_team", true},
		{"viewer can view tasks", viewer.ID, "view_tasks", false},
		{"nonexistent member", "nonexistent", "view_tasks", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.CheckPermission(tt.memberID, tt.perm)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

func TestServiceAdapter_CheckProjectAccess(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)
	adapter := NewServiceAdapter(service)

	team, owner, _ := service.CreateTeam("Test Team", "owner@example.com")

	// Developer with restricted projects
	devRestricted, _ := service.AddMember(team.ID, owner.ID, "restricted@example.com", RoleDeveloper, []string{"/project/a", "/project/b"})

	// Developer with no restrictions (all projects)
	devUnrestricted, _ := service.AddMember(team.ID, owner.ID, "unrestricted@example.com", RoleDeveloper, nil)

	// Viewer (no execute permission)
	viewer, _ := service.AddMember(team.ID, owner.ID, "viewer@example.com", RoleViewer, nil)

	tests := []struct {
		name        string
		memberID    string
		projectPath string
		perm        string
		wantErr     bool
	}{
		{"restricted dev, allowed project", devRestricted.ID, "/project/a", "execute_tasks", false},
		{"restricted dev, allowed project b", devRestricted.ID, "/project/b", "execute_tasks", false},
		{"restricted dev, disallowed project", devRestricted.ID, "/project/c", "execute_tasks", true},
		{"unrestricted dev, any project", devUnrestricted.ID, "/project/anything", "execute_tasks", false},
		{"viewer, no execute", viewer.ID, "/project/a", "execute_tasks", true},
		{"viewer, can view", viewer.ID, "/project/a", "view_projects", false},
		{"owner, any project any perm", owner.ID, "/project/x", "manage_projects", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.CheckProjectAccess(tt.memberID, tt.projectPath, tt.perm)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

func TestNewServiceAdapter(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store, _ := NewStore(db)
	service := NewService(store)

	adapter := NewServiceAdapter(service)
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}
	if adapter.service != service {
		t.Error("adapter should reference the provided service")
	}
}
