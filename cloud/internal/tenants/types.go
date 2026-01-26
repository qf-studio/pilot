package tenants

import (
	"time"

	"github.com/google/uuid"
)

// Organization represents a tenant in Pilot Cloud
type Organization struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	PlanID      string    `json:"plan_id"`
	OwnerID     uuid.UUID `json:"owner_id"`
	Settings    OrgSettings `json:"settings"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SuspendedAt *time.Time `json:"suspended_at,omitempty"`
}

// OrgSettings holds organization-specific settings
type OrgSettings struct {
	MaxConcurrentTasks int    `json:"max_concurrent_tasks"`
	DefaultBranch      string `json:"default_branch"`
	WebhookSecret      string `json:"-"` // Not exposed in JSON
	AllowedDomains     []string `json:"allowed_domains,omitempty"`
}

// User represents a user in the system
type User struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	PasswordHash  string    `json:"-"` // Not exposed in JSON
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
}

// Membership represents a user's membership in an organization
type Membership struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	UserID    uuid.UUID `json:"user_id"`
	Role      Role      `json:"role"`
	InvitedBy *uuid.UUID `json:"invited_by,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
}

// Role defines permission levels within an organization
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// HasPermission checks if a role has a specific permission
func (r Role) HasPermission(required Role) bool {
	hierarchy := map[Role]int{
		RoleOwner:  4,
		RoleAdmin:  3,
		RoleMember: 2,
		RoleViewer: 1,
	}
	return hierarchy[r] >= hierarchy[required]
}

// Project represents a connected repository/project
type Project struct {
	ID            uuid.UUID `json:"id"`
	OrgID         uuid.UUID `json:"org_id"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	DefaultBranch string    `json:"default_branch"`
	IntegrationID *uuid.UUID `json:"integration_id,omitempty"` // Link to OAuth integration
	Settings      ProjectSettings `json:"settings"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	IsActive      bool      `json:"is_active"`
}

// ProjectSettings holds project-specific configuration
type ProjectSettings struct {
	NavigatorEnabled bool     `json:"navigator_enabled"`
	AutoMerge        bool     `json:"auto_merge"`
	RequireReview    bool     `json:"require_review"`
	AllowedLabels    []string `json:"allowed_labels,omitempty"`
}

// Invitation represents a pending org invitation
type Invitation struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	Token     string    `json:"-"` // Not exposed
	InvitedBy uuid.UUID `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditLog records actions for compliance
type AuditLog struct {
	ID         uuid.UUID `json:"id"`
	OrgID      uuid.UUID `json:"org_id"`
	UserID     *uuid.UUID `json:"user_id,omitempty"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	IPAddress  string    `json:"ip_address,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Plan represents a subscription plan
type Plan struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	TasksPerMonth   int     `json:"tasks_per_month"`
	ProjectLimit    int     `json:"project_limit"`
	MemberLimit     int     `json:"member_limit"`
	PriceMonthly    int     `json:"price_monthly"` // In cents
	OveragePerTask  int     `json:"overage_per_task"` // In cents
	Features        []string `json:"features"`
}

// DefaultPlans returns the available subscription plans
func DefaultPlans() []Plan {
	return []Plan{
		{
			ID:             "free",
			Name:           "Free",
			TasksPerMonth:  10,
			ProjectLimit:   1,
			MemberLimit:    1,
			PriceMonthly:   0,
			OveragePerTask: 100, // $1.00
			Features:       []string{"basic_tasks", "community_support"},
		},
		{
			ID:             "pro",
			Name:           "Pro",
			TasksPerMonth:  100,
			ProjectLimit:   5,
			MemberLimit:    5,
			PriceMonthly:   4900, // $49.00
			OveragePerTask: 50,   // $0.50
			Features:       []string{"basic_tasks", "priority_support", "advanced_analytics"},
		},
		{
			ID:             "team",
			Name:           "Team",
			TasksPerMonth:  500,
			ProjectLimit:   -1, // Unlimited
			MemberLimit:    -1, // Unlimited
			PriceMonthly:   19900, // $199.00
			OveragePerTask: 40,    // $0.40
			Features:       []string{"basic_tasks", "priority_support", "advanced_analytics", "sso", "api_access", "audit_logs"},
		},
		{
			ID:             "enterprise",
			Name:           "Enterprise",
			TasksPerMonth:  -1, // Custom
			ProjectLimit:   -1,
			MemberLimit:    -1,
			PriceMonthly:   -1, // Custom
			OveragePerTask: 0,
			Features:       []string{"basic_tasks", "dedicated_support", "advanced_analytics", "sso", "api_access", "audit_logs", "self_hosted", "sla"},
		},
	}
}
