package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LoyaltyEvent struct {
	EventID string    `json:"event_id"`
	UserID  string    `json:"user_id"`
	GameID  string    `json:"game_id"`
	Type    string    `json:"type"`
	Points  int64     `json:"points"`
	TS      time.Time `json:"ts"`
}

type Balance struct {
	UserID  string `json:"user_id"`
	Balance int64  `json:"balance"`
}

type PlayerIndex struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Level  int    `json:"level"`
	GameID string `json:"game_id"`
}

type LoyaltyService struct {
	mu       sync.Mutex
	events   map[string]*LoyaltyEvent
	balances map[string]int64
	players  map[string]PlayerIndex
	audit    []LoyaltyEvent
}

func NewLoyaltyService() *LoyaltyService {
	s := &LoyaltyService{
		events:   make(map[string]*LoyaltyEvent),
		balances: make(map[string]int64),
		players:  make(map[string]PlayerIndex),
	}
	s.AddPlayer("user_001", "Alice Dice", 3, "justdice")
	s.AddPlayer("user_002", "Bob Slots", 1, "justdice")
	s.AddPlayer("user_003", "Alina Cards", 5, "poker")
	s.AddPlayer("user_004", "Charlie Dice", 2, "justdice")
	return s
}

func (s *LoyaltyService) AddPlayer(userID, name string, level int, gameID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.players[userID] = PlayerIndex{UserID: userID, Name: name, Level: level, GameID: gameID}
}

func (s *LoyaltyService) Audit() []LoyaltyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LoyaltyEvent, len(s.audit))
	copy(out, s.audit)
	return out
}

func (s *LoyaltyService) Balance(userID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balances[userID]
}

func (s *LoyaltyService) Leaderboard(limit int) []Balance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Balance, 0, len(s.balances))
	for userID, bal := range s.balances {
		out = append(out, Balance{UserID: userID, Balance: bal})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Balance != out[j].Balance {
			return out[i].Balance > out[j].Balance
		}
		return out[i].UserID < out[j].UserID
	})
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

func (s *LoyaltyService) Search(q, gameID string, level, limit int) []PlayerIndex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lowerQ := strings.ToLower(strings.TrimSpace(q))
	out := make([]PlayerIndex, 0, len(s.players))
	for _, p := range s.players {
		if lowerQ != "" && !strings.HasPrefix(strings.ToLower(p.Name), lowerQ) {
			continue
		}
		if gameID != "" && p.GameID != gameID {
			continue
		}
		if level > 0 && p.Level != level {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].UserID < out[j].UserID
	})
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

func loyaltyID(key, eventID string) string {
	if strings.TrimSpace(eventID) != "" {
		return strings.TrimSpace(eventID)
	}
	return strings.TrimSpace(key)
}

func (s *LoyaltyService) apply(typ, key, eventID, userID, gameID string, points int64, earn bool) (*LoyaltyEvent, int64, bool, error) {
	id := loyaltyID(key, eventID)
	if id == "" {
		return nil, 0, false, fmt.Errorf("%w: event_id or Idempotency-Key header is required", ErrInvalid)
	}
	if strings.TrimSpace(userID) == "" {
		return nil, 0, false, fmt.Errorf("%w: user_id is required", ErrInvalid)
	}
	if strings.TrimSpace(gameID) == "" {
		return nil, 0, false, fmt.Errorf("%w: game_id is required", ErrInvalid)
	}
	if points <= 0 {
		return nil, 0, false, fmt.Errorf("%w: points must be positive", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.events[id]; ok {
		if existing.Type != typ || existing.UserID != userID || existing.GameID != gameID || existing.Points != points {
			return nil, 0, false, ErrIdempotencyConflict
		}
		return existing, s.balances[userID], false, nil
	}

	if !earn && s.balances[userID] < points {
		return nil, 0, false, fmt.Errorf("%w: insufficient loyalty balance", ErrInsufficientFunds)
	}

	event := &LoyaltyEvent{
		EventID: id,
		UserID:  userID,
		GameID:  gameID,
		Type:    typ,
		Points:  points,
		TS:      time.Now().UTC(),
	}
	s.events[id] = event
	if earn {
		s.balances[userID] += points
	} else {
		s.balances[userID] -= points
	}
	s.audit = append(s.audit, *event)
	if _, ok := s.players[userID]; !ok {
		s.players[userID] = PlayerIndex{UserID: userID, Name: userID, Level: 1, GameID: gameID}
	}
	return event, s.balances[userID], true, nil
}

func (s *LoyaltyService) Earn(key, eventID, userID, gameID string, points int64) (*LoyaltyEvent, int64, bool, error) {
	return s.apply("earn", key, eventID, userID, gameID, points, true)
}

func (s *LoyaltyService) Redeem(key, eventID, userID, gameID string, points int64) (*LoyaltyEvent, int64, bool, error) {
	return s.apply("redeem", key, eventID, userID, gameID, points, false)
}

type loyaltyRequest struct {
	EventID string `json:"event_id"`
	UserID  string `json:"user_id"`
	GameID  string `json:"game_id"`
	Points  int64  `json:"points"`
}

func parseLimit(r *http.Request, def int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: limit must be a non-negative integer", ErrInvalid)
	}
	if n > 100 {
		n = 100
	}
	return n, nil
}

func registerLoyaltyRoutes(mux *http.ServeMux, svc *LoyaltyService) {
	mux.HandleFunc("POST /v1/loyalty/earn", func(w http.ResponseWriter, r *http.Request) {
		var req loyaltyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		event, balance, created, err := svc.Earn(r.Header.Get("Idempotency-Key"), req.EventID, req.UserID, req.GameID, req.Points)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{
			"event_id": event.EventID,
			"user_id":  event.UserID,
			"game_id":  event.GameID,
			"type":     event.Type,
			"points":   event.Points,
			"balance":  balance,
			"replayed": !created,
			"ts":       event.TS,
		})
	})

	mux.HandleFunc("POST /v1/loyalty/redeem", func(w http.ResponseWriter, r *http.Request) {
		var req loyaltyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		event, balance, created, err := svc.Redeem(r.Header.Get("Idempotency-Key"), req.EventID, req.UserID, req.GameID, req.Points)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{
			"event_id": event.EventID,
			"user_id":  event.UserID,
			"game_id":  event.GameID,
			"type":     event.Type,
			"points":   event.Points,
			"balance":  balance,
			"replayed": !created,
			"ts":       event.TS,
		})
	})

	mux.HandleFunc("GET /v1/loyalty/balance", func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			writeError(w, fmt.Errorf("%w: user_id query parameter is required", ErrInvalid))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id": userID,
			"balance": svc.Balance(userID),
		})
	})

	mux.HandleFunc("GET /v1/loyalty/leaderboard", func(w http.ResponseWriter, r *http.Request) {
		limit, err := parseLimit(r, 10)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, svc.Leaderboard(limit))
	})

	mux.HandleFunc("GET /v1/search/players", func(w http.ResponseWriter, r *http.Request) {
		limit, err := parseLimit(r, 20)
		if err != nil {
			writeError(w, err)
			return
		}
		level := 0
		if raw := r.URL.Query().Get("level"); raw != "" {
			level, err = strconv.Atoi(raw)
			if err != nil || level < 0 {
				writeError(w, fmt.Errorf("%w: level must be a non-negative integer", ErrInvalid))
				return
			}
		}
		results := svc.Search(r.URL.Query().Get("q"), r.URL.Query().Get("game_id"), level, limit)
		if results == nil {
			results = []PlayerIndex{}
		}
		writeJSON(w, http.StatusOK, results)
	})
}
