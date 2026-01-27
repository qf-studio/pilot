package teams

import (
	"time"

	"github.com/google/uuid"
)

// Team represents a local team in Pilot
type Team struct {
	ID        string    `json:"id" yaml:"id"`
	Name      string    `json:"name" yaml:"name"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
	Settings  Settings  `json:"settings" yaml:"settings"`
}

// Settings holds team-specific settings
type Settings struct {
	MaxConcurrentTasks int      `json:"max_concurrent_tasks" yaml:"max_concurrent_tasks"`
	DefaultBranch      string   `json:"default_branch" yaml:"default_branch"`
	AllowedProjects    []string `json:"allowed_projects,omitempty" yaml:"allowed_projects,omitempty"`
}

// Member represents a team member
type Member struct {
	ID        string    `json:"id" yaml:"id"`
	TeamID    string    `json:"team_id" yaml:"team_id"`
	Email     string    `json:"email" yaml:"email"`
	Name      string    `json:"name,omitempty" yaml:"name,omitempty"`
	Role      Role      `json:"role" yaml:"role"`
	Projects  []string  `json:"projects,omitempty" yaml:"projects,omitempty"` // Empty = all projects
	JoinedAt  time.Time `json:"joined_at" yaml:"joined_at"`
	InvitedBy string    `json:"invited_by,omitempty" yaml:"invited_by,omitempty"`
}

// Role defines permission levels within a team
type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
)

// Permission defines specific actions
type Permission string

const (
	// Team management
	PermManageTeam    Permission = "manage_team"    // Delete team, change settings
	PermManageMembers Permission = "manage_members" // Add/remove members, change roles
	PermManageBilling Permission = "manage_billing" // View/modify billing

	// Project access
	PermManageProjects Permission = "manage_projects" // Add/remove projects
	PermExecuteTasks   Permission = "execute_tasks"   // Run tasks on projects
	PermViewProjects   Permission = "view_projects"   // View project status

	// Task operations
	PermCreateTasks  Permission = "create_tasks" // Create new tasks
	PermCancelTasks  Permission = "cancel_tasks" // Cancel running tasks
	PermViewTasks    Permission = "view_tasks"   // View task details
	PermViewAuditLog Permission = "view_audit_log"
)

// rolePermissions maps roles to their permissions
var rolePermissions = map[Role][]Permission{
	RoleOwner: {
		PermManageTeam, PermManageMembers, PermManageBilling,
		PermManageProjects, PermExecuteTasks, PermViewProjects,
		PermCreateTasks, PermCancelTasks, PermViewTasks, PermViewAuditLog,
	},
	RoleAdmin: {
		PermManageMembers,
		PermManageProjects, PermExecuteTasks, PermViewProjects,
		PermCreateTasks, PermCancelTasks, PermViewTasks, PermViewAuditLog,
	},
	RoleDeveloper: {
		PermExecuteTasks, PermViewProjects,
		PermCreateTasks, PermCancelTasks, PermViewTasks,
	},
	RoleViewer: {
		PermViewProjects, PermViewTasks,
	},
}

// HasPermission checks if a role has a specific permission
func (r Role) HasPermission(perm Permission) bool {
	perms, ok := rolePermissions[r]
	if !ok {
		return false
	}
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}

// Level returns the hierarchy level of a role (higher = more permissions)
func (r Role) Level() int {
	levels := map[Role]int{
		RoleOwner:     4,
		RoleAdmin:     3,
		RoleDeveloper: 2,
		RoleViewer:    1,
	}
	return levels[r]
}

// CanManage checks if this role can manage another role
func (r Role) CanManage(other Role) bool {
	return r.Level() > other.Level()
}

// IsValid checks if the role is valid
func (r Role) IsValid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer:
		return true
	default:
		return false
	}
}

// Permissions returns all permissions for the role
func (r Role) Permissions() []Permission {
	return rolePermissions[r]
}

// AuditAction defines audit log action types
type AuditAction string

const (
	AuditTeamCreated     AuditAction = "team.created"
	AuditTeamUpdated     AuditAction = "team.updated"
	AuditTeamDeleted     AuditAction = "team.deleted"
	AuditMemberAdded     AuditAction = "member.added"
	AuditMemberRemoved   AuditAction = "member.removed"
	AuditMemberUpdated   AuditAction = "member.updated"
	AuditRoleChanged     AuditAction = "role.changed"
	AuditProjectAdded    AuditAction = "project.added"
	AuditProjectRemoved  AuditAction = "project.removed"
	AuditTaskCreated     AuditAction = "task.created"
	AuditTaskCompleted   AuditAction = "task.completed"
	AuditTaskFailed      AuditAction = "task.failed"
	AuditTaskCancelled   AuditAction = "task.cancelled"
	AuditSettingsChanged AuditAction = "settings.changed"
)

// AuditEntry represents an audit log entry
type AuditEntry struct {
	ID         string                 `json:"id"`
	TeamID     string                 `json:"team_id"`
	ActorID    string                 `json:"actor_id"`    // Member who performed action
	ActorEmail string                 `json:"actor_email"` // For display
	Action     AuditAction            `json:"action"`
	Resource   string                 `json:"resource"`              // Type of resource affected
	ResourceID string                 `json:"resource_id,omitempty"` // ID of affected resource
	Details    map[string]interface{} `json:"details,omitempty"`     // Additional context
	CreatedAt  time.Time              `json:"created_at"`
}

// ProjectAccess represents per-project access control
type ProjectAccess struct {
	TeamID      string `json:"team_id"`
	ProjectPath string `json:"project_path"`
	DefaultRole Role   `json:"default_role"` // Default role for team members
}

// NewTeam creates a new team with an owner
func NewTeam(name string, ownerEmail string) (*Team, *Member) {
	now := time.Now()
	teamID := uuid.New().String()

	team := &Team{
		ID:        teamID,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
		Settings: Settings{
			MaxConcurrentTasks: 2,
			DefaultBranch:      "main",
		},
	}

	owner := &Member{
		ID:       uuid.New().String(),
		TeamID:   teamID,
		Email:    ownerEmail,
		Role:     RoleOwner,
		JoinedAt: now,
	}

	return team, owner
}

// NewMember creates a new team member
func NewMember(teamID, email string, role Role, invitedBy string) *Member {
	return &Member{
		ID:        uuid.New().String(),
		TeamID:    teamID,
		Email:     email,
		Role:      role,
		JoinedAt:  time.Now(),
		InvitedBy: invitedBy,
	}
}
