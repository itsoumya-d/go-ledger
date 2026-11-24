package main

import (
	"context"
	"testing"
)

func TestDisasterRecoverySnapshotAndIntegrity(t *testing.T) {
	ctx := context.Background()
	orchestrator := NewDisasterRecoveryOrchestrator()

	payload := []byte("WAL_CHECKPOINT_LEDGER_TRANSACTION_STATE_DATA_2026")
	manifest, err := orchestrator.RegisterSnapshot(ctx, "snap-001", "ledger_prod", "s3", payload)
	if err != nil {
		t.Fatalf("unexpected snapshot failure: %v", err)
	}

	if manifest.ByteSize != int64(len(payload)) {
		t.Errorf("expected size %d, got %d", len(payload), manifest.ByteSize)
	}

	valid, err := orchestrator.VerifyIntegrity("snap-001", payload)
	if err != nil || !valid {
		t.Fatalf("expected valid integrity check, got valid=%v, err=%v", valid, err)
	}

	tamperedPayload := []byte("CORRUPTED_LEDGER_TRANSACTION_STATE_DATA")
	validTampered, err := orchestrator.VerifyIntegrity("snap-001", tamperedPayload)
	if err != nil {
		t.Fatalf("unexpected error checking tampered payload: %v", err)
	}
	if validTampered {
		t.Errorf("expected tampered payload to fail integrity check")
	}

	// Missing manifest check
	_, errMissing := orchestrator.VerifyIntegrity("non-existent-snap", payload)
	if errMissing == nil {
		t.Errorf("expected error for non-existent snapshot ID")
	}

	// Empty parameter checks
	_, errEmpty := orchestrator.RegisterSnapshot(ctx, "", "db", "s3", payload)
	if errEmpty == nil {
		t.Errorf("expected error when registering snapshot with empty backup ID")
	}
	_, errEmptyPayload := orchestrator.RegisterSnapshot(ctx, "snap-empty", "db", "s3", []byte{})
	if errEmptyPayload == nil {
		t.Errorf("expected error when registering snapshot with empty payload")
	}
}

func TestCardinalityGovernor(t *testing.T) {
	governor := NewCardinalityGovernor(3)

	// Add 3 distinct series
	if !governor.AllowMetric("http_requests_total{code=200}") {
		t.Errorf("metric series 1 should be allowed")
	}
	if !governor.AllowMetric("http_requests_total{code=400}") {
		t.Errorf("metric series 2 should be allowed")
	}
	if !governor.AllowMetric("http_requests_total{code=500}") {
		t.Errorf("metric series 3 should be allowed")
	}

	// Repeated metric series should still be allowed
	if !governor.AllowMetric("http_requests_total{code=200}") {
		t.Errorf("existing metric series should be allowed")
	}

	// 4th distinct metric series exceeds capacity
	if governor.AllowMetric("http_requests_total{code=404}") {
		t.Errorf("4th metric series should be dropped by governor")
	}

	active, dropped := governor.Stats()
	if active != 3 || dropped != 1 {
		t.Errorf("expected 3 active and 1 dropped series, got active=%d dropped=%d", active, dropped)
	}
}

func TestHealthProbeEvaluator(t *testing.T) {
	prober := NewHealthProbeEvaluator(100)

	live, msg := prober.LivenessProbe()
	if !live || msg != "live" {
		t.Errorf("expected healthy liveness probe, got live=%v msg=%s", live, msg)
	}

	ready, readyMsg := prober.ReadinessProbe()
	if !ready || readyMsg != "ready" {
		t.Errorf("expected healthy readiness probe, got ready=%v msg=%s", ready, readyMsg)
	}

	// Simulate database failure
	prober.SetDBStatus(false)
	readyFailed, _ := prober.ReadinessProbe()
	if readyFailed {
		t.Errorf("readiness probe should fail when DB status is unhealthy")
	}

	// Restore DB, simulate traffic saturation
	prober.SetDBStatus(true)
	prober.SetLoad(150) // Above limit of 100
	readySaturated, _ := prober.ReadinessProbe()
	if readySaturated {
		t.Errorf("readiness probe should fail when connection pool is saturated")
	}
}
