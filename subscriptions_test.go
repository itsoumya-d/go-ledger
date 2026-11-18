package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func computeTestSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestSubscriptionLifecycleAndEntitlement(t *testing.T) {
	svc := NewSubscriptionService("")
	fixedNow := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixedNow }

	// 1. Create trial subscription
	sub, created, err := svc.CreateSubscription("key_trial_1", "acc_100", "enterprise_5000", 14)
	if err != nil {
		t.Fatalf("unexpected error creating subscription: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true")
	}
	if sub.Status != StatusTrialing {
		t.Fatalf("expected status=trialing, got %s", sub.Status)
	}
	expectedEnd := fixedNow.AddDate(0, 0, 14)
	if !sub.CurrentPeriodEnd.Equal(expectedEnd) {
		t.Fatalf("expected period end %v, got %v", expectedEnd, sub.CurrentPeriodEnd)
	}

	// 2. Idempotent re-creation with identical key
	sub2, created2, err := svc.CreateSubscription("key_trial_1", "acc_100", "enterprise_5000", 14)
	if err != nil || created2 || sub2.ID != sub.ID {
		t.Fatalf("expected idempotent lookup without creation, got err=%v, created=%v", err, created2)
	}

	// 3. Conflict on key reuse with different plan
	_, _, err = svc.CreateSubscription("key_trial_1", "acc_100", "voice_seat", 14)
	if err == nil {
		t.Fatalf("expected idempotency conflict error")
	}

	// 4. Verify Entitlement
	ok, err := svc.VerifyEntitlement("acc_100", "enterprise_5000")
	if err != nil || !ok {
		t.Fatalf("expected entitlement ok=true, got ok=%v, err=%v", ok, err)
	}

	mismatch, _ := svc.VerifyEntitlement("acc_100", "wholesale_100")
	if mismatch {
		t.Fatalf("expected mismatch for different plan")
	}

	// 5. Cancel subscription
	canceled, err := svc.CancelSubscription(sub.ID)
	if err != nil || canceled.Status != StatusCanceled {
		t.Fatalf("expected status=canceled, got err=%v, sub=%+v", err, canceled)
	}

	// Verify entitlement is revoked
	ok, err = svc.VerifyEntitlement("acc_100", "enterprise_5000")
	if ok || err == nil {
		t.Fatalf("expected entitlement revoked after cancellation")
	}
}

func TestStripeWebhookIdempotentProcessingAndSignatures(t *testing.T) {
	secret := "whsec_super_secret_test_key"
	svc := NewSubscriptionService(secret)
	fixedNow := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixedNow }

	// Create active subscription
	sub, _, err := svc.CreateSubscription("key_active_1", "acc_200", "enterprise_5000", 0)
	if err != nil {
		t.Fatalf("failed creating sub: %v", err)
	}

	// Build webhook event payload
	eventData, _ := json.Marshal(map[string]any{
		"id":         sub.ID,
		"account_id": "acc_200",
		"status":     "past_due",
	})
	payload, _ := json.Marshal(WebhookEvent{
		ID:        "evt_payment_failed_1",
		Type:      "invoice.payment_failed",
		Timestamp: fixedNow.Unix(),
		Data:      eventData,
	})

	// 1. Rejects invalid signature
	_, _, err = svc.ProcessWebhookEvent("bad_sig", payload)
	if err == nil {
		t.Fatalf("expected invalid signature error")
	}

	// 2. Processes valid signature
	validSig := computeTestSignature(secret, payload)
	evtID, firstRun, err := svc.ProcessWebhookEvent(validSig, payload)
	if err != nil || !firstRun || evtID != "evt_payment_failed_1" {
		t.Fatalf("failed processing webhook: err=%v, firstRun=%v", err, firstRun)
	}

	// Verify status updated to past_due
	updatedSub, _ := svc.GetSubscription(sub.ID)
	if updatedSub.Status != StatusPastDue {
		t.Fatalf("expected past_due, got %s", updatedSub.Status)
	}

	// 3. Replay protection (idempotency)
	evtID2, replayRun, err := svc.ProcessWebhookEvent(validSig, payload)
	if err != nil || replayRun || evtID2 != "evt_payment_failed_1" {
		t.Fatalf("expected replay detection (firstRun=false), got firstRun=%v, err=%v", replayRun, err)
	}

	// 4. Payment succeeded event updates status back to active
	successData, _ := json.Marshal(map[string]any{
		"id":     sub.ID,
		"status": "active",
	})
	successPayload, _ := json.Marshal(WebhookEvent{
		ID:        "evt_payment_succeeded_2",
		Type:      "invoice.payment_succeeded",
		Timestamp: fixedNow.Unix(),
		Data:      successData,
	})
	successSig := computeTestSignature(secret, successPayload)
	_, _, err = svc.ProcessWebhookEvent(successSig, successPayload)
	if err != nil {
		t.Fatalf("failed processing success webhook: %v", err)
	}

	activeSub, _ := svc.GetSubscription(sub.ID)
	if activeSub.Status != StatusActive {
		t.Fatalf("expected active status after payment success, got %s", activeSub.Status)
	}
}
