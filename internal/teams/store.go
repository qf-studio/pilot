package teams

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides persistent storage for teams
type Store struct {
	db *sql.DB
}

// NewStore creates a new team store using an existing database connection
func NewStore(db *sql.DB) (*Store, error) {
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate team tables: %w", err)
	}
	return store, nil
}

// migrate creates necessary tables for team management
func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			settings TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS team_members (
			id TEXT PRIMARY KEY,
			team_id TEXT NOT NULL,
			email TEXT NOT NULL,
			name TEXT,
			role TEXT NOT NULL,
			projects TEXT,
			joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			invited_by TEXT,
			FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE,
			UNIQUE(team_id, email)
		)`,
		`CREATE TABLE IF NOT EXISTS team_audit_log (
			id TEXT PRIMARY KEY,
			team_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			actor_email TEXT NOT NULL,
			action TEXT NOT NULL,
			resource TEXT NOT NULL,
			resource_id TEXT,
			details TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS project_access (
			team_id TEXT NOT NULL,
			project_path TEXT NOT NULL,
			default_role TEXT NOT NULL DEFAULT 'developer',
			PRIMARY KEY (team_id, project_path),
			FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team_id)`,
		`CREATE INDEX IF NOT EXISTS idx_team_members_email ON team_members(email)`,
		`CREATE INDEX IF NOT EXISTS idx_team_audit_log_team ON team_audit_log(team_id)`,
		`CREATE INDEX IF NOT EXISTS idx_team_audit_log_created ON team_audit_log(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_team_audit_log_actor ON team_audit_log(actor_id)`,
		`CREATE INDEX IF NOT EXISTS idx_project_access_project ON project_access(project_path)`,
	}

	for _, migration := range migrations {
		_, err := s.db.Exec(migration)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "duplicate column") {
				continue
			}
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// CreateTeam creates a new team
func (s *Store) CreateTeam(team *Team) error {
	settings, _ := json.Marshal(team.Settings)
	_, err := s.db.Exec(`
		INSERT INTO teams (id, name, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, team.ID, team.Name, string(settings), team.CreatedAt, team.UpdatedAt)
	return err
}

// GetTeam retrieves a team by ID
func (s *Store) GetTeam(id string) (*Team, error) {
	row := s.db.QueryRow(`
		SELECT id, name, settings, created_at, updated_at
		FROM teams WHERE id = ?
	`, id)

	var team Team
	var settingsStr sql.NullString
	if err := row.Scan(&team.ID, &team.Name, &settingsStr, &team.CreatedAt, &team.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if settingsStr.Valid && settingsStr.String != "" {
		_ = json.Unmarshal([]byte(settingsStr.String), &team.Settings)
	}

	return &team, nil
}

// GetTeamByName retrieves a team by name
func (s *Store) GetTeamByName(name string) (*Team, error) {
	row := s.db.QueryRow(`
		SELECT id, name, settings, created_at, updated_at
		FROM teams WHERE name = ?
	`, name)

	var team Team
	var settingsStr sql.NullString
	if err := row.Scan(&team.ID, &team.Name, &settingsStr, &team.CreatedAt, &team.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if settingsStr.Valid && settingsStr.String != "" {
		_ = json.Unmarshal([]byte(settingsStr.String), &team.Settings)
	}

	return &team, nil
}

// UpdateTeam updates a team
func (s *Store) UpdateTeam(team *Team) error {
	settings, _ := json.Marshal(team.Settings)
	team.UpdatedAt = time.Now()
	_, err := s.db.Exec(`
		UPDATE teams SET name = ?, settings = ?, updated_at = ?
		WHERE id = ?
	`, team.Name, string(settings), team.UpdatedAt, team.ID)
	return err
}

// DeleteTeam deletes a team (cascades to members, audit log, project access)
func (s *Store) DeleteTeam(id string) error {
	_, err := s.db.Exec(`DELETE FROM teams WHERE id = ?`, id)
	return err
}

// ListTeams retrieves all teams
func (s *Store) ListTeams() ([]*Team, error) {
	rows, err := s.db.Query(`
		SELECT id, name, settings, created_at, updated_at
		FROM teams ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var teams []*Team
	for rows.Next() {
		var team Team
		var settingsStr sql.NullString
		if err := rows.Scan(&team.ID, &team.Name, &settingsStr, &team.CreatedAt, &team.UpdatedAt); err != nil {
			return nil, err
		}
		if settingsStr.Valid && settingsStr.String != "" {
			_ = json.Unmarshal([]byte(settingsStr.String), &team.Settings)
		}
		teams = append(teams, &team)
	}

	return teams, nil
}

// AddMember adds a member to a team
func (s *Store) AddMember(member *Member) error {
	projects, _ := json.Marshal(member.Projects)
	_, err := s.db.Exec(`
		INSERT INTO team_members (id, team_id, email, name, role, projects, joined_at, invited_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, member.ID, member.TeamID, member.Email, member.Name, string(member.Role), string(projects), member.JoinedAt, member.InvitedBy)
	return err
}

// GetMember retrieves a member by ID
func (s *Store) GetMember(id string) (*Member, error) {
	row := s.db.QueryRow(`
		SELECT id, team_id, email, name, role, projects, joined_at, invited_by
		FROM team_members WHERE id = ?
	`, id)
	return s.scanMember(row)
}

// GetMemberByEmail retrieves a member by email within a team
func (s *Store) GetMemberByEmail(teamID, email string) (*Member, error) {
	row := s.db.QueryRow(`
		SELECT id, team_id, email, name, role, projects, joined_at, invited_by
		FROM team_members WHERE team_id = ? AND email = ?
	`, teamID, email)
	return s.scanMember(row)
}

// GetMembersByEmail retrieves all memberships for an email (across teams)
func (s *Store) GetMembersByEmail(email string) ([]*Member, error) {
	rows, err := s.db.Query(`
		SELECT id, team_id, email, name, role, projects, joined_at, invited_by
		FROM team_members WHERE email = ?
	`, email)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanMembers(rows)
}

// ListMembers retrieves all members of a team
func (s *Store) ListMembers(teamID string) ([]*Member, error) {
	rows, err := s.db.Query(`
		SELECT id, team_id, email, name, role, projects, joined_at, invited_by
		FROM team_members WHERE team_id = ?
		ORDER BY role, email
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanMembers(rows)
}

// UpdateMember updates a member
func (s *Store) UpdateMember(member *Member) error {
	projects, _ := json.Marshal(member.Projects)
	_, err := s.db.Exec(`
		UPDATE team_members SET name = ?, role = ?, projects = ?
		WHERE id = ?
	`, member.Name, string(member.Role), string(projects), member.ID)
	return err
}

// RemoveMember removes a member from a team
func (s *Store) RemoveMember(id string) error {
	_, err := s.db.Exec(`DELETE FROM team_members WHERE id = ?`, id)
	return err
}

// CountMembersByRole counts members by role in a team
func (s *Store) CountMembersByRole(teamID string, role Role) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM team_members WHERE team_id = ? AND role = ?
	`, teamID, string(role)).Scan(&count)
	return count, err
}

// scanMember scans a single member row
func (s *Store) scanMember(row *sql.Row) (*Member, error) {
	var member Member
	var name, projects, invitedBy sql.NullString
	var roleStr string

	if err := row.Scan(&member.ID, &member.TeamID, &member.Email, &name, &roleStr, &projects, &member.JoinedAt, &invitedBy); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	member.Role = Role(roleStr)
	if name.Valid {
		member.Name = name.String
	}
	if invitedBy.Valid {
		member.InvitedBy = invitedBy.String
	}
	if projects.Valid && projects.String != "" {
		_ = json.Unmarshal([]byte(projects.String), &member.Projects)
	}

	return &member, nil
}

// scanMembers scans multiple member rows
func (s *Store) scanMembers(rows *sql.Rows) ([]*Member, error) {
	var members []*Member
	for rows.Next() {
		var member Member
		var name, projects, invitedBy sql.NullString
		var roleStr string

		if err := rows.Scan(&member.ID, &member.TeamID, &member.Email, &name, &roleStr, &projects, &member.JoinedAt, &invitedBy); err != nil {
			return nil, err
		}

		member.Role = Role(roleStr)
		if name.Valid {
			member.Name = name.String
		}
		if invitedBy.Valid {
			member.InvitedBy = invitedBy.String
		}
		if projects.Valid && projects.String != "" {
			_ = json.Unmarshal([]byte(projects.String), &member.Projects)
		}

		members = append(members, &member)
	}
	return members, nil
}

// AddAuditEntry adds an audit log entry
func (s *Store) AddAuditEntry(entry *AuditEntry) error {
	details, _ := json.Marshal(entry.Details)
	_, err := s.db.Exec(`
		INSERT INTO team_audit_log (id, team_id, actor_id, actor_email, action, resource, resource_id, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.ID, entry.TeamID, entry.ActorID, entry.ActorEmail, string(entry.Action), entry.Resource, entry.ResourceID, string(details), entry.CreatedAt)
	return err
}

// GetAuditLog retrieves audit log entries for a team
func (s *Store) GetAuditLog(teamID string, limit int) ([]*AuditEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, team_id, actor_id, actor_email, action, resource, resource_id, details, created_at
		FROM team_audit_log WHERE team_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, teamID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []*AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var resourceID, detailsStr sql.NullString
		var actionStr string

		if err := rows.Scan(&entry.ID, &entry.TeamID, &entry.ActorID, &entry.ActorEmail, &actionStr, &entry.Resource, &resourceID, &detailsStr, &entry.CreatedAt); err != nil {
			return nil, err
		}

		entry.Action = AuditAction(actionStr)
		if resourceID.Valid {
			entry.ResourceID = resourceID.String
		}
		if detailsStr.Valid && detailsStr.String != "" {
			_ = json.Unmarshal([]byte(detailsStr.String), &entry.Details)
		}

		entries = append(entries, &entry)
	}

	return entries, nil
}

// SetProjectAccess sets the default role for a project within a team
func (s *Store) SetProjectAccess(access *ProjectAccess) error {
	_, err := s.db.Exec(`
		INSERT INTO project_access (team_id, project_path, default_role)
		VALUES (?, ?, ?)
		ON CONFLICT(team_id, project_path) DO UPDATE SET
			default_role = excluded.default_role
	`, access.TeamID, access.ProjectPath, string(access.DefaultRole))
	return err
}

// GetProjectAccess retrieves project access for a team
func (s *Store) GetProjectAccess(teamID, projectPath string) (*ProjectAccess, error) {
	row := s.db.QueryRow(`
		SELECT team_id, project_path, default_role
		FROM project_access WHERE team_id = ? AND project_path = ?
	`, teamID, projectPath)

	var access ProjectAccess
	var roleStr string
	if err := row.Scan(&access.TeamID, &access.ProjectPath, &roleStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	access.DefaultRole = Role(roleStr)

	return &access, nil
}

// ListProjectAccess retrieves all project access entries for a team
func (s *Store) ListProjectAccess(teamID string) ([]*ProjectAccess, error) {
	rows, err := s.db.Query(`
		SELECT team_id, project_path, default_role
		FROM project_access WHERE team_id = ?
		ORDER BY project_path
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var accesses []*ProjectAccess
	for rows.Next() {
		var access ProjectAccess
		var roleStr string
		if err := rows.Scan(&access.TeamID, &access.ProjectPath, &roleStr); err != nil {
			return nil, err
		}
		access.DefaultRole = Role(roleStr)
		accesses = append(accesses, &access)
	}

	return accesses, nil
}

// RemoveProjectAccess removes project access entry
func (s *Store) RemoveProjectAccess(teamID, projectPath string) error {
	_, err := s.db.Exec(`DELETE FROM project_access WHERE team_id = ? AND project_path = ?`, teamID, projectPath)
	return err
}
