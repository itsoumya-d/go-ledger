package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var (
	ErrOutboxNotFound = errors.New("outbox record not found")
	ErrOutboxNotDead  = errors.New("outbox record is not in dead letter state")
	ErrOutboxNilEvent = errors.New("outbox event type cannot be empty")
)

type OutboxStatus string

const (
	OutboxStatusPending    OutboxStatus = "pending"
	OutboxStatusPublished  OutboxStatus = "published"
	OutboxStatusDeadLetter OutboxStatus = "dead_letter"
)

type OutboxRecord struct {
	ID          string       `json:"id"`
	Event       Event        `json:"event"`
	Status      OutboxStatus `json:"status"`
	Attempts    int          `json:"attempts"`
	MaxAttempts int          `json:"max_attempts"`
	LastError   string       `json:"last_error,omitempty"`
	NextRetryAt time.Time    `json:"next_retry_at"`
	CreatedAt   time.Time    `json:"created_at"`
	PublishedAt *time.Time   `json:"published_at,omitempty"`
}

type OutboxStats struct {
	TotalEnqueued int `json:"total_enqueued"`
	Pending       int `json:"pending"`
	Published     int `json:"published"`
	DeadLetter    int `json:"dead_letter"`
	TotalRetries  int `json:"total_retries"`
}

type OutboxStore interface {
	Enqueue(ctx context.Context, event Event, maxAttempts int) (*OutboxRecord, error)
	FetchPending(ctx context.Context, batchSize int, now time.Time) ([]*OutboxRecord, error)
	MarkPublished(ctx context.Context, id string, publishedAt time.Time) error
	MarkRetry(ctx context.Context, id string, err error, nextRetryAt time.Time) error
	MarkDeadLetter(ctx context.Context, id string, err error) error
	ReplayDeadLetter(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*OutboxRecord, bool)
	Stats(ctx context.Context) OutboxStats
	ListDeadLetters(ctx context.Context) []*OutboxRecord
}

type MemoryOutboxStore struct {
	mu           sync.RWMutex
	records      map[string]*OutboxRecord
	order        []string
	totalRetries int
}

func NewMemoryOutboxStore() *MemoryOutboxStore {
	return &MemoryOutboxStore{
		records: make(map[string]*OutboxRecord),
	}
}

func randomOutboxHex(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func (s *MemoryOutboxStore) Enqueue(ctx context.Context, event Event, maxAttempts int) (*OutboxRecord, error) {
	if event.Type == "" {
		return nil, ErrOutboxNilEvent
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	now := time.Now().UTC()
	if event.Time.IsZero() {
		event.Time = now
	}
	if event.ID == "" {
		event.ID = randomOutboxHex("evt")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := &OutboxRecord{
		ID:          randomOutboxHex("obx"),
		Event:       event,
		Status:      OutboxStatusPending,
		Attempts:    0,
		MaxAttempts: maxAttempts,
		NextRetryAt: now,
		CreatedAt:   now,
	}
	s.records[rec.ID] = rec
	s.order = append(s.order, rec.ID)
	return rec, nil
}

func (s *MemoryOutboxStore) FetchPending(ctx context.Context, batchSize int, now time.Time) ([]*OutboxRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if batchSize <= 0 {
		batchSize = 50
	}
	var out []*OutboxRecord
	for _, id := range s.order {
		rec := s.records[id]
		if rec.Status == OutboxStatusPending && !rec.NextRetryAt.After(now) {
			copy := *rec
			out = append(out, &copy)
			if len(out) >= batchSize {
				break
			}
		}
	}
	return out, nil
}

func (s *MemoryOutboxStore) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	rec.Status = OutboxStatusPublished
	rec.PublishedAt = &publishedAt
	rec.LastError = ""
	return nil
}

func (s *MemoryOutboxStore) MarkRetry(ctx context.Context, id string, err error, nextRetryAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	rec.Attempts++
	s.totalRetries++
	if err != nil {
		rec.LastError = err.Error()
	}
	rec.NextRetryAt = nextRetryAt
	return nil
}

func (s *MemoryOutboxStore) MarkDeadLetter(ctx context.Context, id string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	rec.Status = OutboxStatusDeadLetter
	rec.Attempts++
	if err != nil {
		rec.LastError = err.Error()
	}
	return nil
}

func (s *MemoryOutboxStore) ReplayDeadLetter(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.records[id]
	if !ok {
		return ErrOutboxNotFound
	}
	if rec.Status != OutboxStatusDeadLetter {
		return ErrOutboxNotDead
	}
	rec.Status = OutboxStatusPending
	rec.Attempts = 0
	rec.LastError = ""
	rec.NextRetryAt = time.Now().UTC()
	return nil
}

func (s *MemoryOutboxStore) Get(ctx context.Context, id string) (*OutboxRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, ok := s.records[id]
	if !ok {
		return nil, false
	}
	copy := *rec
	return &copy, true
}

func (s *MemoryOutboxStore) Stats(ctx context.Context) OutboxStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := OutboxStats{
		TotalEnqueued: len(s.records),
		TotalRetries:  s.totalRetries,
	}
	for _, rec := range s.records {
		switch rec.Status {
		case OutboxStatusPending:
			stats.Pending++
		case OutboxStatusPublished:
			stats.Published++
		case OutboxStatusDeadLetter:
			stats.DeadLetter++
		}
	}
	return stats
}

func (s *MemoryOutboxStore) ListDeadLetters(ctx context.Context) []*OutboxRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var dlq []*OutboxRecord
	for _, id := range s.order {
		rec := s.records[id]
		if rec.Status == OutboxStatusDeadLetter {
			copy := *rec
			dlq = append(dlq, &copy)
		}
	}
	return dlq
}

type OutboxDispatcher struct {
	store           OutboxStore
	bus             EventBus
	metrics         *Metrics
	initialInterval time.Duration
	maxInterval     time.Duration
}

func NewOutboxDispatcher(store OutboxStore, bus EventBus, metrics *Metrics) *OutboxDispatcher {
	return &OutboxDispatcher{
		store:           store,
		bus:             bus,
		metrics:         metrics,
		initialInterval: 25 * time.Millisecond,
		maxInterval:     5 * time.Second,
	}
}

func (d *OutboxDispatcher) CalculateBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	multiplier := math.Pow(2, float64(attempt))
	backoff := time.Duration(float64(d.initialInterval) * multiplier)
	if backoff > d.maxInterval {
		backoff = d.maxInterval
	}
	return backoff
}

func (d *OutboxDispatcher) Flush(ctx context.Context, batchSize int) (int, error) {
	now := time.Now().UTC()
	pending, err := d.store.FetchPending(ctx, batchSize, now)
	if err != nil {
		return 0, err
	}

	publishedCount := 0
	for _, rec := range pending {
		if ctx.Err() != nil {
			return publishedCount, ctx.Err()
		}

		err := d.bus.Publish(ctx, rec.Event)
		if err == nil {
			pubTime := time.Now().UTC()
			_ = d.store.MarkPublished(ctx, rec.ID, pubTime)
			publishedCount++
			if d.metrics != nil {
				d.metrics.RecordOutboxPublished(rec.Event.Type)
			}
			continue
		}

		nextAttempt := rec.Attempts + 1
		if nextAttempt >= rec.MaxAttempts {
			_ = d.store.MarkDeadLetter(ctx, rec.ID, err)
			if d.metrics != nil {
				d.metrics.RecordOutboxDLQ(rec.Event.Type)
			}
		} else {
			backoff := d.CalculateBackoff(nextAttempt)
			nextRetry := now.Add(backoff)
			_ = d.store.MarkRetry(ctx, rec.ID, err, nextRetry)
			if d.metrics != nil {
				d.metrics.RecordOutboxRetry(rec.Event.Type)
			}
		}
	}

	return publishedCount, nil
}

type enqueueOutboxRequest struct {
	Type        string         `json:"type"`
	ID          string         `json:"id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	MaxAttempts int            `json:"max_attempts,omitempty"`
}

func registerOutboxRoutes(mux *http.ServeMux, store OutboxStore, dispatcher *OutboxDispatcher, metrics *Metrics) {
	mux.HandleFunc("GET /v1/outbox/stats", func(w http.ResponseWriter, r *http.Request) {
		stats := store.Stats(r.Context())
		writeJSON(w, http.StatusOK, stats)
	})

	mux.HandleFunc("POST /v1/outbox/flush", func(w http.ResponseWriter, r *http.Request) {
		batchSize := 50
		if b := r.URL.Query().Get("batch_size"); b != "" {
			if parsed, err := strconv.Atoi(b); err == nil && parsed > 0 {
				batchSize = parsed
			}
		}
		flushed, err := dispatcher.Flush(r.Context(), batchSize)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"flushed": flushed,
		})
	})

	mux.HandleFunc("POST /v1/outbox/enqueue", func(w http.ResponseWriter, r *http.Request) {
		var req enqueueOutboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		if req.Type == "" {
			writeError(w, fmt.Errorf("%w: type is required", ErrInvalid))
			return
		}
		event := Event{
			Type:    req.Type,
			ID:      req.ID,
			Payload: req.Payload,
			Time:    time.Now().UTC(),
		}
		rec, err := store.Enqueue(r.Context(), event, req.MaxAttempts)
		if err != nil {
			writeError(w, err)
			return
		}
		if metrics != nil {
			metrics.RecordOutboxEnqueued(event.Type)
		}
		writeJSON(w, http.StatusCreated, rec)
	})

	mux.HandleFunc("GET /v1/outbox/dead_letters", func(w http.ResponseWriter, r *http.Request) {
		dlq := store.ListDeadLetters(r.Context())
		writeJSON(w, http.StatusOK, dlq)
	})

	mux.HandleFunc("POST /v1/outbox/dead_letters/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.ReplayDeadLetter(r.Context(), id); err != nil {
			if errors.Is(err, ErrOutboxNotFound) {
				writeError(w, fmt.Errorf("%w: outbox record", ErrNotFound))
				return
			}
			if errors.Is(err, ErrOutboxNotDead) {
				writeError(w, fmt.Errorf("%w: outbox record is not dead-lettered", ErrInvalid))
				return
			}
			writeError(w, err)
			return
		}
		rec, _ := store.Get(r.Context(), id)
		writeJSON(w, http.StatusOK, rec)
	})

	mux.HandleFunc("GET /v1/outbox/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		rec, ok := store.Get(r.Context(), id)
		if !ok {
			writeError(w, fmt.Errorf("%w: outbox record", ErrNotFound))
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})
}
