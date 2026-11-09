package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestLoyaltyEarnIdempotency(t *testing.T) {
	s := NewLoyaltyService()
	first, bal, created, err := s.Earn("", "evt_1", "user_001", "justdice", 100)
	if err != nil || !created {
		t.Fatalf("expected created earn, created=%v err=%v", created, err)
	}
	if first.EventID != "evt_1" || bal != 100 {
		t.Fatalf("unexpected first earn: %+v bal=%d", first, bal)
	}
	replay, bal2, created2, err := s.Earn("", "evt_1", "user_001", "justdice", 100)
	if err != nil || created2 {
		t.Fatalf("expected replay, created=%v err=%v", created2, err)
	}
	if replay.EventID != first.EventID {
		t.Fatalf("replay event mismatch: %s vs %s", replay.EventID, first.EventID)
	}
	if bal2 != 100 {
		t.Fatalf("replay balance = %d, want 100 (no double-credit)", bal2)
	}
	if got := s.Balance("user_001"); got != 100 {
		t.Fatalf("stored balance = %d, want 100", got)
	}
	viaKey, bal3, created3, err := s.Earn("key-earn-1", "", "user_001", "justdice", 50)
	if err != nil || !created3 || viaKey.EventID != "key-earn-1" || bal3 != 150 {
		t.Fatalf("Idempotency-Key earn failed: %+v bal=%d created=%v err=%v", viaKey, bal3, created3, err)
	}
}

func TestLoyaltyIdempotencyConflict(t *testing.T) {
	s := NewLoyaltyService()
	if _, _, _, err := s.Earn("", "evt_conflict", "user_001", "justdice", 100); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Earn("", "evt_conflict", "user_001", "justdice", 200); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for different points, got %v", err)
	}
	if _, _, _, err := s.Redeem("", "evt_conflict", "user_001", "justdice", 100); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict across earn/redeem type, got %v", err)
	}
	if got := s.Balance("user_001"); got != 100 {
		t.Fatalf("balance changed on conflict: %d", got)
	}
}

func TestLoyaltyRedeem(t *testing.T) {
	s := NewLoyaltyService()
	if _, _, _, err := s.Earn("", "evt_r1", "user_001", "justdice", 500); err != nil {
		t.Fatal(err)
	}
	ev, bal, created, err := s.Redeem("", "redeem_1", "user_001", "justdice", 200)
	if err != nil || !created {
		t.Fatalf("redeem: created=%v err=%v", created, err)
	}
	if ev.Type != "redeem" || bal != 300 {
		t.Fatalf("redeem result: %+v bal=%d, want balance 300", ev, bal)
	}
	_, bal2, created2, err := s.Redeem("", "redeem_1", "user_001", "justdice", 200)
	if err != nil || created2 || bal2 != 300 {
		t.Fatalf("redeem replay: created=%v bal=%d err=%v", created2, bal2, err)
	}
	if got := s.Balance("user_001"); got != 300 {
		t.Fatalf("final balance = %d, want 300", got)
	}
}

func TestLoyaltyRedeemInsufficient(t *testing.T) {
	s := NewLoyaltyService()
	if _, _, _, err := s.Earn("", "evt_r2", "user_001", "justdice", 50); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Redeem("", "redeem_2", "user_001", "justdice", 500); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
	if got := s.Balance("user_001"); got != 50 {
		t.Fatalf("balance changed on failed redeem: %d", got)
	}
	srv := httptest.NewServer(newServer(NewStore()))
	defer srv.Close()
	post := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := post("/v1/loyalty/earn", map[string]any{"event_id": "http_e1", "user_id": "u_http", "game_id": "justdice", "points": 100}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("earn status = %d", resp.StatusCode)
	}
	resp := post("/v1/loyalty/redeem", map[string]any{"event_id": "http_r1", "user_id": "u_http", "game_id": "justdice", "points": 500})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("insufficient redeem status = %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLoyaltyValidation(t *testing.T) {
	s := NewLoyaltyService()
	cases := []struct {
		name string
		fn   func() error
	}{
		{"zero points", func() error { _, _, _, err := s.Earn("", "v1", "user_001", "justdice", 0); return err }},
		{"negative points", func() error { _, _, _, err := s.Redeem("", "v2", "user_001", "justdice", -5); return err }},
		{"missing user", func() error { _, _, _, err := s.Earn("", "v3", "", "justdice", 10); return err }},
		{"missing game", func() error { _, _, _, err := s.Earn("", "v4", "user_001", "", 10); return err }},
		{"missing event id", func() error { _, _, _, err := s.Earn("", "", "user_001", "justdice", 10); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}

func TestLoyaltyLeaderboardOrdering(t *testing.T) {
	s := NewLoyaltyService()
	earn := func(id, user string, pts int64) {
		if _, _, _, err := s.Earn("", id, user, "justdice", pts); err != nil {
			t.Fatal(err)
		}
	}
	earn("lb1", "user_b", 100)
	earn("lb2", "user_a", 100)
	earn("lb3", "user_c", 300)
	earn("lb4", "user_d", 50)
	board := s.Leaderboard(10)
	want := []Balance{
		{UserID: "user_c", Balance: 300},
		{UserID: "user_a", Balance: 100},
		{UserID: "user_b", Balance: 100},
		{UserID: "user_d", Balance: 50},
	}
	if len(board) != len(want) {
		t.Fatalf("leaderboard = %+v, want %+v", board, want)
	}
	for i := range want {
		if board[i] != want[i] {
			t.Fatalf("leaderboard[%d] = %+v, want %+v (full: %+v)", i, board[i], want[i], board)
		}
	}
	if got := s.Leaderboard(2); len(got) != 2 || got[0].UserID != "user_c" {
		t.Fatalf("limited leaderboard = %+v", got)
	}
}

func TestLoyaltySearchPrefixAndFilters(t *testing.T) {
	s := NewLoyaltyService()
	prefix := s.Search("ali", "", 0, 20)
	if len(prefix) != 2 {
		t.Fatalf("prefix 'ali' = %+v, want 2 matches", prefix)
	}
	if prefix[0].Name != "Alice Dice" || prefix[1].Name != "Alina Cards" {
		t.Fatalf("prefix ordering = %+v", prefix)
	}
	gameFiltered := s.Search("ali", "justdice", 0, 20)
	if len(gameFiltered) != 1 || gameFiltered[0].Name != "Alice Dice" {
		t.Fatalf("game filter = %+v", gameFiltered)
	}
	levelFiltered := s.Search("ali", "", 5, 20)
	if len(levelFiltered) != 1 || levelFiltered[0].Name != "Alina Cards" {
		t.Fatalf("level filter = %+v", levelFiltered)
	}
	all := s.Search("", "", 0, 2)
	if len(all) != 2 {
		t.Fatalf("empty q with limit 2 = %+v", all)
	}
	byGame := s.Search("", "justdice", 0, 20)
	for _, p := range byGame {
		if p.GameID != "justdice" {
			t.Fatalf("game-filtered search returned %+v", p)
		}
	}
	if len(byGame) == 0 {
		t.Fatalf("expected justdice players")
	}
}

func TestLoyaltyConcurrentEarn(t *testing.T) {
	s := NewLoyaltyService()
	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("evt_conc_%d", i)
			if _, _, _, err := s.Earn(id, id, "user_conc", "justdice", 10); err != nil {
				t.Errorf("concurrent earn %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := s.Balance("user_conc"); got != int64(n*10) {
		t.Fatalf("final balance = %d, want %d", got, n*10)
	}
	s2 := NewLoyaltyService()
	var wg2 sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			_, _, _, _ = s2.Earn("evt_same", "evt_same", "user_same", "justdice", 25)
		}()
	}
	wg2.Wait()
	if got := s2.Balance("user_same"); got != 25 {
		t.Fatalf("same-event concurrent balance = %d, want 25 (single credit)", got)
	}
	if got := len(s2.Audit()); got != 1 {
		t.Fatalf("audit entries = %d, want 1", got)
	}
}

func TestLoyaltyAudit(t *testing.T) {
	s := NewLoyaltyService()
	if _, _, _, err := s.Earn("", "a1", "user_001", "justdice", 100); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Earn("", "a1", "user_001", "justdice", 100); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Redeem("", "a2", "user_001", "justdice", 40); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Redeem("", "a3", "user_001", "justdice", 9999); err == nil {
		t.Fatalf("expected insufficient redeem error")
	}
	audit := s.Audit()
	if len(audit) != 2 {
		t.Fatalf("audit = %+v, want 2 entries (no replay/failure entries)", audit)
	}
	if audit[0].EventID != "a1" || audit[0].Type != "earn" || audit[1].EventID != "a2" || audit[1].Type != "redeem" {
		t.Fatalf("audit order/content = %+v", audit)
	}
}

func TestLoyaltyHTTPFlow(t *testing.T) {
	srv := httptest.NewServer(newServer(NewStore()))
	defer srv.Close()

	post := func(path, key string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, out := post("/v1/loyalty/earn", "", map[string]any{"event_id": "h1", "user_id": "user_001", "game_id": "justdice", "points": 250})
	if code != http.StatusCreated || out["balance"].(float64) != 250 {
		t.Fatalf("earn: code=%d out=%v", code, out)
	}
	code, out = post("/v1/loyalty/earn", "", map[string]any{"event_id": "h1", "user_id": "user_001", "game_id": "justdice", "points": 250})
	if code != http.StatusOK || out["balance"].(float64) != 250 || out["replayed"].(bool) != true {
		t.Fatalf("earn replay: code=%d out=%v", code, out)
	}
	code, out = post("/v1/loyalty/redeem", "", map[string]any{"event_id": "h2", "user_id": "user_001", "game_id": "justdice", "points": 50})
	if code != http.StatusCreated || out["balance"].(float64) != 200 {
		t.Fatalf("redeem: code=%d out=%v", code, out)
	}

	resp, err := http.Get(srv.URL + "/v1/loyalty/balance?user_id=user_001")
	if err != nil {
		t.Fatal(err)
	}
	var bal map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&bal)
	resp.Body.Close()
	if bal["balance"].(float64) != 200 {
		t.Fatalf("balance endpoint = %v", bal)
	}

	resp, err = http.Get(srv.URL + "/v1/loyalty/leaderboard?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	var board []Balance
	_ = json.NewDecoder(resp.Body).Decode(&board)
	resp.Body.Close()
	if len(board) == 0 || board[0].UserID != "user_001" || board[0].Balance != 200 {
		t.Fatalf("leaderboard endpoint = %+v", board)
	}

	resp, err = http.Get(srv.URL + "/v1/search/players?q=alice")
	if err != nil {
		t.Fatal(err)
	}
	var players []PlayerIndex
	_ = json.NewDecoder(resp.Body).Decode(&players)
	resp.Body.Close()
	if len(players) != 1 || players[0].Name != "Alice Dice" {
		t.Fatalf("search endpoint = %+v", players)
	}

	resp, err = http.Get(srv.URL + "/v1/loyalty/balance")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("balance without user_id status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}
