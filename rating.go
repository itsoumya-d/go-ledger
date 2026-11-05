package main

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// PlanTier is a marginal usage band. UpToUnits == 0 means "no upper bound"
// and must be the final tier.
type PlanTier struct {
	UpToUnits      int64 `json:"up_to_units"`
	UnitPriceCents int64 `json:"unit_price_cents"`
}

type Plan struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	BaseMonthlyCents int64      `json:"base_monthly_cents"`
	IncludedUnits    int64      `json:"included_units"`
	Tiers            []PlanTier `json:"tiers"`
	TaxRateBps       int64      `json:"tax_rate_bps"`
	Currency         string     `json:"currency"`
}

type InvoiceLine struct {
	Description    string `json:"description"`
	Units          int64  `json:"units"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	AmountCents    int64  `json:"amount_cents"`
}

type RatedInvoice struct {
	ID             string        `json:"id"`
	AccountID      string        `json:"account_id"`
	PlanID         string        `json:"plan_id"`
	PeriodStart    string        `json:"period_start"`
	PeriodEnd      string        `json:"period_end"`
	UsageUnits     int64         `json:"usage_units"`
	ActiveDays     int           `json:"active_days"`
	DaysInMonth    int           `json:"days_in_month"`
	BaseCents      int64         `json:"base_cents"`
	UsageCents     int64         `json:"usage_cents"`
	SubtotalCents  int64         `json:"subtotal_cents"`
	TaxCents       int64         `json:"tax_cents"`
	TotalCents     int64         `json:"total_cents"`
	Currency       string        `json:"currency"`
	Status         string        `json:"status"`
	IntentID       string        `json:"intent_id,omitempty"`
	IdempotencyKey string        `json:"-"`
	CreatedAt      time.Time     `json:"created_at"`
	Lines          []InvoiceLine `json:"lines"`
}

type RatingService struct {
	mu       sync.Mutex
	plans    map[string]Plan
	invoices map[string]*RatedInvoice
	byKey    map[string]string
	seq      int64
}

func NewRatingService() *RatingService {
	plans := []Plan{
		{
			ID:               "wholesale_100",
			Name:             "Wholesale Broadband 100",
			BaseMonthlyCents: 4900,
			IncludedUnits:    100,
			Tiers: []PlanTier{
				{UpToUnits: 500, UnitPriceCents: 4},
				{UpToUnits: 2000, UnitPriceCents: 3},
				{UpToUnits: 0, UnitPriceCents: 2},
			},
			TaxRateBps: 1500,
			Currency:   "NZD",
		},
		{
			ID:               "voice_seat",
			Name:             "Voice Seat",
			BaseMonthlyCents: 2500,
			IncludedUnits:    0,
			Tiers: []PlanTier{
				{UpToUnits: 1000, UnitPriceCents: 3},
				{UpToUnits: 0, UnitPriceCents: 2},
			},
			TaxRateBps: 1500,
			Currency:   "NZD",
		},
		{
			ID:               "enterprise_5000",
			Name:             "Enterprise Connectivity 5000",
			BaseMonthlyCents: 49900,
			IncludedUnits:    5000,
			Tiers: []PlanTier{
				{UpToUnits: 10000, UnitPriceCents: 1},
				{UpToUnits: 0, UnitPriceCents: 1},
			},
			TaxRateBps: 1500,
			Currency:   "NZD",
		},
	}
	byID := make(map[string]Plan, len(plans))
	for _, plan := range plans {
		byID[plan.ID] = plan
	}
	return &RatingService{
		plans:    byID,
		invoices: make(map[string]*RatedInvoice),
		byKey:    make(map[string]string),
	}
}

func (s *RatingService) Plans() []Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Plan, 0, len(s.plans))
	for _, plan := range s.plans {
		out = append(out, plan)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *RatingService) CreateInvoice(
	key, accountID, planID, periodStart, periodEnd string,
	usageUnits int64,
	activeDays int,
) (*RatedInvoice, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("%w: Idempotency-Key is required", ErrInvalid)
	}
	if accountID == "" {
		return nil, false, fmt.Errorf("%w: account_id is required", ErrInvalid)
	}
	if usageUnits < 0 {
		return nil, false, fmt.Errorf("%w: usage_units cannot be negative", ErrInvalid)
	}

	start, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		return nil, false, fmt.Errorf("%w: period_start must be YYYY-MM-DD", ErrInvalid)
	}
	end, err := time.Parse("2006-01-02", periodEnd)
	if err != nil {
		return nil, false, fmt.Errorf("%w: period_end must be YYYY-MM-DD", ErrInvalid)
	}
	if end.Before(start) {
		return nil, false, fmt.Errorf("%w: period_end must be on or after period_start", ErrInvalid)
	}

	daysInMonth := time.Date(start.Year(), start.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if activeDays <= 0 || activeDays > daysInMonth {
		return nil, false, fmt.Errorf("%w: active_days must be between 1 and %d", ErrInvalid, daysInMonth)
	}

	plan, ok := s.plans[planID]
	if !ok {
		return nil, false, fmt.Errorf("%w: plan_id is unknown", ErrInvalid)
	}

	fingerprint := fmt.Sprintf("%s|%s|%s|%s|%d|%d", accountID, planID, periodStart, periodEnd, usageUnits, activeDays)

	s.mu.Lock()
	defer s.mu.Unlock()

	if existingID, ok := s.byKey[key]; ok {
		existing := s.invoices[existingID]
		if existing != nil && existing.IdempotencyKey == key && existing.fingerprint() == fingerprint {
			return existing, false, nil
		}
		return nil, false, ErrIdempotencyConflict
	}

	invoice := rateInvoice(plan, accountID, periodStart, periodEnd, usageUnits, activeDays, daysInMonth)
	s.seq++
	invoice.ID = fmt.Sprintf("inv_%d_%d", time.Now().UnixNano(), s.seq)
	invoice.IdempotencyKey = key
	invoice.Status = "draft"
	invoice.CreatedAt = time.Now().UTC()
	s.invoices[invoice.ID] = invoice
	s.byKey[key] = invoice.ID
	return invoice, true, nil
}

func rateInvoice(
	plan Plan,
	accountID, periodStart, periodEnd string,
	usageUnits int64,
	activeDays, daysInMonth int,
) *RatedInvoice {
	proratedBase := (plan.BaseMonthlyCents*int64(activeDays) + int64(daysInMonth)/2) / int64(daysInMonth)
	billable := usageUnits - plan.IncludedUnits
	if billable < 0 {
		billable = 0
	}

	lines := []InvoiceLine{{
		Description:    "Monthly platform fee (prorated)",
		Units:          int64(activeDays),
		UnitPriceCents: 0,
		AmountCents:    proratedBase,
	}}

	var usageCents int64
	remaining := billable
	previous := int64(0)
	for _, tier := range plan.Tiers {
		if remaining <= 0 {
			break
		}
		capacity := tier.UpToUnits - previous
		if tier.UpToUnits == 0 || capacity > remaining {
			capacity = remaining
		}
		if capacity > 0 {
			amount := capacity * tier.UnitPriceCents
			usageCents += amount
			lines = append(lines, InvoiceLine{
				Description:    tierDescription(tier, previous),
				Units:          capacity,
				UnitPriceCents: tier.UnitPriceCents,
				AmountCents:    amount,
			})
			remaining -= capacity
		}
		if tier.UpToUnits == 0 {
			break
		}
		previous = tier.UpToUnits
	}

	subtotal := proratedBase + usageCents
	tax := (subtotal*plan.TaxRateBps + 5000) / 10000
	return &RatedInvoice{
		AccountID:     accountID,
		PlanID:        plan.ID,
		PeriodStart:   periodStart,
		PeriodEnd:     periodEnd,
		UsageUnits:    usageUnits,
		ActiveDays:    activeDays,
		DaysInMonth:   daysInMonth,
		BaseCents:     proratedBase,
		UsageCents:    usageCents,
		SubtotalCents: subtotal,
		TaxCents:      tax,
		TotalCents:    subtotal + tax,
		Currency:      plan.Currency,
		Lines:         lines,
	}
}

func tierDescription(tier PlanTier, previous int64) string {
	if tier.UpToUnits == 0 {
		return "Usage above included allowance (unlimited tier)"
	}
	return fmt.Sprintf("Usage tier %d-%d units", previous+1, tier.UpToUnits)
}

func (i *RatedInvoice) fingerprint() string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d",
		i.AccountID, i.PlanID, i.PeriodStart, i.PeriodEnd, i.UsageUnits, i.ActiveDays)
}

func (s *RatingService) Invoice(id string) (*RatedInvoice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invoice, ok := s.invoices[id]
	return invoice, ok
}

func (s *RatingService) Invoices(accountID string) []*RatedInvoice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*RatedInvoice, 0)
	for _, invoice := range s.invoices {
		if accountID == "" || invoice.AccountID == accountID {
			out = append(out, invoice)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// CaptureInvoice posts the invoice total to the double-entry ledger exactly once.
func (s *RatingService) CaptureInvoice(id string, store *Store) (*RatedInvoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	invoice, ok := s.invoices[id]
	if !ok {
		return nil, fmt.Errorf("%w: rated invoice", ErrNotFound)
	}
	if invoice.Status == "posted" {
		return invoice, nil
	}

	intent, _, err := store.CreateIntent("invoice:"+invoice.ID, invoice.AccountID, invoice.TotalCents, invoice.Currency)
	if err != nil {
		return nil, err
	}
	captured, err := store.Capture(intent.ID)
	if err != nil {
		return nil, err
	}
	if captured.Status != "captured" {
		return nil, errors.New("invoice capture did not settle")
	}
	invoice.Status = "posted"
	invoice.IntentID = captured.ID
	return invoice, nil
}
