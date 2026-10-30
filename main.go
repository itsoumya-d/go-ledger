package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
)

type fundRequest struct {
	AmountCents int64 `json:"amount_cents"`
}

type createIntentRequest struct {
	AccountID   string `json:"account_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrIdempotencyConflict):
		status = http.StatusConflict
	case errors.Is(err, ErrInsufficientFunds):
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func newServer(store *Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/accounts/{accountID}/fund", func(w http.ResponseWriter, r *http.Request) {
		var req fundRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		if err := store.Fund(r.PathValue("accountID"), req.AmountCents); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":    r.PathValue("accountID"),
			"balance_cents": store.Balance(r.PathValue("accountID")),
		})
	})

	mux.HandleFunc("POST /v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		var req createIntentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		intent, created, err := store.CreateIntent(r.Header.Get("Idempotency-Key"), req.AccountID, req.AmountCents, req.Currency)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, intent)
	})

	mux.HandleFunc("POST /v1/payment_intents/{intentID}/capture", func(w http.ResponseWriter, r *http.Request) {
		intent, err := store.Capture(r.PathValue("intentID"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, intent)
	})

	mux.HandleFunc("GET /v1/accounts/{accountID}/balance", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":    r.PathValue("accountID"),
			"balance_cents": store.Balance(r.PathValue("accountID")),
		})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return mux
}

func main() {
	store := NewStore()
	_ = store.Fund("customer_demo", 10_000)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("go-ledger listening on :%s", port)
	if err := http.ListenAndServe(":"+port, newServer(store)); err != nil {
		log.Fatal(err)
	}
}
