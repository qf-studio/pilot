package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

// Store provides billing data access
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new billing store
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// --- Subscription Operations ---

// SaveSubscription saves or updates a subscription
func (s *Store) SaveSubscription(ctx context.Context, sub *Subscription) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subscriptions (id, org_id, stripe_customer_id, stripe_subscription_id, plan_id, status,
		                           current_period_start, current_period_end, cancel_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (org_id) DO UPDATE SET
			stripe_customer_id = EXCLUDED.stripe_customer_id,
			stripe_subscription_id = EXCLUDED.stripe_subscription_id,
			plan_id = EXCLUDED.plan_id,
			status = EXCLUDED.status,
			current_period_start = EXCLUDED.current_period_start,
			current_period_end = EXCLUDED.current_period_end,
			cancel_at = EXCLUDED.cancel_at,
			updated_at = NOW()
	`, sub.ID, sub.OrgID, sub.StripeCustomerID, sub.StripeSubscriptionID, sub.PlanID, sub.Status,
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd, sub.CancelAt, sub.CreatedAt, sub.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to save subscription: %w", err)
	}
	return nil
}

// GetSubscription retrieves a subscription by org ID
func (s *Store) GetSubscription(ctx context.Context, orgID uuid.UUID) (*Subscription, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, stripe_customer_id, stripe_subscription_id, plan_id, status,
		       current_period_start, current_period_end, cancel_at, created_at, updated_at
		FROM subscriptions WHERE org_id = $1
	`, orgID)

	return s.scanSubscription(row)
}

// GetSubscriptionByStripeID retrieves a subscription by Stripe subscription ID
func (s *Store) GetSubscriptionByStripeID(ctx context.Context, stripeSubID string) (*Subscription, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, stripe_customer_id, stripe_subscription_id, plan_id, status,
		       current_period_start, current_period_end, cancel_at, created_at, updated_at
		FROM subscriptions WHERE stripe_subscription_id = $1
	`, stripeSubID)

	return s.scanSubscription(row)
}

// GetSubscriptionByStripeCustomer retrieves a subscription by Stripe customer ID
func (s *Store) GetSubscriptionByStripeCustomer(ctx context.Context, stripeCustomerID string) (*Subscription, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, stripe_customer_id, stripe_subscription_id, plan_id, status,
		       current_period_start, current_period_end, cancel_at, created_at, updated_at
		FROM subscriptions WHERE stripe_customer_id = $1
	`, stripeCustomerID)

	return s.scanSubscription(row)
}

func (s *Store) scanSubscription(row pgx.Row) (*Subscription, error) {
	var sub Subscription
	var stripeCustomerID, stripeSubID *string
	var cancelAt *time.Time

	err := row.Scan(&sub.ID, &sub.OrgID, &stripeCustomerID, &stripeSubID, &sub.PlanID, &sub.Status,
		&sub.CurrentPeriodStart, &sub.CurrentPeriodEnd, &cancelAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if stripeCustomerID != nil {
		sub.StripeCustomerID = *stripeCustomerID
	}
	if stripeSubID != nil {
		sub.StripeSubscriptionID = *stripeSubID
	}
	sub.CancelAt = cancelAt

	return &sub, nil
}

// --- Usage Operations ---

// SaveUsageRecord saves a usage record
func (s *Store) SaveUsageRecord(ctx context.Context, record *UsageRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_records (id, org_id, execution_id, type, quantity, unit_price_cents, total_cents,
		                           billing_period_start, billing_period_end, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, record.ID, record.OrgID, record.ExecutionID, record.Type, record.Quantity, record.UnitPriceCents,
		record.TotalCents, record.BillingPeriodStart, record.BillingPeriodEnd, record.CreatedAt)

	if err != nil {
		return fmt.Errorf("failed to save usage record: %w", err)
	}
	return nil
}

// GetUsageCount returns the count of usage records
func (s *Store) GetUsageCount(ctx context.Context, orgID uuid.UUID, usageType UsageType, start, end time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity), 0)
		FROM usage_records
		WHERE org_id = $1 AND type = $2 AND billing_period_start >= $3 AND billing_period_end <= $4
	`, orgID, usageType, start, end).Scan(&count)
	return count, err
}

// GetUsageSum returns the sum of quantity for a usage type
func (s *Store) GetUsageSum(ctx context.Context, orgID uuid.UUID, usageType UsageType, start, end time.Time) (int64, error) {
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity), 0)
		FROM usage_records
		WHERE org_id = $1 AND type = $2 AND billing_period_start >= $3 AND billing_period_end <= $4
	`, orgID, usageType, start, end).Scan(&sum)
	return sum, err
}

// GetUsageCost returns the total cost for a period
func (s *Store) GetUsageCost(ctx context.Context, orgID uuid.UUID, start, end time.Time) (int, error) {
	var cost int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(total_cents), 0)
		FROM usage_records
		WHERE org_id = $1 AND billing_period_start >= $2 AND billing_period_end <= $3
	`, orgID, start, end).Scan(&cost)
	return cost, err
}

// ListUsageRecords returns usage records for an organization
func (s *Store) ListUsageRecords(ctx context.Context, orgID uuid.UUID, start, end time.Time, limit, offset int) ([]*UsageRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, execution_id, type, quantity, unit_price_cents, total_cents,
		       billing_period_start, billing_period_end, created_at
		FROM usage_records
		WHERE org_id = $1 AND created_at >= $2 AND created_at < $3
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5
	`, orgID, start, end, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*UsageRecord
	for rows.Next() {
		var r UsageRecord
		var executionID *uuid.UUID

		if err := rows.Scan(&r.ID, &r.OrgID, &executionID, &r.Type, &r.Quantity, &r.UnitPriceCents,
			&r.TotalCents, &r.BillingPeriodStart, &r.BillingPeriodEnd, &r.CreatedAt); err != nil {
			return nil, err
		}

		r.ExecutionID = executionID
		records = append(records, &r)
	}

	return records, nil
}

// --- Invoice Operations ---

// SaveInvoice saves an invoice
func (s *Store) SaveInvoice(ctx context.Context, inv *Invoice) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO invoices (id, org_id, stripe_invoice_id, status, amount_due_cents, amount_paid_cents,
		                      period_start, period_end, due_date, paid_at, hosted_invoice_url, pdf_url, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			amount_paid_cents = EXCLUDED.amount_paid_cents,
			paid_at = EXCLUDED.paid_at
	`, inv.ID, inv.OrgID, inv.StripeInvoiceID, inv.Status, inv.AmountDueCents, inv.AmountPaidCents,
		inv.PeriodStart, inv.PeriodEnd, inv.DueDate, inv.PaidAt, inv.HostedInvoiceURL, inv.PDFUrl, inv.CreatedAt)

	return err
}

// ListInvoices returns invoices for an organization
func (s *Store) ListInvoices(ctx context.Context, orgID uuid.UUID, limit int) ([]*Invoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, stripe_invoice_id, status, amount_due_cents, amount_paid_cents,
		       period_start, period_end, due_date, paid_at, hosted_invoice_url, pdf_url, created_at
		FROM invoices
		WHERE org_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invoices []*Invoice
	for rows.Next() {
		var inv Invoice
		var stripeID *string
		var paidAt *time.Time
		var hostedURL, pdfURL *string

		if err := rows.Scan(&inv.ID, &inv.OrgID, &stripeID, &inv.Status, &inv.AmountDueCents, &inv.AmountPaidCents,
			&inv.PeriodStart, &inv.PeriodEnd, &inv.DueDate, &paidAt, &hostedURL, &pdfURL, &inv.CreatedAt); err != nil {
			return nil, err
		}

		if stripeID != nil {
			inv.StripeInvoiceID = *stripeID
		}
		inv.PaidAt = paidAt
		if hostedURL != nil {
			inv.HostedInvoiceURL = *hostedURL
		}
		if pdfURL != nil {
			inv.PDFUrl = *pdfURL
		}
		invoices = append(invoices, &inv)
	}

	return invoices, nil
}

// --- Aggregate Queries ---

// GetMonthlyUsageByOrg returns monthly usage aggregates for all orgs
func (s *Store) GetMonthlyUsageByOrg(ctx context.Context, year, month int) (map[uuid.UUID]*MonthlyUsage, error) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	rows, err := s.pool.Query(ctx, `
		SELECT org_id, type, SUM(quantity) as total_quantity, SUM(total_cents) as total_cost
		FROM usage_records
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY org_id, type
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]*MonthlyUsage)
	for rows.Next() {
		var orgID uuid.UUID
		var usageType UsageType
		var quantity int64
		var cost int

		if err := rows.Scan(&orgID, &usageType, &quantity, &cost); err != nil {
			return nil, err
		}

		if _, ok := result[orgID]; !ok {
			result[orgID] = &MonthlyUsage{
				OrgID: orgID,
				Year:  year,
				Month: month,
			}
		}

		switch usageType {
		case UsageTypeTask:
			result[orgID].TaskCount = int(quantity)
		case UsageTypeToken:
			result[orgID].TokensUsed = quantity
		case UsageTypeCompute:
			result[orgID].ComputeMinutes = int(quantity)
		}
		result[orgID].TotalCostCents += cost
	}

	return result, nil
}

// MonthlyUsage holds monthly aggregates
type MonthlyUsage struct {
	OrgID          uuid.UUID
	Year           int
	Month          int
	TaskCount      int
	TokensUsed     int64
	ComputeMinutes int
	TotalCostCents int
}
