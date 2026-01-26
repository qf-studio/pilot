package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/customer"
	"github.com/stripe/stripe-go/v81/invoice"
	"github.com/stripe/stripe-go/v81/paymentmethod"
	"github.com/stripe/stripe-go/v81/subscription"
	"github.com/stripe/stripe-go/v81/webhook"
)

// Service handles billing operations
type Service struct {
	store          *Store
	stripeKey      string
	webhookSecret  string
	baseURL        string
	stripePriceIDs map[string]string // plan_id -> stripe_price_id
}

// NewService creates a new billing service
func NewService(store *Store, stripeKey, webhookSecret, baseURL string, stripePriceIDs map[string]string) *Service {
	stripe.Key = stripeKey
	return &Service{
		store:          store,
		stripeKey:      stripeKey,
		webhookSecret:  webhookSecret,
		baseURL:        baseURL,
		stripePriceIDs: stripePriceIDs,
	}
}

// CreateCheckoutSession creates a Stripe checkout session for subscription
func (s *Service) CreateCheckoutSession(ctx context.Context, orgID uuid.UUID, planID string, email string) (*CheckoutSession, error) {
	priceID, ok := s.stripePriceIDs[planID]
	if !ok {
		return nil, fmt.Errorf("unknown plan: %s", planID)
	}

	// Get or create Stripe customer
	sub, err := s.store.GetSubscription(ctx, orgID)
	var customerID string
	if err == nil && sub.StripeCustomerID != "" {
		customerID = sub.StripeCustomerID
	} else {
		// Create new customer
		params := &stripe.CustomerParams{
			Email: stripe.String(email),
			Metadata: map[string]string{
				"org_id": orgID.String(),
			},
		}
		cust, err := customer.New(params)
		if err != nil {
			return nil, fmt.Errorf("failed to create customer: %w", err)
		}
		customerID = cust.ID
	}

	// Create checkout session
	params := &stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(s.baseURL + "/billing/success?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:  stripe.String(s.baseURL + "/billing/cancel"),
		Metadata: map[string]string{
			"org_id":  orgID.String(),
			"plan_id": planID,
		},
	}

	sess, err := checkoutsession.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create checkout session: %w", err)
	}

	return &CheckoutSession{
		URL:       sess.URL,
		SessionID: sess.ID,
		ExpiresAt: time.Unix(sess.ExpiresAt, 0),
	}, nil
}

// CreatePortalSession creates a Stripe customer portal session
func (s *Service) CreatePortalSession(ctx context.Context, orgID uuid.UUID) (*PortalSession, error) {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("no subscription found: %w", err)
	}

	if sub.StripeCustomerID == "" {
		return nil, fmt.Errorf("no Stripe customer")
	}

	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(sub.StripeCustomerID),
		ReturnURL: stripe.String(s.baseURL + "/settings/billing"),
	}

	sess, err := session.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create portal session: %w", err)
	}

	return &PortalSession{URL: sess.URL}, nil
}

// HandleWebhook processes a Stripe webhook event
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	event, err := webhook.ConstructEvent(payload, signature, s.webhookSecret)
	if err != nil {
		return fmt.Errorf("invalid webhook signature: %w", err)
	}

	switch event.Type {
	case "checkout.session.completed":
		return s.handleCheckoutCompleted(ctx, event)
	case "customer.subscription.created":
		return s.handleSubscriptionCreated(ctx, event)
	case "customer.subscription.updated":
		return s.handleSubscriptionUpdated(ctx, event)
	case "customer.subscription.deleted":
		return s.handleSubscriptionDeleted(ctx, event)
	case "invoice.paid":
		return s.handleInvoicePaid(ctx, event)
	case "invoice.payment_failed":
		return s.handleInvoicePaymentFailed(ctx, event)
	}

	return nil
}

func (s *Service) handleCheckoutCompleted(ctx context.Context, event stripe.Event) error {
	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return err
	}

	orgIDStr, ok := sess.Metadata["org_id"]
	if !ok {
		return fmt.Errorf("missing org_id in metadata")
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		return fmt.Errorf("invalid org_id: %w", err)
	}

	planID := sess.Metadata["plan_id"]

	// Create or update subscription
	now := time.Now()
	sub := &Subscription{
		ID:                   uuid.New(),
		OrgID:                orgID,
		StripeCustomerID:     sess.Customer.ID,
		StripeSubscriptionID: sess.Subscription.ID,
		PlanID:               planID,
		Status:               StatusActive,
		CurrentPeriodStart:   now,
		CurrentPeriodEnd:     now.AddDate(0, 1, 0),
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	return s.store.SaveSubscription(ctx, sub)
}

func (s *Service) handleSubscriptionCreated(ctx context.Context, event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return err
	}

	// Get org_id from customer metadata
	cust, err := customer.Get(sub.Customer.ID, nil)
	if err != nil {
		return fmt.Errorf("failed to get customer: %w", err)
	}

	orgIDStr, ok := cust.Metadata["org_id"]
	if !ok {
		return nil // Not our customer
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		return nil
	}

	now := time.Now()
	subscription := &Subscription{
		ID:                   uuid.New(),
		OrgID:                orgID,
		StripeCustomerID:     sub.Customer.ID,
		StripeSubscriptionID: sub.ID,
		Status:               StatusActive,
		CurrentPeriodStart:   time.Unix(sub.CurrentPeriodStart, 0),
		CurrentPeriodEnd:     time.Unix(sub.CurrentPeriodEnd, 0),
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	return s.store.SaveSubscription(ctx, subscription)
}

func (s *Service) handleSubscriptionUpdated(ctx context.Context, event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return err
	}

	existing, err := s.store.GetSubscriptionByStripeID(ctx, sub.ID)
	if err != nil {
		return nil // Not found, ignore
	}

	existing.Status = mapStripeStatus(sub.Status)
	existing.CurrentPeriodStart = time.Unix(sub.CurrentPeriodStart, 0)
	existing.CurrentPeriodEnd = time.Unix(sub.CurrentPeriodEnd, 0)
	if sub.CancelAt > 0 {
		cancelAt := time.Unix(sub.CancelAt, 0)
		existing.CancelAt = &cancelAt
	} else {
		existing.CancelAt = nil
	}
	existing.UpdatedAt = time.Now()

	return s.store.SaveSubscription(ctx, existing)
}

func (s *Service) handleSubscriptionDeleted(ctx context.Context, event stripe.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
		return err
	}

	existing, err := s.store.GetSubscriptionByStripeID(ctx, sub.ID)
	if err != nil {
		return nil
	}

	existing.Status = StatusCanceled
	existing.UpdatedAt = time.Now()

	return s.store.SaveSubscription(ctx, existing)
}

func (s *Service) handleInvoicePaid(ctx context.Context, event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return err
	}

	existing, err := s.store.GetSubscriptionByStripeCustomer(ctx, inv.Customer.ID)
	if err != nil {
		return nil
	}

	now := time.Now()
	dbInvoice := &Invoice{
		ID:               uuid.New().String(),
		OrgID:            existing.OrgID,
		StripeInvoiceID:  inv.ID,
		Status:           "paid",
		AmountDueCents:   int(inv.AmountDue),
		AmountPaidCents:  int(inv.AmountPaid),
		PeriodStart:      time.Unix(inv.PeriodStart, 0),
		PeriodEnd:        time.Unix(inv.PeriodEnd, 0),
		DueDate:          time.Unix(inv.DueDate, 0),
		PaidAt:           &now,
		HostedInvoiceURL: inv.HostedInvoiceURL,
		PDFUrl:           inv.InvoicePDF,
		CreatedAt:        now,
	}

	return s.store.SaveInvoice(ctx, dbInvoice)
}

func (s *Service) handleInvoicePaymentFailed(ctx context.Context, event stripe.Event) error {
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return err
	}

	sub, err := s.store.GetSubscriptionByStripeCustomer(ctx, inv.Customer.ID)
	if err != nil {
		return nil
	}

	sub.Status = StatusPastDue
	sub.UpdatedAt = time.Now()

	return s.store.SaveSubscription(ctx, sub)
}

func mapStripeStatus(status stripe.SubscriptionStatus) SubscriptionStatus {
	switch status {
	case stripe.SubscriptionStatusActive:
		return StatusActive
	case stripe.SubscriptionStatusPastDue:
		return StatusPastDue
	case stripe.SubscriptionStatusCanceled:
		return StatusCanceled
	case stripe.SubscriptionStatusTrialing:
		return StatusTrialing
	case stripe.SubscriptionStatusPaused:
		return StatusPaused
	case stripe.SubscriptionStatusIncomplete:
		return StatusIncomplete
	default:
		return StatusActive
	}
}

// RecordUsage records task usage
func (s *Service) RecordUsage(ctx context.Context, orgID uuid.UUID, executionID *uuid.UUID, usageType UsageType, quantity int64) error {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return fmt.Errorf("no subscription: %w", err)
	}

	pricing, ok := PlanPricing[sub.PlanID]
	if !ok {
		pricing = PlanPricing["free"]
	}

	var unitPrice int
	switch usageType {
	case UsageTypeTask:
		// Check if over limit
		currentUsage, err := s.store.GetUsageCount(ctx, orgID, UsageTypeTask, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
		if err != nil {
			return err
		}
		if pricing.TasksIncluded >= 0 && currentUsage >= pricing.TasksIncluded {
			unitPrice = pricing.OveragePerTask
		}
	case UsageTypeToken:
		unitPrice = 0 // Included in task cost
	case UsageTypeCompute:
		unitPrice = 1 // $0.01 per minute
	}

	record := &UsageRecord{
		ID:                 uuid.New(),
		OrgID:              orgID,
		ExecutionID:        executionID,
		Type:               usageType,
		Quantity:           quantity,
		UnitPriceCents:     unitPrice,
		TotalCents:         int(quantity) * unitPrice,
		BillingPeriodStart: sub.CurrentPeriodStart,
		BillingPeriodEnd:   sub.CurrentPeriodEnd,
		CreatedAt:          time.Now(),
	}

	return s.store.SaveUsageRecord(ctx, record)
}

// GetUsageSummary returns usage summary for current period
func (s *Service) GetUsageSummary(ctx context.Context, orgID uuid.UUID) (*UsageSummary, error) {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		// Return free tier defaults
		now := time.Now()
		return &UsageSummary{
			OrgID:       orgID,
			PeriodStart: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
			PeriodEnd:   time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC),
			TaskLimit:   10,
		}, nil
	}

	pricing, ok := PlanPricing[sub.PlanID]
	if !ok {
		pricing = PlanPricing["free"]
	}

	taskCount, err := s.store.GetUsageCount(ctx, orgID, UsageTypeTask, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return nil, err
	}

	tokens, err := s.store.GetUsageSum(ctx, orgID, UsageTypeToken, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return nil, err
	}

	compute, err := s.store.GetUsageSum(ctx, orgID, UsageTypeCompute, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return nil, err
	}

	totalCost, err := s.store.GetUsageCost(ctx, orgID, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return nil, err
	}

	overageCost := 0
	if pricing.TasksIncluded >= 0 && taskCount > pricing.TasksIncluded {
		overageCost = (taskCount - pricing.TasksIncluded) * pricing.OveragePerTask
	}

	// Project cost for rest of period
	daysElapsed := time.Since(sub.CurrentPeriodStart).Hours() / 24
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	daysInPeriod := sub.CurrentPeriodEnd.Sub(sub.CurrentPeriodStart).Hours() / 24
	projectedCost := int(float64(totalCost) / daysElapsed * daysInPeriod)

	return &UsageSummary{
		OrgID:              orgID,
		PeriodStart:        sub.CurrentPeriodStart,
		PeriodEnd:          sub.CurrentPeriodEnd,
		TaskCount:          taskCount,
		TaskLimit:          pricing.TasksIncluded,
		TokensUsed:         tokens,
		ComputeMinutes:     int(compute),
		TotalCostCents:     totalCost,
		OverageCostCents:   overageCost,
		ProjectedCostCents: projectedCost,
	}, nil
}

// GetInvoices returns invoices for an organization
func (s *Service) GetInvoices(ctx context.Context, orgID uuid.UUID, limit int) ([]*Invoice, error) {
	return s.store.ListInvoices(ctx, orgID, limit)
}

// GetPaymentMethods returns payment methods for an organization
func (s *Service) GetPaymentMethods(ctx context.Context, orgID uuid.UUID) ([]*PaymentMethod, error) {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return nil, err
	}

	if sub.StripeCustomerID == "" {
		return []*PaymentMethod{}, nil
	}

	params := &stripe.PaymentMethodListParams{
		Customer: stripe.String(sub.StripeCustomerID),
		Type:     stripe.String("card"),
	}

	var methods []*PaymentMethod
	i := paymentmethod.List(params)
	for i.Next() {
		pm := i.PaymentMethod()
		method := &PaymentMethod{
			ID:                    uuid.New(),
			OrgID:                 orgID,
			StripePaymentMethodID: pm.ID,
			Type:                  string(pm.Type),
			CreatedAt:             time.Now(),
		}

		if pm.Card != nil {
			method.Last4 = pm.Card.Last4
			method.Brand = string(pm.Card.Brand)
			method.ExpMonth = int(pm.Card.ExpMonth)
			method.ExpYear = int(pm.Card.ExpYear)
		}

		methods = append(methods, method)
	}

	return methods, nil
}

// CancelSubscription cancels the subscription at period end
func (s *Service) CancelSubscription(ctx context.Context, orgID uuid.UUID) error {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return err
	}

	if sub.StripeSubscriptionID == "" {
		return fmt.Errorf("no active subscription")
	}

	params := &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(true),
	}

	_, err = subscription.Update(sub.StripeSubscriptionID, params)
	if err != nil {
		return fmt.Errorf("failed to cancel subscription: %w", err)
	}

	return nil
}

// ChangePlan changes the subscription plan
func (s *Service) ChangePlan(ctx context.Context, orgID uuid.UUID, newPlanID string) error {
	priceID, ok := s.stripePriceIDs[newPlanID]
	if !ok {
		return fmt.Errorf("unknown plan: %s", newPlanID)
	}

	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return err
	}

	if sub.StripeSubscriptionID == "" {
		return fmt.Errorf("no active subscription")
	}

	// Get current subscription items
	stripeSub, err := subscription.Get(sub.StripeSubscriptionID, nil)
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	if len(stripeSub.Items.Data) == 0 {
		return fmt.Errorf("no subscription items")
	}

	params := &stripe.SubscriptionParams{
		Items: []*stripe.SubscriptionItemsParams{
			{
				ID:    stripe.String(stripeSub.Items.Data[0].ID),
				Price: stripe.String(priceID),
			},
		},
		ProrationBehavior: stripe.String(string(stripe.SubscriptionProrationBehaviorCreateProrations)),
	}

	_, err = subscription.Update(sub.StripeSubscriptionID, params)
	if err != nil {
		return fmt.Errorf("failed to change plan: %w", err)
	}

	sub.PlanID = newPlanID
	sub.UpdatedAt = time.Now()
	return s.store.SaveSubscription(ctx, sub)
}

// CheckQuota checks if organization has remaining quota
func (s *Service) CheckQuota(ctx context.Context, orgID uuid.UUID) (bool, error) {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		// No subscription, use free tier
		now := time.Now()
		periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd := periodStart.AddDate(0, 1, 0)
		taskCount, err := s.store.GetUsageCount(ctx, orgID, UsageTypeTask, periodStart, periodEnd)
		if err != nil {
			return false, err
		}
		return taskCount < 10, nil // Free tier limit
	}

	pricing, ok := PlanPricing[sub.PlanID]
	if !ok {
		pricing = PlanPricing["free"]
	}

	// Unlimited for paid plans with overage
	if pricing.TasksIncluded < 0 || pricing.OveragePerTask > 0 {
		return true, nil
	}

	taskCount, err := s.store.GetUsageCount(ctx, orgID, UsageTypeTask, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return false, err
	}

	return taskCount < pricing.TasksIncluded, nil
}

// ReportUsageToStripe reports metered usage to Stripe
func (s *Service) ReportUsageToStripe(ctx context.Context, orgID uuid.UUID) error {
	sub, err := s.store.GetSubscription(ctx, orgID)
	if err != nil {
		return err
	}

	if sub.StripeSubscriptionID == "" {
		return nil
	}

	// Get usage for current period
	taskCount, err := s.store.GetUsageCount(ctx, orgID, UsageTypeTask, sub.CurrentPeriodStart, sub.CurrentPeriodEnd)
	if err != nil {
		return err
	}

	pricing := PlanPricing[sub.PlanID]
	if pricing.TasksIncluded >= 0 && taskCount > pricing.TasksIncluded {
		// Report overage
		overage := taskCount - pricing.TasksIncluded

		// Get subscription item for metered billing
		stripeSub, err := subscription.Get(sub.StripeSubscriptionID, nil)
		if err != nil {
			return err
		}

		// Find metered item (if exists)
		for _, item := range stripeSub.Items.Data {
			if item.Price.Recurring != nil && item.Price.Recurring.UsageType == stripe.PriceRecurringUsageTypeMetered {
				// Report usage
				params := &stripe.UsageRecordParams{
					SubscriptionItem: stripe.String(item.ID),
					Quantity:         stripe.Int64(int64(overage)),
					Timestamp:        stripe.Int64(time.Now().Unix()),
					Action:           stripe.String(string(stripe.UsageRecordActionSet)),
				}

				// Note: stripe.UsageRecord API would be called here
				_ = params
			}
		}
	}

	return nil
}
