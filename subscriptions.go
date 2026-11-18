package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrInvalidSignature     = errors.New("invalid webhook signature")
	ErrSubscriptionInactive = errors.New("subscription is inactive or past due")
)

type SubscriptionStatus string

const (
	StatusActive   SubscriptionStatus = "active"
	StatusTrialing SubscriptionStatus = "trialing"
	StatusPastDue  SubscriptionStatus = "past_due"
	StatusCanceled SubscriptionStatus = "canceled"
)

type Subscription struct {
	ID                 string             `json:"id"`
	AccountID          string             `json:"account_id"`
	PlanID             string             `json:"plan_id"`
	Status             SubscriptionStatus `json:"status"`
	CurrentPeriodStart time.Time          `json:"current_period_start"`
	CurrentPeriodEnd   time.Time          `json:"current_period_end"`
	IdempotencyKey     string             `json:"idempotency_key"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type WebhookEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type SubscriptionService struct {
	mu              sync.RWMutex
	subscriptions   map[string]*Subscription
	byAccount       map[string][]*Subscription
	byKey           map[string]*Subscription
	processedEvents map[string]time.Time
	webhookSecret   string
	now             func() time.Time
	counter         int64
}

func NewSubscriptionService(webhookSecret string) *SubscriptionService {
	return &SubscriptionService{
		subscriptions:   make(map[string]*Subscription),
		byAccount:       make(map[string][]*Subscription),
		byKey:           make(map[string]*Subscription),
		processedEvents: make(map[string]time.Time),
		webhookSecret:   webhookSecret,
		now:             time.Now,
	}
}

func (s *SubscriptionService) CreateSubscription(key, accountID, planID string, trialDays int) (*Subscription, bool, error) {
	if accountID == "" || planID == "" {
		return nil, false, fmt.Errorf("%w: account_id and plan_id are required", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if key != "" {
		if existing, ok := s.byKey[key]; ok {
			if existing.AccountID != accountID || existing.PlanID != planID {
				return nil, false, fmt.Errorf("%w: idempotency key reused with different payload", ErrIdempotencyConflict)
			}
			return existing, false, nil
		}
	}

	s.counter++
	now := s.now().UTC()
	subID := fmt.Sprintf("sub_%d", s.counter)

	status := StatusActive
	periodStart := now
	var periodEnd time.Time

	if trialDays > 0 {
		status = StatusTrialing
		periodEnd = now.AddDate(0, 0, trialDays)
	} else {
		periodEnd = now.AddDate(0, 1, 0) // monthly period
	}

	sub := &Subscription{
		ID:                 subID,
		AccountID:          accountID,
		PlanID:             planID,
		Status:             status,
		CurrentPeriodStart: periodStart,
		CurrentPeriodEnd:   periodEnd,
		IdempotencyKey:     key,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	s.subscriptions[subID] = sub
	s.byAccount[accountID] = append(s.byAccount[accountID], sub)
	if key != "" {
		s.byKey[key] = sub
	}

	return sub, true, nil
}

func (s *SubscriptionService) GetSubscription(id string) (*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sub, ok := s.subscriptions[id]
	if !ok {
		return nil, ErrSubscriptionNotFound
	}
	return sub, nil
}

func (s *SubscriptionService) GetActiveSubscription(accountID string) (*Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	subs, ok := s.byAccount[accountID]
	if !ok || len(subs) == 0 {
		return nil, ErrSubscriptionNotFound
	}

	now := s.now().UTC()
	for i := len(subs) - 1; i >= 0; i-- {
		sub := subs[i]
		if (sub.Status == StatusActive || sub.Status == StatusTrialing) && sub.CurrentPeriodEnd.After(now) {
			return sub, nil
		}
	}

	return nil, ErrSubscriptionInactive
}

func (s *SubscriptionService) VerifyEntitlement(accountID, requiredPlan string) (bool, error) {
	sub, err := s.GetActiveSubscription(accountID)
	if err != nil {
		return false, err
	}
	if requiredPlan == "" || sub.PlanID == requiredPlan {
		return true, nil
	}
	return false, nil
}

func (s *SubscriptionService) CancelSubscription(id string) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub, ok := s.subscriptions[id]
	if !ok {
		return nil, ErrSubscriptionNotFound
	}

	sub.Status = StatusCanceled
	sub.UpdatedAt = s.now().UTC()
	return sub, nil
}

func (s *SubscriptionService) VerifySignature(payload []byte, signature string) error {
	if s.webhookSecret == "" {
		return nil // signature verification disabled in dev/local
	}

	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedMAC)) {
		return ErrInvalidSignature
	}
	return nil
}

func (s *SubscriptionService) ProcessWebhookEvent(signature string, payload []byte) (string, bool, error) {
	if err := s.VerifySignature(payload, signature); err != nil {
		return "", false, err
	}

	var event WebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", false, fmt.Errorf("%w: invalid webhook payload JSON: %v", ErrInvalid, err)
	}

	if event.ID == "" || event.Type == "" {
		return "", false, fmt.Errorf("%w: event id and type required", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, processed := s.processedEvents[event.ID]; processed {
		// Idempotent success replay
		return event.ID, false, nil
	}

	now := s.now().UTC()

	switch event.Type {
	case "customer.subscription.updated", "invoice.payment_succeeded":
		var subData struct {
			ID        string `json:"id"`
			AccountID string `json:"account_id"`
			PlanID    string `json:"plan_id"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(event.Data, &subData); err == nil && subData.ID != "" {
			if sub, ok := s.subscriptions[subData.ID]; ok {
				sub.Status = StatusActive
				sub.CurrentPeriodEnd = now.AddDate(0, 1, 0)
				sub.UpdatedAt = now
			}
		}

	case "customer.subscription.past_due", "invoice.payment_failed":
		var subData struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(event.Data, &subData); err == nil && subData.ID != "" {
			if sub, ok := s.subscriptions[subData.ID]; ok {
				sub.Status = StatusPastDue
				sub.UpdatedAt = now
			}
		}

	case "customer.subscription.deleted":
		var subData struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(event.Data, &subData); err == nil && subData.ID != "" {
			if sub, ok := s.subscriptions[subData.ID]; ok {
				sub.Status = StatusCanceled
				sub.UpdatedAt = now
			}
		}
	}

	s.processedEvents[event.ID] = now
	return event.ID, true, nil
}
