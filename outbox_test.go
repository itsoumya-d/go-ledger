package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type mockFailingBus struct {
	mu         sync.Mutex
	failCount  int
	failUntil  int
	published  []Event
	alwaysFail bool
}

func (b *mockFailingBus) Publish(ctx context.Context, event Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.alwaysFail {
		return errors.New("simulated bus outage")
	}
	if b.failCount < b.failUntil {
		b.failCount++
		return fmt.Errorf("transient network drop (%d/%d)", b.failCount, b.failUntil)
	}
	b.published = append(b.published, event)
	return nil
}

func (b *mockFailingBus) Close() error { return nil }

func TestOutboxEnqueueAndGet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()

	event := Event{
		Type:    "patient.appointment_booked",
		Payload: map[string]any{"practice_id": "prac_101", "patient_id": "pat_202"},
	}
	rec, err := store.Enqueue(ctx, event, 3)
	if err != nil {
		t.Fatalf("unexpected enqueue error: %v", err)
	}
	if rec.ID == "" || rec.Event.ID == "" {
		t.Fatalf("expected auto-generated IDs, got rec.ID=%s, event.ID=%s", rec.ID, rec.Event.ID)
	}
	if rec.Status != OutboxStatusPending {
		t.Fatalf("expected pending status, got %s", rec.Status)
	}
	if rec.Attempts != 0 {
		t.Fatalf("expected 0 attempts, got %d", rec.Attempts)
	}

	fetched, ok := store.Get(ctx, rec.ID)
	if !ok || fetched.ID != rec.ID {
		t.Fatalf("expected to fetch record %s, got ok=%v", rec.ID, ok)
	}
}

func TestOutboxValidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()

	_, err := store.Enqueue(ctx, Event{}, 3)
	if !errors.Is(err, ErrOutboxNilEvent) {
		t.Fatalf("expected ErrOutboxNilEvent, got %v", err)
	}
}

func TestOutboxFetchPendingBatching(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()

	for i := 0; i < 10; i++ {
		_, err := store.Enqueue(ctx, Event{Type: fmt.Sprintf("event.%d", i)}, 3)
		if err != nil {
			t.Fatalf("enqueue %d failed: %v", i, err)
		}
	}

	now := time.Now().UTC()
	pending, err := store.FetchPending(ctx, 4, now)
	if err != nil {
		t.Fatalf("fetch pending failed: %v", err)
	}
	if len(pending) != 4 {
		t.Fatalf("expected batch size 4, got %d", len(pending))
	}
}

func TestOutboxDispatcherSuccessfulPublish(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()
	bus := NewMemoryBus(10)
	metrics := NewMetrics()
	dispatcher := NewOutboxDispatcher(store, bus, metrics)

	_, _ = store.Enqueue(ctx, Event{Type: "ehr.patient_sync"}, 3)
	_, _ = store.Enqueue(ctx, Event{Type: "telephony.call_completed"}, 3)

	flushed, err := dispatcher.Flush(ctx, 10)
	if err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	if flushed != 2 {
		t.Fatalf("expected 2 flushed events, got %d", flushed)
	}

	stats := store.Stats(ctx)
	if stats.Published != 2 || stats.Pending != 0 {
		t.Fatalf("expected 2 published and 0 pending, got %+v", stats)
	}
}

func TestOutboxDispatcherRetryAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()
	bus := &mockFailingBus{alwaysFail: true}
	metrics := NewMetrics()
	dispatcher := NewOutboxDispatcher(store, bus, metrics)
	dispatcher.initialInterval = 1 * time.Millisecond
	dispatcher.maxInterval = 10 * time.Millisecond

	rec, _ := store.Enqueue(ctx, Event{Type: "payment.charge_attempted"}, 2)

	// Attempt 1 -> fails, should be scheduled for retry
	flushed, err := dispatcher.Flush(ctx, 10)
	if err != nil {
		t.Fatalf("flush 1 error: %v", err)
	}
	if flushed != 0 {
		t.Fatalf("expected 0 flushed, got %d", flushed)
	}

	r1, _ := store.Get(ctx, rec.ID)
	if r1.Status != OutboxStatusPending || r1.Attempts != 1 {
		t.Fatalf("expected pending after 1 failure with attempts=1, got status=%s attempts=%d", r1.Status, r1.Attempts)
	}

	// Sleep past backoff
	time.Sleep(15 * time.Millisecond)

	// Attempt 2 -> hits max_attempts (2) -> dead letter
	flushed, err = dispatcher.Flush(ctx, 10)
	if err != nil {
		t.Fatalf("flush 2 error: %v", err)
	}
	if flushed != 0 {
		t.Fatalf("expected 0 flushed, got %d", flushed)
	}

	r2, _ := store.Get(ctx, rec.ID)
	if r2.Status != OutboxStatusDeadLetter || r2.Attempts != 2 {
		t.Fatalf("expected dead_letter status with attempts=2, got status=%s attempts=%d", r2.Status, r2.Attempts)
	}

	dlqs := store.ListDeadLetters(ctx)
	if len(dlqs) != 1 || dlqs[0].ID != rec.ID {
		t.Fatalf("expected 1 dead letter matching %s, got %d", rec.ID, len(dlqs))
	}

	// Replay dead letter
	err = store.ReplayDeadLetter(ctx, rec.ID)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	r3, _ := store.Get(ctx, rec.ID)
	if r3.Status != OutboxStatusPending || r3.Attempts != 0 {
		t.Fatalf("expected replayed status pending with attempts 0, got %s, attempts=%d", r3.Status, r3.Attempts)
	}

	// Replaying a non-dead letter should fail
	if err := store.ReplayDeadLetter(ctx, rec.ID); !errors.Is(err, ErrOutboxNotDead) {
		t.Fatalf("expected ErrOutboxNotDead, got %v", err)
	}
}

func TestOutboxHTTPEndpoints(t *testing.T) {
	store := NewStore()
	handler := newServer(store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// 1. Enqueue an outbox event via HTTP
	enqueuePayload := []byte(`{"type":"review.invite_sent","payload":{"practice_id":"p1"},"max_attempts":3}`)
	resp, err := http.Post(srv.URL+"/v1/outbox/enqueue", "application/json", bytes.NewReader(enqueuePayload))
	if err != nil {
		t.Fatalf("enqueue request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}
	var createdRec OutboxRecord
	_ = json.NewDecoder(resp.Body).Decode(&createdRec)
	resp.Body.Close()

	if createdRec.ID == "" || createdRec.Event.Type != "review.invite_sent" {
		t.Fatalf("unexpected record: %+v", createdRec)
	}

	// 2. Query stats
	resp, err = http.Get(srv.URL + "/v1/outbox/stats")
	if err != nil {
		t.Fatalf("stats request failed: %v", err)
	}
	var stats OutboxStats
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if stats.TotalEnqueued < 1 || stats.Pending < 1 {
		t.Fatalf("expected at least 1 pending and enqueued, got %+v", stats)
	}

	// 3. Get record by ID
	resp, err = http.Get(srv.URL + "/v1/outbox/records/" + createdRec.ID)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get record failed: status=%d err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// 4. Flush
	resp, err = http.Post(srv.URL+"/v1/outbox/flush", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("flush failed: status=%d err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// 5. Query stats after flush
	resp, _ = http.Get(srv.URL + "/v1/outbox/stats")
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if stats.Published < 1 {
		t.Fatalf("expected published >= 1, got %+v", stats)
	}
}

func TestOutboxIntegrationWithPaymentAndInvoiceCapture(t *testing.T) {
	store := NewStore()
	handler := newServer(store)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Fund account
	_, _ = http.Post(srv.URL+"/v1/accounts/acct_weave/fund", "application/json", bytes.NewReader([]byte(`{"amount_cents":50000}`)))

	// Create payment intent
	createReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/payment_intents", bytes.NewReader([]byte(`{"account_id":"acct_weave","amount_cents":2500,"currency":"USD"}`)))
	createReq.Header.Set("Idempotency-Key", "pi-weave-1")
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil || createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create payment intent failed: %v", err)
	}
	var pi PaymentIntent
	_ = json.NewDecoder(createResp.Body).Decode(&pi)
	createResp.Body.Close()

	// Capture payment intent -> should trigger outbox enqueue
	capResp, err := http.Post(srv.URL+"/v1/payment_intents/"+pi.ID+"/capture", "application/json", nil)
	if err != nil || capResp.StatusCode != http.StatusOK {
		t.Fatalf("capture payment intent failed: %v", err)
	}
	capResp.Body.Close()

	// Verify outbox stats reflected the capture
	resp, _ := http.Get(srv.URL + "/v1/outbox/stats")
	var stats OutboxStats
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if stats.TotalEnqueued < 1 {
		t.Fatalf("expected outbox event enqueued for payment capture, got %+v", stats)
	}

	// Create and capture a rated invoice
	invReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rated_invoices", bytes.NewReader([]byte(`{
		"account_id": "acct_weave",
		"plan_id": "wholesale_100",
		"period_start": "2026-11-01",
		"period_end": "2026-11-30",
		"usage_units": 1500,
		"active_days": 30
	}`)))
	invReq.Header.Set("Idempotency-Key", "inv-weave-1")
	invReq.Header.Set("Content-Type", "application/json")
	invResp, err := http.DefaultClient.Do(invReq)
	if err != nil || invResp.StatusCode != http.StatusCreated {
		t.Fatalf("create invoice failed: %v", err)
	}
	var inv RatedInvoice
	_ = json.NewDecoder(invResp.Body).Decode(&inv)
	invResp.Body.Close()

	// Capture invoice
	capInvResp, err := http.Post(srv.URL+"/v1/rated_invoices/"+inv.ID+"/capture", "application/json", nil)
	if err != nil || capInvResp.StatusCode != http.StatusOK {
		t.Fatalf("capture invoice failed: %v", err)
	}
	capInvResp.Body.Close()

	// Flush outbox
	flushResp, _ := http.Post(srv.URL+"/v1/outbox/flush", "application/json", nil)
	flushResp.Body.Close()

	// Verify both events published
	resp, _ = http.Get(srv.URL + "/v1/outbox/stats")
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if stats.Published < 2 {
		t.Fatalf("expected at least 2 events published from outbox, got %+v", stats)
	}
}

func TestOutboxConcurrentEnqueueAndFlush(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()
	bus := NewMemoryBus(100)
	metrics := NewMetrics()
	dispatcher := NewOutboxDispatcher(store, bus, metrics)

	const workerCount = 10
	const eventsPerWorker = 20

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < eventsPerWorker; i++ {
				_, err := store.Enqueue(ctx, Event{
					Type:    "concurrency.event",
					Payload: map[string]any{"worker": workerID, "seq": i},
				}, 3)
				if err != nil {
					t.Errorf("worker %d failed enqueue: %v", workerID, err)
				}
			}
		}(w)
	}

	// Concurrent flush loop
	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
				_, _ = dispatcher.Flush(ctx, 25)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	close(stopCh)

	// Final flush
	_, _ = dispatcher.Flush(ctx, 300)

	stats := store.Stats(ctx)
	expectedTotal := workerCount * eventsPerWorker
	if stats.TotalEnqueued != expectedTotal {
		t.Fatalf("expected %d enqueued, got %d", expectedTotal, stats.TotalEnqueued)
	}
}
