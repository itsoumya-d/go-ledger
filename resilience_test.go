package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailoverCoordinator_RoutingAndAutoFailover(t *testing.T) {
	fc := NewFailoverCoordinator([]string{"ap-southeast-2", "ap-southeast-1"}, 2, 200*time.Millisecond)

	if active := fc.ActiveRegion(); active != "ap-southeast-2" {
		t.Fatalf("expected ap-southeast-2, got %s", active)
	}

	reg, err := fc.ExecuteWithFailover(context.Background(), func(r string) error { return nil })
	if err != nil || reg != "ap-southeast-2" {
		t.Fatalf("unexpected routing result: %s, %v", reg, err)
	}

	fc.RecordHeartbeat("ap-southeast-2", 300*time.Millisecond, false)
	fc.RecordHeartbeat("ap-southeast-2", 300*time.Millisecond, false)

	if active := fc.ActiveRegion(); active != "ap-southeast-1" {
		t.Fatalf("expected failover to ap-southeast-1, got %s", active)
	}

	reg, err = fc.ExecuteWithFailover(context.Background(), func(r string) error { return nil })
	if err != nil || reg != "ap-southeast-1" {
		t.Fatalf("unexpected failover execution: %s, %v", reg, err)
	}

	fc.RecordHeartbeat("ap-southeast-2", 50*time.Millisecond, true)
	if active := fc.ActiveRegion(); active != "ap-southeast-2" {
		t.Errorf("expected recovery to ap-southeast-2, got %s", active)
	}
}

func TestFailoverCoordinator_AllDown(t *testing.T) {
	fc := NewFailoverCoordinator([]string{"ap-southeast-2"}, 1, 100*time.Millisecond)
	fc.RecordHeartbeat("ap-southeast-2", 200*time.Millisecond, false)
	if _, err := fc.ExecuteWithFailover(context.Background(), func(r string) error { return nil }); !errors.Is(err, ErrAllRegionsDown) {
		t.Fatalf("expected ErrAllRegionsDown, got %v", err)
	}
}

func TestChaosInjector_Execution(t *testing.T) {
	ci := NewChaosInjector()
	sampleErr := errors.New("synthetic network error")
	if err := ci.Execute(func() error { return nil }); err != nil {
		t.Fatalf("expected nil when disarmed: %v", err)
	}
	ci.Arm(10*time.Millisecond, sampleErr)
	start := time.Now()
	err := ci.Execute(func() error { return nil })
	if !errors.Is(err, sampleErr) || time.Since(start) < 8*time.Millisecond {
		t.Fatalf("chaos injection failed: err=%v, elapsed=%v", err, time.Since(start))
	}
	ci.Disarm()
	if err := ci.Execute(func() error { return nil }); err != nil {
		t.Fatalf("expected nil after disarm: %v", err)
	}
}

func TestFailoverCoordinator_ConcurrentLoad(t *testing.T) {
	fc := NewFailoverCoordinator([]string{"ap-southeast-2", "ap-southeast-1"}, 5, 200*time.Millisecond)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = fc.ExecuteWithFailover(context.Background(), func(r string) error {
				count.Add(1)
				if idx%5 == 0 {
					return errors.New("transient error")
				}
				return nil
			})
		}(i)
	}
	wg.Wait()
	if count.Load() == 0 {
		t.Error("expected transactions executed under concurrency")
	}
}
