package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNewPostgresAuditStoreRequiresDSN(t *testing.T) {
	if _, err := NewPostgresAuditStore(""); err == nil {
		t.Fatal("expected an error for an empty DSN")
	}
}

func TestPostgresAuditStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	store, err := NewPostgresAuditStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID:      fmt.Sprintf("evt_test_%d", time.Now().UnixNano()),
		Type:    "test.roundtrip",
		Payload: map[string]any{"amount_cents": float64(4200), "currency": "USD"},
		Time:    time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("duplicate append should be a no-op: %v", err)
	}

	events, err := store.List(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *Event
	for i := range events {
		if events[i].ID == event.ID {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("event %s not found in %d rows", event.ID, len(events))
	}
	if found.Type != event.Type {
		t.Fatalf("type = %q, want %q", found.Type, event.Type)
	}
	if amount, ok := found.Payload["amount_cents"].(float64); !ok || amount != 4200 {
		t.Fatalf("payload = %#v", found.Payload)
	}
	if !found.Time.Equal(event.Time) {
		t.Fatalf("time = %s, want %s", found.Time, event.Time)
	}
}
