package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnauthorized         = errors.New("unauthorized")
	ErrConsentNotFound      = errors.New("consent not found")
	ErrConsentNotAuthorised = errors.New("consent is not authorised")
	ErrPaymentNotFound      = errors.New("payment not found")
)

type Consent struct {
	ID          string    `json:"consent_id"`
	AccountID   string    `json:"account_id"`
	Permissions []string  `json:"permissions"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type Payment struct {
	ID             string    `json:"payment_id"`
	ConsentID      string    `json:"consent_id"`
	AccountID      string    `json:"account_id"`
	IntentID       string    `json:"intent_id"`
	AmountCents    int64     `json:"amount_cents"`
	Currency       string    `json:"currency"`
	Status         string    `json:"status"`
	IdempotencyKey string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

type OpenBankingService struct {
	mu            sync.Mutex
	store         *Store
	bus           EventBus
	audit         *PostgresAuditStore
	consents      map[string]*Consent
	payments      map[string]*Payment
	paymentsByKey map[string]string
}

func NewOpenBankingService(store *Store, bus EventBus, audit *PostgresAuditStore) *OpenBankingService {
	return &OpenBankingService{
		store:         store,
		bus:           bus,
		audit:         audit,
		consents:      make(map[string]*Consent),
		payments:      make(map[string]*Payment),
		paymentsByKey: make(map[string]string),
	}
}

func (s *OpenBankingService) CreateConsent(accountID string, permissions []string) (*Consent, error) {
	if accountID == "" || len(permissions) == 0 {
		return nil, fmt.Errorf("%w: account_id and permissions are required", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Consent{
		ID:          fmt.Sprintf("consent_%d", len(s.consents)+1),
		AccountID:   accountID,
		Permissions: permissions,
		Status:      "authorised",
		CreatedAt:   time.Now().UTC(),
	}
	s.consents[c.ID] = c
	s.record("consent.created", map[string]any{"consent_id": c.ID, "account_id": accountID, "permissions": permissions})
	return c, nil
}

func (s *OpenBankingService) GetConsent(id string) (*Consent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.consents[id]
	if !ok {
		return nil, ErrConsentNotFound
	}
	return c, nil
}

// CreatePayment is idempotent and settles synchronously through the double-entry
// ledger. Replaying the same key and payload returns the original payment.
func (s *OpenBankingService) CreatePayment(key, consentID, accountID string, amount int64, currency string) (*Payment, bool, error) {
	if key == "" {
		return nil, false, fmt.Errorf("%w: Idempotency-Key header is required", ErrInvalid)
	}
	if err := validate(accountID, amount, currency); err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existingID, ok := s.paymentsByKey[key]; ok {
		existing := s.payments[existingID]
		if existing.ConsentID != consentID || existing.AccountID != accountID || existing.AmountCents != amount || existing.Currency != currency {
			return nil, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}

	consent, ok := s.consents[consentID]
	if !ok {
		return nil, false, ErrConsentNotFound
	}
	if consent.Status != "authorised" {
		return nil, false, ErrConsentNotAuthorised
	}

	intent, _, err := s.store.CreateIntent("openbanking:"+key, accountID, amount, currency)
	if err != nil {
		return nil, false, err
	}
	if _, err := s.store.Capture(intent.ID); err != nil {
		return nil, false, err
	}

	p := &Payment{
		ID:             fmt.Sprintf("payment_%d", len(s.payments)+1),
		ConsentID:      consentID,
		AccountID:      accountID,
		IntentID:       intent.ID,
		AmountCents:    amount,
		Currency:       currency,
		Status:         "completed",
		IdempotencyKey: key,
		CreatedAt:      time.Now().UTC(),
	}
	s.payments[p.ID] = p
	s.paymentsByKey[key] = p.ID
	s.record("payment.completed", map[string]any{
		"payment_id": p.ID, "intent_id": intent.ID, "account_id": accountID,
		"amount_cents": amount, "currency": currency,
	})
	return p, true, nil
}

func (s *OpenBankingService) GetPayment(id string) (*Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return nil, ErrPaymentNotFound
	}
	return p, nil
}

func (s *OpenBankingService) record(eventType string, payload map[string]any) {
	event := Event{Type: eventType, ID: fmt.Sprintf("evt_%d", time.Now().UnixNano()), Payload: payload, Time: time.Now().UTC()}
	if s.bus != nil {
		_ = s.bus.Publish(nil, event)
	}
	if s.audit != nil {
		_ = s.audit.Append(nil, event)
	}
}

func bearerAuth(next http.Handler) http.Handler {
	expected := os.Getenv("OPENBANKING_TOKEN")
	if expected == "" {
		expected = "dev-token"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeError(w, ErrUnauthorized)
			return
		}
		got := strings.TrimPrefix(header, prefix)
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			writeError(w, ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func openBankingHandler(svc *OpenBankingService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /openbanking/v1/consents", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AccountID   string   `json:"account_id"`
			Permissions []string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		c, err := svc.CreateConsent(req.AccountID, req.Permissions)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	})

	mux.HandleFunc("GET /openbanking/v1/consents/{consentID}", func(w http.ResponseWriter, r *http.Request) {
		c, err := svc.GetConsent(r.PathValue("consentID"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	mux.HandleFunc("POST /openbanking/v1/payments", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ConsentID   string `json:"consent_id"`
			AccountID   string `json:"account_id"`
			AmountCents int64  `json:"amount_cents"`
			Currency    string `json:"currency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		p, created, err := svc.CreatePayment(r.Header.Get("Idempotency-Key"), req.ConsentID, req.AccountID, req.AmountCents, req.Currency)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, p)
	})

	mux.HandleFunc("GET /openbanking/v1/payments/{paymentID}", func(w http.ResponseWriter, r *http.Request) {
		p, err := svc.GetPayment(r.PathValue("paymentID"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("GET /openbanking/v1/accounts/{accountID}/balance", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":    r.PathValue("accountID"),
			"balance_cents": svc.store.Balance(r.PathValue("accountID")),
			"currency":      "USD",
		})
	})

	return bearerAuth(mux)
}
