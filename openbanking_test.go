package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestOpenBankingGetConsentPaymentAndBalance(t *testing.T) {
	store := NewStore()
	if err := store.Fund("acct_get", 1000); err != nil {
		t.Fatal(err)
	}
	svc := NewOpenBankingService(store, NewMemoryBus(4), nil)
	handler := openBankingHandler(svc)

	consent, err := svc.CreateConsent("acct_get", []string{"ReadAccounts", "WritePayments"})
	if err != nil {
		t.Fatal(err)
	}
	payment, _, err := svc.CreatePayment("get-key-1", consent.ID, "acct_get", 100, "USD")
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer dev-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, "/openbanking/v1/consents/"+consent.ID); rec.Code != http.StatusOK {
		t.Fatalf("get consent status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var gotConsent Consent
	if err := json.Unmarshal(do(http.MethodGet, "/openbanking/v1/consents/"+consent.ID).Body.Bytes(), &gotConsent); err != nil {
		t.Fatal(err)
	}
	if gotConsent.ID != consent.ID || gotConsent.Status != "authorised" {
		t.Fatalf("consent = %+v", gotConsent)
	}
	if rec := do(http.MethodGet, "/openbanking/v1/payments/"+payment.ID); rec.Code != http.StatusOK {
		t.Fatalf("get payment status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/openbanking/v1/accounts/acct_get/balance"); rec.Code != http.StatusOK {
		t.Fatalf("balance status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/openbanking/v1/consents/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing consent status = %d, want 404", rec.Code)
	}
	if rec := do(http.MethodGet, "/openbanking/v1/payments/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing payment status = %d, want 404", rec.Code)
	}
}

func TestOpenBankingRejectsInvalidBodiesAndMissingKey(t *testing.T) {
	store := NewStore()
	_ = store.Fund("acct_bad", 1000)
	svc := NewOpenBankingService(store, NewMemoryBus(4), nil)
	handler := openBankingHandler(svc)

	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer dev-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(http.MethodPost, "/openbanking/v1/consents", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed consent status = %d, want 400", rec.Code)
	}
	if rec := send(http.MethodPost, "/openbanking/v1/consents", `{"account_id":"a"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty permissions status = %d, want 400", rec.Code)
	}
	consent, err := svc.CreateConsent("acct_bad", []string{"WritePayments"})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"consent_id":%q,"account_id":"acct_bad","amount_cents":10,"currency":"USD"}`, consent.ID)
	if rec := send(http.MethodPost, "/openbanking/v1/payments", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status = %d, want 400", rec.Code)
	}
	if rec := send(http.MethodPost, "/openbanking/v1/payments", "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed payment status = %d, want 400", rec.Code)
	}
}

func TestStoreIntentLookup(t *testing.T) {
	store := NewStore()
	_ = store.Fund("acct_lookup", 500)
	intent, created, err := store.CreateIntent("lookup-1", "acct_lookup", 50, "USD")
	if err != nil || !created {
		t.Fatalf("create = %v, %v", created, err)
	}
	got, ok := store.Intent(intent.ID)
	if !ok || got.ID != intent.ID {
		t.Fatalf("intent lookup = %+v, %v", got, ok)
	}
	if _, ok := store.Intent("missing"); ok {
		t.Fatal("missing intent returned ok")
	}
}
