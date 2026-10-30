package main

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

var (
	ErrInvalid             = errors.New("invalid request")
	ErrNotFound            = errors.New("not found")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")
	ErrInsufficientFunds   = errors.New("insufficient funds")
)

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

type PaymentIntent struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	AmountCents    int64     `json:"amount_cents"`
	Currency       string    `json:"currency"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

type Posting struct {
	IntentID     string `json:"intent_id"`
	AccountID    string `json:"account_id"`
	AmountCents  int64  `json:"amount_cents"`
	Currency     string `json:"currency"`
	SettlementID string `json:"settlement_id"`
}

type Store struct {
	mu       sync.Mutex
	intents  map[string]*PaymentIntent
	byKey    map[string]string
	balances map[string]int64
	postings []Posting
}

const settlementAccount = "merchant_settlement"

func NewStore() *Store {
	return &Store{
		intents:  make(map[string]*PaymentIntent),
		byKey:    make(map[string]string),
		balances: make(map[string]int64),
	}
}

func validate(accountID string, amount int64, currency string) error {
	if accountID == "" {
		return fmt.Errorf("%w: account_id is required", ErrInvalid)
	}
	if amount <= 0 {
		return fmt.Errorf("%w: amount_cents must be positive", ErrInvalid)
	}
	if !currencyRE.MatchString(currency) {
		return fmt.Errorf("%w: currency must be a 3-letter ISO code", ErrInvalid)
	}
	return nil
}

func (s *Store) Fund(accountID string, amount int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if accountID == "" || amount <= 0 {
		return fmt.Errorf("%w: account_id and positive amount_cents are required", ErrInvalid)
	}
	s.balances[accountID] += amount
	return nil
}

// CreateIntent is idempotent by key: replaying the same key and payload returns
// the original intent; reusing the key with a different payload is a conflict.
func (s *Store) CreateIntent(key, accountID string, amount int64, currency string) (*PaymentIntent, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("%w: Idempotency-Key header is required", ErrInvalid)
	}
	if err := validate(accountID, amount, currency); err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existingID, ok := s.byKey[key]; ok {
		existing := s.intents[existingID]
		if existing.AccountID != accountID || existing.AmountCents != amount || existing.Currency != currency {
			return nil, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}

	intent := &PaymentIntent{
		ID:             fmt.Sprintf("pi_%d", len(s.intents)+1),
		AccountID:      accountID,
		AmountCents:    amount,
		Currency:       currency,
		Status:         "requires_capture",
		IdempotencyKey: key,
		CreatedAt:      time.Now().UTC(),
	}
	s.intents[intent.ID] = intent
	s.byKey[key] = intent.ID
	return intent, true, nil
}

// Capture posts a double-entry pair exactly once. Replaying capture on an
// already-captured intent is a no-op and returns the captured intent.
func (s *Store) Capture(id string) (*PaymentIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	intent, ok := s.intents[id]
	if !ok {
		return nil, ErrNotFound
	}
	if intent.Status == "captured" {
		return intent, nil
	}
	if s.balances[intent.AccountID] < intent.AmountCents {
		return nil, ErrInsufficientFunds
	}

	s.balances[intent.AccountID] -= intent.AmountCents
	s.balances[settlementAccount] += intent.AmountCents
	s.postings = append(s.postings,
		Posting{IntentID: intent.ID, AccountID: intent.AccountID, AmountCents: -intent.AmountCents, Currency: intent.Currency, SettlementID: "stl_" + intent.ID},
		Posting{IntentID: intent.ID, AccountID: settlementAccount, AmountCents: intent.AmountCents, Currency: intent.Currency, SettlementID: "stl_" + intent.ID},
	)
	intent.Status = "captured"
	return intent, nil
}

func (s *Store) Balance(accountID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balances[accountID]
}

func (s *Store) Postings() []Posting {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Posting, len(s.postings))
	copy(out, s.postings)
	return out
}

func (s *Store) Intent(id string) (*PaymentIntent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.intents[id]
	return intent, ok
}
