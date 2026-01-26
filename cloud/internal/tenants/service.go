package tenants

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Service provides multi-tenant business logic
type Service struct {
	store *Store
}

// NewService creates a new tenant service
func NewService(store *Store) *Service {
	return &Service{store: store}
}

// CreateOrgInput holds data for creating an organization
type CreateOrgInput struct {
	Name      string
	OwnerName string
	Email     string
	Password  string
}

// CreateOrganizationWithOwner creates an organization and its owner in a transaction
func (s *Service) CreateOrganizationWithOwner(ctx context.Context, input CreateOrgInput) (*Organization, *User, error) {
	now := time.Now()
	userID := uuid.New()
	orgID := uuid.New()

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Create user
	user := &User{
		ID:            userID,
		Email:         strings.ToLower(input.Email),
		Name:          input.OwnerName,
		PasswordHash:  string(hash),
		EmailVerified: false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateUser(ctx, user); err != nil {
		return nil, nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Create organization
	org := &Organization{
		ID:        orgID,
		Name:      input.Name,
		Slug:      slugify(input.Name),
		PlanID:    "free",
		OwnerID:   userID,
		Settings: OrgSettings{
			MaxConcurrentTasks: 1,
			DefaultBranch:      "main",
			WebhookSecret:      generateSecret(32),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.store.CreateOrganization(ctx, org); err != nil {
		return nil, nil, fmt.Errorf("failed to create organization: %w", err)
	}

	// Create owner membership
	membership := &Membership{
		ID:       uuid.New(),
		OrgID:    orgID,
		UserID:   userID,
		Role:     RoleOwner,
		JoinedAt: now,
	}

	if err := s.store.CreateMembership(ctx, membership); err != nil {
		return nil, nil, fmt.Errorf("failed to create membership: %w", err)
	}

	return org, user, nil
}

// InviteMember invites a new member to an organization
func (s *Service) InviteMember(ctx context.Context, orgID, inviterID uuid.UUID, email string, role Role) (*Invitation, error) {
	// Check inviter has permission
	membership, err := s.store.GetMembership(ctx, orgID, inviterID)
	if err != nil {
		return nil, fmt.Errorf("inviter not a member: %w", err)
	}

	// Owners and admins can invite
	if !membership.Role.HasPermission(RoleAdmin) {
		return nil, ErrForbidden
	}

	// Cannot invite role higher than own
	if !membership.Role.HasPermission(role) {
		return nil, fmt.Errorf("cannot invite role higher than own")
	}

	// Check if user is already a member
	existingUser, err := s.store.GetUserByEmail(ctx, strings.ToLower(email))
	if err == nil {
		_, err := s.store.GetMembership(ctx, orgID, existingUser.ID)
		if err == nil {
			return nil, ErrAlreadyExists
		}
	}

	// Create invitation
	inv := &Invitation{
		ID:        uuid.New(),
		OrgID:     orgID,
		Email:     strings.ToLower(email),
		Role:      role,
		Token:     generateSecret(32),
		InvitedBy: inviterID,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour), // 7 days
		CreatedAt: time.Now(),
	}

	if err := s.store.CreateInvitation(ctx, inv); err != nil {
		return nil, err
	}

	return inv, nil
}

// AcceptInvitation accepts an invitation and creates membership
func (s *Service) AcceptInvitation(ctx context.Context, token string, userID uuid.UUID) error {
	inv, err := s.store.GetInvitationByToken(ctx, token)
	if err != nil {
		return fmt.Errorf("invalid or expired invitation: %w", err)
	}

	// Check user exists
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("user not found: %w", err)
	}

	// Verify email matches
	if strings.ToLower(user.Email) != strings.ToLower(inv.Email) {
		return fmt.Errorf("email mismatch")
	}

	// Create membership
	membership := &Membership{
		ID:        uuid.New(),
		OrgID:     inv.OrgID,
		UserID:    userID,
		Role:      inv.Role,
		InvitedBy: &inv.InvitedBy,
		JoinedAt:  time.Now(),
	}

	if err := s.store.CreateMembership(ctx, membership); err != nil {
		return fmt.Errorf("failed to create membership: %w", err)
	}

	// Delete invitation
	if err := s.store.DeleteInvitation(ctx, inv.ID); err != nil {
		return fmt.Errorf("failed to delete invitation: %w", err)
	}

	return nil
}

// AddProject adds a project to an organization
func (s *Service) AddProject(ctx context.Context, orgID, userID uuid.UUID, name, repoURL string) (*Project, error) {
	// Check user has permission
	membership, err := s.store.GetMembership(ctx, orgID, userID)
	if err != nil {
		return nil, fmt.Errorf("user not a member: %w", err)
	}

	if !membership.Role.HasPermission(RoleMember) {
		return nil, ErrForbidden
	}

	// Check project limit
	org, err := s.store.GetOrganization(ctx, orgID)
	if err != nil {
		return nil, err
	}

	plans := DefaultPlans()
	var plan *Plan
	for _, p := range plans {
		if p.ID == org.PlanID {
			plan = &p
			break
		}
	}

	if plan != nil && plan.ProjectLimit > 0 {
		projects, err := s.store.ListProjects(ctx, orgID)
		if err != nil {
			return nil, err
		}
		if len(projects) >= plan.ProjectLimit {
			return nil, fmt.Errorf("project limit reached for plan %s", plan.Name)
		}
	}

	// Create project
	now := time.Now()
	project := &Project{
		ID:            uuid.New(),
		OrgID:         orgID,
		Name:          name,
		RepoURL:       repoURL,
		DefaultBranch: "main",
		Settings: ProjectSettings{
			NavigatorEnabled: true,
			AutoMerge:        false,
			RequireReview:    true,
		},
		CreatedAt: now,
		UpdatedAt: now,
		IsActive:  true,
	}

	if err := s.store.CreateProject(ctx, project); err != nil {
		return nil, err
	}

	return project, nil
}

// Authenticate validates credentials and returns user
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	user, err := s.store.GetUserByEmail(ctx, strings.ToLower(email))
	if err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	// Update last login
	now := time.Now()
	user.LastLoginAt = &now
	_ = s.store.UpdateUser(ctx, user)

	return user, nil
}

// CheckPermission verifies a user has required role in an org
func (s *Service) CheckPermission(ctx context.Context, orgID, userID uuid.UUID, required Role) error {
	membership, err := s.store.GetMembership(ctx, orgID, userID)
	if err != nil {
		return ErrForbidden
	}

	if !membership.Role.HasPermission(required) {
		return ErrForbidden
	}

	return nil
}

// LogAction records an audit log entry
func (s *Service) LogAction(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, action, resource string, resourceID string, metadata map[string]interface{}, ip, userAgent string) {
	log := &AuditLog{
		ID:         uuid.New(),
		OrgID:      orgID,
		UserID:     userID,
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		Metadata:   metadata,
		IPAddress:  ip,
		UserAgent:  userAgent,
		CreatedAt:  time.Now(),
	}

	// Fire and forget - audit logging should not block
	_ = s.store.CreateAuditLog(ctx, log)
}

// Helper functions

func slugify(s string) string {
	// Convert to lowercase
	slug := strings.ToLower(s)
	// Replace spaces with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	// Remove non-alphanumeric except hyphens
	reg := regexp.MustCompile("[^a-z0-9-]+")
	slug = reg.ReplaceAllString(slug, "")
	// Remove consecutive hyphens
	reg = regexp.MustCompile("-+")
	slug = reg.ReplaceAllString(slug, "-")
	// Trim hyphens
	slug = strings.Trim(slug, "-")
	return slug
}

func generateSecret(length int) string {
	bytes := make([]byte, length)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
