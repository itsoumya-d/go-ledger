package main

import (
	"errors"
	"testing"
)

func mustInvoice(
	t *testing.T,
	service *RatingService,
	key, account, plan, start, end string,
	usage int64,
	activeDays int,
) *RatedInvoice {
	t.Helper()
	invoice, created, err := service.CreateInvoice(key, account, plan, start, end, usage, activeDays)
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if !created {
		t.Fatalf("expected a newly created invoice")
	}
	return invoice
}

func TestRatedInvoiceTieredUsage(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-1", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)

	if invoice.BaseCents != 4900 {
		t.Fatalf("base = %d, want 4900", invoice.BaseCents)
	}
	if invoice.UsageCents != 3800 {
		t.Fatalf("usage = %d, want 3800", invoice.UsageCents)
	}
	if invoice.SubtotalCents != 8700 {
		t.Fatalf("subtotal = %d, want 8700", invoice.SubtotalCents)
	}
	if invoice.TaxCents != 1305 {
		t.Fatalf("tax = %d, want 1305", invoice.TaxCents)
	}
	if invoice.TotalCents != 10005 {
		t.Fatalf("total = %d, want 10005", invoice.TotalCents)
	}
	if len(invoice.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(invoice.Lines))
	}
}

func TestRatedInvoiceUsageInsideIncludedAllowance(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-2", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 80, 30)

	if invoice.UsageCents != 0 {
		t.Fatalf("usage = %d, want 0", invoice.UsageCents)
	}
	if len(invoice.Lines) != 1 {
		t.Fatalf("lines = %d, want only the base line", len(invoice.Lines))
	}
}

func TestRatedInvoiceTierBoundary(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-3", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 600, 30)

	if invoice.UsageCents != 2000 {
		t.Fatalf("usage = %d, want 2000 at the tier boundary", invoice.UsageCents)
	}
	if len(invoice.Lines) != 2 {
		t.Fatalf("lines = %d, want base + first tier", len(invoice.Lines))
	}
}

func TestRatedInvoiceUnlimitedFinalTier(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-4", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 3000, 30)

	if invoice.UsageCents != 8300 {
		t.Fatalf("usage = %d, want 8300", invoice.UsageCents)
	}
}

func TestRatedInvoiceProration(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-5", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 0, 15)

	if invoice.BaseCents != 2450 {
		t.Fatalf("base = %d, want 2450 for 15 of 30 days", invoice.BaseCents)
	}
}

func TestRatedInvoiceTaxRoundsToNearestCent(t *testing.T) {
	service := NewRatingService()
	invoice := mustInvoice(t, service, "inv-key-6", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 101, 30)

	if invoice.SubtotalCents != 4904 {
		t.Fatalf("subtotal = %d, want 4904", invoice.SubtotalCents)
	}
	if invoice.TaxCents != 736 {
		t.Fatalf("tax = %d, want 736", invoice.TaxCents)
	}
	if invoice.TotalCents != 5640 {
		t.Fatalf("total = %d, want 5640", invoice.TotalCents)
	}
}

func TestRatedInvoiceIdempotency(t *testing.T) {
	service := NewRatingService()
	first := mustInvoice(t, service, "same-key", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)
	second, created, err := service.CreateInvoice("same-key", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)
	if err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if created {
		t.Fatalf("expected replay to return the original invoice")
	}
	if second.ID != first.ID {
		t.Fatalf("id = %s, want %s", second.ID, first.ID)
	}
}

func TestRatedInvoiceIdempotencyConflict(t *testing.T) {
	service := NewRatingService()
	_ = mustInvoice(t, service, "conflict-key", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)
	_, _, err := service.CreateInvoice("conflict-key", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1201, 30)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRatedInvoiceValidation(t *testing.T) {
	service := NewRatingService()
	cases := []struct {
		name       string
		key        string
		account    string
		plan       string
		start      string
		end        string
		usage      int64
		activeDays int
	}{
		{"missing key", "", "acct", "wholesale_100", "2026-11-01", "2026-11-30", 1, 30},
		{"missing account", "k1", "", "wholesale_100", "2026-11-01", "2026-11-30", 1, 30},
		{"negative usage", "k2", "acct", "wholesale_100", "2026-11-01", "2026-11-30", -1, 30},
		{"bad start", "k3", "acct", "wholesale_100", "11/01/2026", "2026-11-30", 1, 30},
		{"bad end", "k4", "acct", "wholesale_100", "2026-11-01", "2026-13-30", 1, 30},
		{"end before start", "k5", "acct", "wholesale_100", "2026-11-30", "2026-11-01", 1, 30},
		{"unknown plan", "k6", "acct", "missing", "2026-11-01", "2026-11-30", 1, 30},
		{"zero active days", "k7", "acct", "wholesale_100", "2026-11-01", "2026-11-30", 1, 0},
		{"too many active days", "k8", "acct", "wholesale_100", "2026-11-01", "2026-11-30", 1, 31},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := service.CreateInvoice(tc.key, tc.account, tc.plan, tc.start, tc.end, tc.usage, tc.activeDays)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCaptureInvoicePostsToLedger(t *testing.T) {
	service := NewRatingService()
	store := NewStore()
	_ = store.Fund("acct_a", 20_000)
	invoice := mustInvoice(t, service, "capture-key", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)

	captured, err := service.CaptureInvoice(invoice.ID, store)
	if err != nil {
		t.Fatalf("CaptureInvoice: %v", err)
	}
	if captured.Status != "posted" {
		t.Fatalf("status = %s, want posted", captured.Status)
	}
	if captured.IntentID == "" {
		t.Fatalf("intent id is empty")
	}
	if got := store.Balance("acct_a"); got != 20_000-invoice.TotalCents {
		t.Fatalf("balance = %d, want %d", got, 20_000-invoice.TotalCents)
	}
	if got := len(store.Postings()); got != 2 {
		t.Fatalf("postings = %d, want 2", got)
	}
}

func TestCaptureInvoiceRejectsInsufficientFunds(t *testing.T) {
	service := NewRatingService()
	store := NewStore()
	invoice := mustInvoice(t, service, "insufficient-key", "acct_b", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)

	_, err := service.CaptureInvoice(invoice.ID, store)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
}

func TestCaptureInvoiceIsIdempotent(t *testing.T) {
	service := NewRatingService()
	store := NewStore()
	_ = store.Fund("acct_a", 20_000)
	invoice := mustInvoice(t, service, "capture-twice", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 1200, 30)
	_, _ = service.CaptureInvoice(invoice.ID, store)

	captured, err := service.CaptureInvoice(invoice.ID, store)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if captured.Status != "posted" {
		t.Fatalf("status = %s, want posted", captured.Status)
	}
	if got := len(store.Postings()); got != 2 {
		t.Fatalf("postings = %d, want 2", got)
	}
}

func TestInvoicesFilterByAccount(t *testing.T) {
	service := NewRatingService()
	_ = mustInvoice(t, service, "a1", "acct_a", "wholesale_100", "2026-11-01", "2026-11-30", 10, 30)
	_ = mustInvoice(t, service, "b1", "acct_b", "voice_seat", "2026-11-01", "2026-11-30", 10, 30)

	if got := len(service.Invoices("acct_a")); got != 1 {
		t.Fatalf("acct_a invoices = %d, want 1", got)
	}
	if got := len(service.Invoices("")); got != 2 {
		t.Fatalf("all invoices = %d, want 2", got)
	}
}

func TestInvoiceLookupReturnsNotFound(t *testing.T) {
	service := NewRatingService()
	if _, ok := service.Invoice("missing"); ok {
		t.Fatalf("expected missing invoice to be absent")
	}
}

func TestPlanCatalog(t *testing.T) {
	service := NewRatingService()
	plans := service.Plans()
	if len(plans) != 3 {
		t.Fatalf("plans = %d, want 3", len(plans))
	}
	if plans[0].ID != "enterprise_5000" {
		t.Fatalf("first plan = %s, want sorted catalog", plans[0].ID)
	}
}
