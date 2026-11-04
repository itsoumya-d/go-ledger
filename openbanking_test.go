package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenBankingConsentAndIdempotentPayment(t *testing.T) {
	store := NewStore()
	if err := store.Fund("acct_1", 1000); err != nil {
		t.Fatal(err)
	}
	svc := NewOpenBankingService(store, NewMemoryBus(8), nil)
	handler := openBankingHandler(svc)

	consentBody, _ := json.Marshal(map[string]any{
		"account_id":  "acct_1",
		"permissions": []string{"ReadAccounts", "WritePayments"},
	})
	req := httptest.NewRequest(http.MethodPost, "/openbanking/v1/consents", bytes.NewReader(consentBody))
	req.Header.Set("Authorization", "Bearer dev-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("consent status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var consent Consent
	if err := json.Unmarshal(rec.Body.Bytes(), &consent); err != nil {
		t.Fatal(err)
	}

	paymentBody, _ := json.Marshal(map[string]any{
		"consent_id":   consent.ID,
		"account_id":   "acct_1",
		"amount_cents": 400,
		"currency":     "USD",
	})
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/openbanking/v1/payments", bytes.NewReader(paymentBody))
		req.Header.Set("Authorization", "Bearer dev-token")
		req.Header.Set("Idempotency-Key", "pay-42")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := send()
	if first.Code != http.StatusCreated {
		t.Fatalf("first payment status = %d, body = %s", first.Code, first.Body.String())
	}
	second := send()
	if second.Code != http.StatusOK {
		t.Fatalf("replayed payment status = %d, body = %s", second.Code, second.Body.String())
	}
	if got := store.Balance("acct_1"); got != 600 {
		t.Fatalf("balance = %d, want 600", got)
	}
}

func TestOpenBankingRejectsMissingBearerToken(t *testing.T) {
	store := NewStore()
	svc := NewOpenBankingService(store, NewMemoryBus(4), nil)
	handler := openBankingHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/openbanking/v1/accounts/acct_1/balance", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestOpenBankingConflictsOnReusedKeyWithDifferentPayload(t *testing.T) {
	store := NewStore()
	_ = store.Fund("acct_1", 1000)
	svc := NewOpenBankingService(store, NewMemoryBus(4), nil)
	consent, err := svc.CreateConsent("acct_1", []string{"WritePayments"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreatePayment("pay-7", consent.ID, "acct_1", 100, "USD"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreatePayment("pay-7", consent.ID, "acct_1", 200, "USD"); err != ErrIdempotencyConflict {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
}
