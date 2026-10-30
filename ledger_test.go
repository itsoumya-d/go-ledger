package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestIdempotentCreate(t *testing.T) {
	s := NewStore()
	first, created, err := s.CreateIntent("key-1", "customer_a", 500, "USD")
	if err != nil || !created {
		t.Fatalf("expected created intent, got created=%v err=%v", created, err)
	}
	replay, created, err := s.CreateIntent("key-1", "customer_a", 500, "USD")
	if err != nil || created {
		t.Fatalf("expected replay, got created=%v err=%v", created, err)
	}
	if replay.ID != first.ID {
		t.Fatalf("expected same intent id, got %s and %s", first.ID, replay.ID)
	}
	_, _, err = s.CreateIntent("key-1", "customer_a", 501, "USD")
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestCaptureDoubleEntry(t *testing.T) {
	s := NewStore()
	if err := s.Fund("customer_a", 1000); err != nil {
		t.Fatal(err)
	}
	intent, _, err := s.CreateIntent("key-2", "customer_a", 400, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(intent.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.Balance("customer_a"); got != 600 {
		t.Fatalf("customer balance = %d, want 600", got)
	}
	if got := s.Balance(settlementAccount); got != 400 {
		t.Fatalf("settlement balance = %d, want 400", got)
	}
	postings := s.Postings()
	if len(postings) != 2 {
		t.Fatalf("postings = %d, want 2", len(postings))
	}
	if postings[0].AmountCents+postings[1].AmountCents != 0 {
		t.Fatalf("double-entry postings do not sum to zero: %+v", postings)
	}
}

func TestInsufficientFunds(t *testing.T) {
	s := NewStore()
	_ = s.Fund("customer_a", 100)
	intent, _, err := s.CreateIntent("key-3", "customer_a", 200, "GBP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(intent.ID); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
	if got := s.Balance("customer_a"); got != 100 {
		t.Fatalf("balance changed on failed capture: %d", got)
	}
	if len(s.Postings()) != 0 {
		t.Fatalf("postings written on failed capture")
	}
}

func TestConcurrentCapture(t *testing.T) {
	s := NewStore()
	_ = s.Fund("customer_a", 1000)
	intent, _, err := s.CreateIntent("key-4", "customer_a", 300, "USD")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Capture(intent.ID)
		}()
	}
	wg.Wait()
	if got := s.Balance("customer_a"); got != 700 {
		t.Fatalf("customer balance = %d, want 700", got)
	}
	if got := s.Balance(settlementAccount); got != 300 {
		t.Fatalf("settlement balance = %d, want 300", got)
	}
	if len(s.Postings()) != 2 {
		t.Fatalf("expected exactly one double-entry pair, got %d postings", len(s.Postings()))
	}
	if intent.Status != "captured" {
		t.Fatalf("status = %s, want captured", intent.Status)
	}
}

func TestHTTPFlow(t *testing.T) {
	srv := httptest.NewServer(newServer(NewStore()))
	defer srv.Close()

	post := func(path, key string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if resp := post("/v1/accounts/customer_a/fund", "", map[string]int64{"amount_cents": 1000}); resp.StatusCode != http.StatusOK {
		t.Fatalf("fund status = %d", resp.StatusCode)
	}
	if resp := post("/v1/payment_intents", "http-key", map[string]any{"account_id": "customer_a", "amount_cents": 250, "currency": "USD"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if resp := post("/v1/payment_intents", "http-key", map[string]any{"account_id": "customer_a", "amount_cents": 250, "currency": "USD"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d", resp.StatusCode)
	}
	resp, err := http.Get(srv.URL + "/v1/accounts/customer_a/balance")
	if err != nil {
		t.Fatal(err)
	}
	var balance map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&balance)
	if balance["balance_cents"].(float64) != 1000 {
		t.Fatalf("unexpected balance: %v", balance)
	}
}

func TestValidation(t *testing.T) {
	s := NewStore()
	if _, _, err := s.CreateIntent("", "customer_a", 100, "USD"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid for missing key, got %v", err)
	}
	if _, _, err := s.CreateIntent("k", "customer_a", 0, "USD"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid for zero amount, got %v", err)
	}
	if _, _, err := s.CreateIntent("k", "customer_a", 100, "usd"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid for currency, got %v", err)
	}
}
