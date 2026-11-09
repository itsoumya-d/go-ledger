package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type createRatedInvoiceRequest struct {
	AccountID   string `json:"account_id"`
	PlanID      string `json:"plan_id"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	UsageUnits  int64  `json:"usage_units"`
	ActiveDays  int    `json:"active_days"`
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
	case errors.Is(err, ErrUnauthorized):
		status = http.StatusUnauthorized
	case errors.Is(err, ErrConsentNotFound), errors.Is(err, ErrPaymentNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConsentNotAuthorised):
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func newServer(store *Store) http.Handler {
	bus := NewEventBusFromEnv(context.Background())
	var audit *PostgresAuditStore
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		if store, err := NewPostgresAuditStore(dsn); err == nil {
			if err := store.Init(context.Background()); err == nil {
				audit = store
				log.Printf("audit store: PostgreSQL enabled")
			} else {
				log.Printf("audit store: PostgreSQL init failed: %v", err)
			}
		} else {
			log.Printf("audit store: PostgreSQL unavailable: %v", err)
		}
	}
	openBanking := NewOpenBankingService(store, bus, audit)
	rating := NewRatingService()
	loyalty := NewLoyaltyService()
	metrics := NewMetrics()

	mux := http.NewServeMux()
	registerLoyaltyRoutes(mux, loyalty)

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

	mux.HandleFunc("GET /v1/plans", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, rating.Plans())
	})

	mux.HandleFunc("POST /v1/rated_invoices", func(w http.ResponseWriter, r *http.Request) {
		var req createRatedInvoiceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, ErrInvalid)
			return
		}
		invoice, created, err := rating.CreateInvoice(
			r.Header.Get("Idempotency-Key"),
			req.AccountID,
			req.PlanID,
			req.PeriodStart,
			req.PeriodEnd,
			req.UsageUnits,
			req.ActiveDays,
		)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, invoice)
	})

	mux.HandleFunc("GET /v1/rated_invoices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, rating.Invoices(r.URL.Query().Get("account_id")))
	})

	mux.HandleFunc("GET /v1/rated_invoices/{invoiceID}", func(w http.ResponseWriter, r *http.Request) {
		invoice, ok := rating.Invoice(r.PathValue("invoiceID"))
		if !ok {
			writeError(w, fmt.Errorf("%w: rated invoice", ErrNotFound))
			return
		}
		writeJSON(w, http.StatusOK, invoice)
	})

	mux.HandleFunc("POST /v1/rated_invoices/{invoiceID}/capture", func(w http.ResponseWriter, r *http.Request) {
		invoice, err := rating.CaptureInvoice(r.PathValue("invoiceID"), store)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, invoice)
	})

	mux.Handle("/graphql", graphQLHandler(store))
	mux.Handle("/openbanking/", openBankingHandler(openBanking))
	mux.Handle("/metrics", metrics.Handler())

	return metrics.Middleware(mux)
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
