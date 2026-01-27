package teams

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Service provides team management operations with permission checking
type Service struct {
	store *Store
}

// NewService creates a new team service
func NewService(store *Store) *Service {
	return &Service{store: store}
}

// Error types
var (
	ErrTeamNotFound      = fmt.Errorf("team not found")
	ErrMemberNotFound    = fmt.Errorf("member not found")
	ErrPermissionDenied  = fmt.Errorf("permission denied")
	ErrInvalidRole       = fmt.Errorf("invalid role")
	ErrCannotRemoveOwner = fmt.Errorf("cannot remove team owner")
	ErrLastOwner         = fmt.Errorf("cannot remove last owner")
	ErrAlreadyMember     = fmt.Errorf("already a member")
	ErrSelfRoleChange    = fmt.Errorf("cannot change own role")
)

// CreateTeam creates a new team with the given owner
func (s *Service) CreateTeam(name, ownerEmail string) (*Team, *Member, error) {
	team, owner := NewTeam(name, ownerEmail)

	if err := s.store.CreateTeam(team); err != nil {
		return nil, nil, fmt.Errorf("failed to create team: %w", err)
	}

	if err := s.store.AddMember(owner); err != nil {
		// Rollback team creation
		_ = s.store.DeleteTeam(team.ID)
		return nil, nil, fmt.Errorf("failed to add owner: %w", err)
	}

	// Log team creation
	_ = s.logAudit(team.ID, owner.ID, owner.Email, AuditTeamCreated, "team", team.ID, map[string]interface{}{
		"name": team.Name,
	})

	return team, owner, nil
}

// GetTeam retrieves a team by ID
func (s *Service) GetTeam(teamID string) (*Team, error) {
	team, err := s.store.GetTeam(teamID)
	if err != nil {
		return nil, err
	}
	if team == nil {
		return nil, ErrTeamNotFound
	}
	return team, nil
}

// GetTeamByName retrieves a team by name
func (s *Service) GetTeamByName(name string) (*Team, error) {
	return s.store.GetTeamByName(name)
}

// ListTeams retrieves all teams
func (s *Service) ListTeams() ([]*Team, error) {
	return s.store.ListTeams()
}

// UpdateTeamSettings updates team settings
func (s *Service) UpdateTeamSettings(teamID, actorID string, settings Settings) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageTeam) {
		return ErrPermissionDenied
	}

	team, err := s.store.GetTeam(teamID)
	if err != nil || team == nil {
		return ErrTeamNotFound
	}

	team.Settings = settings
	if err := s.store.UpdateTeam(team); err != nil {
		return fmt.Errorf("failed to update team: %w", err)
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditSettingsChanged, "team", teamID, map[string]interface{}{
		"settings": settings,
	})

	return nil
}

// DeleteTeam deletes a team (only owner can do this)
func (s *Service) DeleteTeam(teamID, actorID string) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if actor.Role != RoleOwner {
		return ErrPermissionDenied
	}

	team, err := s.store.GetTeam(teamID)
	if err != nil || team == nil {
		return ErrTeamNotFound
	}

	// Log before deletion
	_ = s.logAudit(teamID, actorID, actor.Email, AuditTeamDeleted, "team", teamID, map[string]interface{}{
		"name": team.Name,
	})

	return s.store.DeleteTeam(teamID)
}

// AddMember adds a new member to a team
func (s *Service) AddMember(teamID, actorID, email string, role Role, projects []string) (*Member, error) {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return nil, ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageMembers) {
		return nil, ErrPermissionDenied
	}

	// Can't add someone with higher role than yourself (except owner)
	if role != RoleOwner && !actor.Role.CanManage(role) && actor.Role != RoleOwner {
		return nil, ErrPermissionDenied
	}

	if !role.IsValid() {
		return nil, ErrInvalidRole
	}

	// Check if already a member
	existing, _ := s.store.GetMemberByEmail(teamID, email)
	if existing != nil {
		return nil, ErrAlreadyMember
	}

	member := NewMember(teamID, email, role, actorID)
	member.Projects = projects

	if err := s.store.AddMember(member); err != nil {
		return nil, fmt.Errorf("failed to add member: %w", err)
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditMemberAdded, "member", member.ID, map[string]interface{}{
		"email":    email,
		"role":     string(role),
		"projects": projects,
	})

	return member, nil
}

// RemoveMember removes a member from a team
func (s *Service) RemoveMember(teamID, actorID, memberID string) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageMembers) {
		return ErrPermissionDenied
	}

	member, err := s.store.GetMember(memberID)
	if err != nil || member == nil {
		return ErrMemberNotFound
	}

	// Can't remove owner unless there's another owner
	if member.Role == RoleOwner {
		count, _ := s.store.CountMembersByRole(teamID, RoleOwner)
		if count <= 1 {
			return ErrLastOwner
		}
	}

	// Can't remove someone with equal or higher role (unless you're owner)
	if actor.Role != RoleOwner && !actor.Role.CanManage(member.Role) {
		return ErrPermissionDenied
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditMemberRemoved, "member", memberID, map[string]interface{}{
		"email": member.Email,
		"role":  string(member.Role),
	})

	return s.store.RemoveMember(memberID)
}

// UpdateMemberRole updates a member's role
func (s *Service) UpdateMemberRole(teamID, actorID, memberID string, newRole Role) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageMembers) {
		return ErrPermissionDenied
	}

	member, err := s.store.GetMember(memberID)
	if err != nil || member == nil {
		return ErrMemberNotFound
	}

	// Can't change own role
	if actorID == memberID {
		return ErrSelfRoleChange
	}

	// Can't demote/promote to equal or higher role than yourself (unless owner)
	if actor.Role != RoleOwner {
		if !actor.Role.CanManage(member.Role) || !actor.Role.CanManage(newRole) {
			return ErrPermissionDenied
		}
	}

	// If demoting from owner, ensure there's another owner
	if member.Role == RoleOwner && newRole != RoleOwner {
		count, _ := s.store.CountMembersByRole(teamID, RoleOwner)
		if count <= 1 {
			return ErrLastOwner
		}
	}

	if !newRole.IsValid() {
		return ErrInvalidRole
	}

	oldRole := member.Role
	member.Role = newRole
	if err := s.store.UpdateMember(member); err != nil {
		return fmt.Errorf("failed to update member: %w", err)
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditRoleChanged, "member", memberID, map[string]interface{}{
		"email":    member.Email,
		"old_role": string(oldRole),
		"new_role": string(newRole),
	})

	return nil
}

// UpdateMemberProjects updates a member's project access
func (s *Service) UpdateMemberProjects(teamID, actorID, memberID string, projects []string) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageMembers) {
		return ErrPermissionDenied
	}

	member, err := s.store.GetMember(memberID)
	if err != nil || member == nil {
		return ErrMemberNotFound
	}

	member.Projects = projects
	if err := s.store.UpdateMember(member); err != nil {
		return fmt.Errorf("failed to update member: %w", err)
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditMemberUpdated, "member", memberID, map[string]interface{}{
		"email":    member.Email,
		"projects": projects,
	})

	return nil
}

// GetMember retrieves a member by ID
func (s *Service) GetMember(memberID string) (*Member, error) {
	return s.store.GetMember(memberID)
}

// GetMemberByEmail retrieves a member by email in a team
func (s *Service) GetMemberByEmail(teamID, email string) (*Member, error) {
	return s.store.GetMemberByEmail(teamID, email)
}

// ListMembers lists all members of a team
func (s *Service) ListMembers(teamID string) ([]*Member, error) {
	return s.store.ListMembers(teamID)
}

// GetTeamsForUser retrieves all teams a user belongs to
func (s *Service) GetTeamsForUser(email string) ([]*TeamMembership, error) {
	members, err := s.store.GetMembersByEmail(email)
	if err != nil {
		return nil, err
	}

	var memberships []*TeamMembership
	for _, m := range members {
		team, err := s.store.GetTeam(m.TeamID)
		if err != nil || team == nil {
			continue
		}
		memberships = append(memberships, &TeamMembership{
			Team:   team,
			Member: m,
		})
	}

	return memberships, nil
}

// TeamMembership combines team and member info
type TeamMembership struct {
	Team   *Team
	Member *Member
}

// CheckPermission checks if a member has a specific permission
func (s *Service) CheckPermission(memberID string, perm Permission) error {
	member, err := s.store.GetMember(memberID)
	if err != nil || member == nil {
		return ErrMemberNotFound
	}

	if !member.Role.HasPermission(perm) {
		return ErrPermissionDenied
	}

	return nil
}

// CheckProjectAccess checks if a member can access a project
func (s *Service) CheckProjectAccess(memberID, projectPath string, requiredPerm Permission) error {
	member, err := s.store.GetMember(memberID)
	if err != nil || member == nil {
		return ErrMemberNotFound
	}

	// Check base permission
	if !member.Role.HasPermission(requiredPerm) {
		return ErrPermissionDenied
	}

	// If member has restricted project access, check it
	if len(member.Projects) > 0 {
		allowed := false
		for _, p := range member.Projects {
			if p == projectPath {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrPermissionDenied
		}
	}

	return nil
}

// SetProjectAccess sets the default role for a project
func (s *Service) SetProjectAccess(teamID, actorID, projectPath string, defaultRole Role) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageProjects) {
		return ErrPermissionDenied
	}

	access := &ProjectAccess{
		TeamID:      teamID,
		ProjectPath: projectPath,
		DefaultRole: defaultRole,
	}

	if err := s.store.SetProjectAccess(access); err != nil {
		return fmt.Errorf("failed to set project access: %w", err)
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditProjectAdded, "project", projectPath, map[string]interface{}{
		"default_role": string(defaultRole),
	})

	return nil
}

// RemoveProjectAccess removes project access from a team
func (s *Service) RemoveProjectAccess(teamID, actorID, projectPath string) error {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermManageProjects) {
		return ErrPermissionDenied
	}

	_ = s.logAudit(teamID, actorID, actor.Email, AuditProjectRemoved, "project", projectPath, nil)

	return s.store.RemoveProjectAccess(teamID, projectPath)
}

// ListProjectAccess lists all project access entries for a team
func (s *Service) ListProjectAccess(teamID string) ([]*ProjectAccess, error) {
	return s.store.ListProjectAccess(teamID)
}

// GetAuditLog retrieves audit log entries
func (s *Service) GetAuditLog(teamID, actorID string, limit int) ([]*AuditEntry, error) {
	actor, err := s.store.GetMember(actorID)
	if err != nil || actor == nil {
		return nil, ErrMemberNotFound
	}

	if !actor.Role.HasPermission(PermViewAuditLog) {
		return nil, ErrPermissionDenied
	}

	return s.store.GetAuditLog(teamID, limit)
}

// LogTaskEvent logs a task-related audit event
func (s *Service) LogTaskEvent(teamID, actorID, actorEmail, taskID string, action AuditAction, details map[string]interface{}) error {
	return s.logAudit(teamID, actorID, actorEmail, action, "task", taskID, details)
}

// logAudit creates an audit log entry
func (s *Service) logAudit(teamID, actorID, actorEmail string, action AuditAction, resource, resourceID string, details map[string]interface{}) error {
	entry := &AuditEntry{
		ID:         uuid.New().String(),
		TeamID:     teamID,
		ActorID:    actorID,
		ActorEmail: actorEmail,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Details:    details,
		CreatedAt:  time.Now(),
	}
	return s.store.AddAuditEntry(entry)
}
