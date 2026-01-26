package billing

import (
	"time"

	"github.com/google/uuid"
)

// SubscriptionStatus represents subscription state
type SubscriptionStatus string

const (
	StatusActive    SubscriptionStatus = "active"
	StatusPastDue   SubscriptionStatus = "past_due"
	StatusCanceled  SubscriptionStatus = "canceled"
	StatusTrialing  SubscriptionStatus = "trialing"
	StatusPaused    SubscriptionStatus = "paused"
	StatusIncomplete SubscriptionStatus = "incomplete"
)

// Subscription represents a customer's subscription
type Subscription struct {
	ID                   uuid.UUID          `json:"id"`
	OrgID                uuid.UUID          `json:"org_id"`
	StripeCustomerID     string             `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string             `json:"stripe_subscription_id,omitempty"`
	PlanID               string             `json:"plan_id"`
	Status               SubscriptionStatus `json:"status"`
	CurrentPeriodStart   time.Time          `json:"current_period_start"`
	CurrentPeriodEnd     time.Time          `json:"current_period_end"`
	CancelAt             *time.Time         `json:"cancel_at,omitempty"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
}

// UsageType represents the type of usage being tracked
type UsageType string

const (
	UsageTypeTask   UsageType = "task"
	UsageTypeToken  UsageType = "token"
	UsageTypeCompute UsageType = "compute"
)

// UsageRecord represents a single usage event
type UsageRecord struct {
	ID                 uuid.UUID `json:"id"`
	OrgID              uuid.UUID `json:"org_id"`
	ExecutionID        *uuid.UUID `json:"execution_id,omitempty"`
	Type               UsageType `json:"type"`
	Quantity           int64     `json:"quantity"`
	UnitPriceCents     int       `json:"unit_price_cents"`
	TotalCents         int       `json:"total_cents"`
	BillingPeriodStart time.Time `json:"billing_period_start"`
	BillingPeriodEnd   time.Time `json:"billing_period_end"`
	CreatedAt          time.Time `json:"created_at"`
}

// UsageSummary provides aggregated usage for a period
type UsageSummary struct {
	OrgID              uuid.UUID `json:"org_id"`
	PeriodStart        time.Time `json:"period_start"`
	PeriodEnd          time.Time `json:"period_end"`
	TaskCount          int       `json:"task_count"`
	TaskLimit          int       `json:"task_limit"`
	TokensUsed         int64     `json:"tokens_used"`
	ComputeMinutes     int       `json:"compute_minutes"`
	TotalCostCents     int       `json:"total_cost_cents"`
	OverageCostCents   int       `json:"overage_cost_cents"`
	ProjectedCostCents int       `json:"projected_cost_cents"`
}

// Invoice represents a billing invoice
type Invoice struct {
	ID               string    `json:"id"`
	OrgID            uuid.UUID `json:"org_id"`
	StripeInvoiceID  string    `json:"stripe_invoice_id,omitempty"`
	Status           string    `json:"status"` // draft, open, paid, void, uncollectible
	AmountDueCents   int       `json:"amount_due_cents"`
	AmountPaidCents  int       `json:"amount_paid_cents"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	DueDate          time.Time `json:"due_date"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
	HostedInvoiceURL string    `json:"hosted_invoice_url,omitempty"`
	PDFUrl           string    `json:"pdf_url,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// PaymentMethod represents a stored payment method
type PaymentMethod struct {
	ID                    uuid.UUID `json:"id"`
	OrgID                 uuid.UUID `json:"org_id"`
	StripePaymentMethodID string    `json:"stripe_payment_method_id"`
	Type                  string    `json:"type"` // card, bank_account
	Last4                 string    `json:"last4"`
	Brand                 string    `json:"brand,omitempty"` // visa, mastercard, etc.
	ExpMonth              int       `json:"exp_month,omitempty"`
	ExpYear               int       `json:"exp_year,omitempty"`
	IsDefault             bool      `json:"is_default"`
	CreatedAt             time.Time `json:"created_at"`
}

// WebhookEvent represents a Stripe webhook event
type WebhookEvent struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	CreatedAt time.Time              `json:"created_at"`
}

// Plan pricing in cents
var PlanPricing = map[string]struct {
	MonthlyPrice   int
	TasksIncluded  int
	OveragePerTask int
}{
	"free":       {MonthlyPrice: 0, TasksIncluded: 10, OveragePerTask: 100},
	"pro":        {MonthlyPrice: 4900, TasksIncluded: 100, OveragePerTask: 50},
	"team":       {MonthlyPrice: 19900, TasksIncluded: 500, OveragePerTask: 40},
	"enterprise": {MonthlyPrice: -1, TasksIncluded: -1, OveragePerTask: 0}, // Custom
}

// CheckoutSession holds data for Stripe checkout
type CheckoutSession struct {
	URL       string    `json:"url"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PortalSession holds data for Stripe customer portal
type PortalSession struct {
	URL string `json:"url"`
}
