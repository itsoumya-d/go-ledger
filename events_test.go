package main

import (
	"context"
	"testing"
	"time"
)

func TestMemoryBusPublishSubscribe(t *testing.T) {
	bus := NewMemoryBus(4)
	defer bus.Close()

	event := Event{Type: "payment.completed", ID: "evt_1", Payload: map[string]any{"amount_cents": 100}, Time: time.Now().UTC()}
	if err := bus.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bus.Subscribe():
		if got.Type != event.Type || got.ID != event.ID {
			t.Fatalf("got = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
