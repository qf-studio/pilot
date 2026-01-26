package tenants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrForbidden     = errors.New("forbidden")
)

// Store provides multi-tenant data access
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new tenant store
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// --- Organization Operations ---

// CreateOrganization creates a new organization
func (s *Store) CreateOrganization(ctx context.Context, org *Organization) error {
	settings, _ := json.Marshal(org.Settings)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, plan_id, owner_id, settings, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, org.ID, org.Name, org.Slug, org.PlanID, org.OwnerID, settings, org.CreatedAt, org.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create organization: %w", err)
	}
	return nil
}

// GetOrganization retrieves an organization by ID
func (s *Store) GetOrganization(ctx context.Context, id uuid.UUID) (*Organization, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, slug, plan_id, owner_id, settings, created_at, updated_at, suspended_at
		FROM organizations WHERE id = $1
	`, id)

	return s.scanOrganization(row)
}

// GetOrganizationBySlug retrieves an organization by slug
func (s *Store) GetOrganizationBySlug(ctx context.Context, slug string) (*Organization, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, slug, plan_id, owner_id, settings, created_at, updated_at, suspended_at
		FROM organizations WHERE slug = $1
	`, slug)

	return s.scanOrganization(row)
}

func (s *Store) scanOrganization(row pgx.Row) (*Organization, error) {
	var org Organization
	var settingsJSON []byte
	var suspendedAt *time.Time

	err := row.Scan(&org.ID, &org.Name, &org.Slug, &org.PlanID, &org.OwnerID,
		&settingsJSON, &org.CreatedAt, &org.UpdatedAt, &suspendedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if settingsJSON != nil {
		_ = json.Unmarshal(settingsJSON, &org.Settings)
	}
	org.SuspendedAt = suspendedAt

	return &org, nil
}

// UpdateOrganization updates an organization
func (s *Store) UpdateOrganization(ctx context.Context, org *Organization) error {
	settings, _ := json.Marshal(org.Settings)

	result, err := s.pool.Exec(ctx, `
		UPDATE organizations
		SET name = $2, slug = $3, plan_id = $4, settings = $5, updated_at = $6, suspended_at = $7
		WHERE id = $1
	`, org.ID, org.Name, org.Slug, org.PlanID, settings, time.Now(), org.SuspendedAt)

	if err != nil {
		return fmt.Errorf("failed to update organization: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListOrganizationsForUser returns all organizations a user belongs to
func (s *Store) ListOrganizationsForUser(ctx context.Context, userID uuid.UUID) ([]*Organization, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.plan_id, o.owner_id, o.settings, o.created_at, o.updated_at, o.suspended_at
		FROM organizations o
		JOIN memberships m ON o.id = m.org_id
		WHERE m.user_id = $1
		ORDER BY o.name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []*Organization
	for rows.Next() {
		var org Organization
		var settingsJSON []byte
		var suspendedAt *time.Time

		if err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.PlanID, &org.OwnerID,
			&settingsJSON, &org.CreatedAt, &org.UpdatedAt, &suspendedAt); err != nil {
			return nil, err
		}

		if settingsJSON != nil {
			_ = json.Unmarshal(settingsJSON, &org.Settings)
		}
		org.SuspendedAt = suspendedAt
		orgs = append(orgs, &org)
	}

	return orgs, nil
}

// --- User Operations ---

// CreateUser creates a new user
func (s *Store) CreateUser(ctx context.Context, user *User) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, avatar_url, password_hash, email_verified, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, user.ID, user.Email, user.Name, user.AvatarURL, user.PasswordHash, user.EmailVerified, user.CreatedAt, user.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetUser retrieves a user by ID
func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, email, name, avatar_url, password_hash, email_verified, created_at, updated_at, last_login_at
		FROM users WHERE id = $1
	`, id)

	return s.scanUser(row)
}

// GetUserByEmail retrieves a user by email
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, email, name, avatar_url, password_hash, email_verified, created_at, updated_at, last_login_at
		FROM users WHERE email = $1
	`, email)

	return s.scanUser(row)
}

func (s *Store) scanUser(row pgx.Row) (*User, error) {
	var user User
	var lastLoginAt *time.Time

	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.AvatarURL, &user.PasswordHash,
		&user.EmailVerified, &user.CreatedAt, &user.UpdatedAt, &lastLoginAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	user.LastLoginAt = lastLoginAt
	return &user, nil
}

// UpdateUser updates a user
func (s *Store) UpdateUser(ctx context.Context, user *User) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE users
		SET email = $2, name = $3, avatar_url = $4, email_verified = $5, updated_at = $6, last_login_at = $7
		WHERE id = $1
	`, user.ID, user.Email, user.Name, user.AvatarURL, user.EmailVerified, time.Now(), user.LastLoginAt)

	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Membership Operations ---

// CreateMembership creates a new membership
func (s *Store) CreateMembership(ctx context.Context, membership *Membership) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO memberships (id, org_id, user_id, role, invited_by, joined_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, membership.ID, membership.OrgID, membership.UserID, membership.Role, membership.InvitedBy, membership.JoinedAt)

	if err != nil {
		return fmt.Errorf("failed to create membership: %w", err)
	}
	return nil
}

// GetMembership retrieves a membership
func (s *Store) GetMembership(ctx context.Context, orgID, userID uuid.UUID) (*Membership, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, user_id, role, invited_by, joined_at
		FROM memberships WHERE org_id = $1 AND user_id = $2
	`, orgID, userID)

	var m Membership
	var invitedBy *uuid.UUID

	err := row.Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &invitedBy, &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	m.InvitedBy = invitedBy
	return &m, nil
}

// ListMemberships returns all memberships for an organization
func (s *Store) ListMemberships(ctx context.Context, orgID uuid.UUID) ([]*Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, user_id, role, invited_by, joined_at
		FROM memberships WHERE org_id = $1
		ORDER BY joined_at
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []*Membership
	for rows.Next() {
		var m Membership
		var invitedBy *uuid.UUID

		if err := rows.Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &invitedBy, &m.JoinedAt); err != nil {
			return nil, err
		}

		m.InvitedBy = invitedBy
		memberships = append(memberships, &m)
	}

	return memberships, nil
}

// UpdateMembership updates a membership role
func (s *Store) UpdateMembership(ctx context.Context, orgID, userID uuid.UUID, role Role) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE memberships SET role = $3 WHERE org_id = $1 AND user_id = $2
	`, orgID, userID, role)

	if err != nil {
		return fmt.Errorf("failed to update membership: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMembership removes a membership
func (s *Store) DeleteMembership(ctx context.Context, orgID, userID uuid.UUID) error {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM memberships WHERE org_id = $1 AND user_id = $2
	`, orgID, userID)

	if err != nil {
		return fmt.Errorf("failed to delete membership: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Project Operations ---

// CreateProject creates a new project
func (s *Store) CreateProject(ctx context.Context, project *Project) error {
	settings, _ := json.Marshal(project.Settings)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO projects (id, org_id, name, repo_url, default_branch, integration_id, settings, created_at, updated_at, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, project.ID, project.OrgID, project.Name, project.RepoURL, project.DefaultBranch,
		project.IntegrationID, settings, project.CreatedAt, project.UpdatedAt, project.IsActive)

	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}
	return nil
}

// GetProject retrieves a project by ID
func (s *Store) GetProject(ctx context.Context, id uuid.UUID) (*Project, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, name, repo_url, default_branch, integration_id, settings, created_at, updated_at, is_active
		FROM projects WHERE id = $1
	`, id)

	return s.scanProject(row)
}

func (s *Store) scanProject(row pgx.Row) (*Project, error) {
	var p Project
	var settingsJSON []byte
	var integrationID *uuid.UUID

	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.RepoURL, &p.DefaultBranch,
		&integrationID, &settingsJSON, &p.CreatedAt, &p.UpdatedAt, &p.IsActive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if settingsJSON != nil {
		_ = json.Unmarshal(settingsJSON, &p.Settings)
	}
	p.IntegrationID = integrationID

	return &p, nil
}

// ListProjects returns all projects for an organization
func (s *Store) ListProjects(ctx context.Context, orgID uuid.UUID) ([]*Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, name, repo_url, default_branch, integration_id, settings, created_at, updated_at, is_active
		FROM projects WHERE org_id = $1
		ORDER BY name
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var p Project
		var settingsJSON []byte
		var integrationID *uuid.UUID

		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.RepoURL, &p.DefaultBranch,
			&integrationID, &settingsJSON, &p.CreatedAt, &p.UpdatedAt, &p.IsActive); err != nil {
			return nil, err
		}

		if settingsJSON != nil {
			_ = json.Unmarshal(settingsJSON, &p.Settings)
		}
		p.IntegrationID = integrationID
		projects = append(projects, &p)
	}

	return projects, nil
}

// --- Audit Log Operations ---

// CreateAuditLog records an action
func (s *Store) CreateAuditLog(ctx context.Context, log *AuditLog) error {
	metadata, _ := json.Marshal(log.Metadata)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_logs (id, org_id, user_id, action, resource, resource_id, metadata, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, log.ID, log.OrgID, log.UserID, log.Action, log.Resource, log.ResourceID, metadata, log.IPAddress, log.UserAgent, log.CreatedAt)

	return err
}

// ListAuditLogs returns audit logs for an organization
func (s *Store) ListAuditLogs(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*AuditLog, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, user_id, action, resource, resource_id, metadata, ip_address, user_agent, created_at
		FROM audit_logs WHERE org_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, orgID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*AuditLog
	for rows.Next() {
		var log AuditLog
		var metadataJSON []byte
		var userID *uuid.UUID

		if err := rows.Scan(&log.ID, &log.OrgID, &userID, &log.Action, &log.Resource,
			&log.ResourceID, &metadataJSON, &log.IPAddress, &log.UserAgent, &log.CreatedAt); err != nil {
			return nil, err
		}

		if metadataJSON != nil {
			_ = json.Unmarshal(metadataJSON, &log.Metadata)
		}
		log.UserID = userID
		logs = append(logs, &log)
	}

	return logs, nil
}

// --- Invitation Operations ---

// CreateInvitation creates a new invitation
func (s *Store) CreateInvitation(ctx context.Context, inv *Invitation) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO invitations (id, org_id, email, role, token, invited_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, inv.ID, inv.OrgID, inv.Email, inv.Role, inv.Token, inv.InvitedBy, inv.ExpiresAt, inv.CreatedAt)

	return err
}

// GetInvitationByToken retrieves an invitation by token
func (s *Store) GetInvitationByToken(ctx context.Context, token string) (*Invitation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, email, role, token, invited_by, expires_at, created_at
		FROM invitations WHERE token = $1 AND expires_at > NOW()
	`, token)

	var inv Invitation
	err := row.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.Token,
		&inv.InvitedBy, &inv.ExpiresAt, &inv.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &inv, nil
}

// DeleteInvitation removes an invitation
func (s *Store) DeleteInvitation(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, id)
	return err
}
