package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresAuditStore struct {
	db *sql.DB
}

func NewPostgresAuditStore(dsn string) (*PostgresAuditStore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return &PostgresAuditStore{db: db}, nil
}

func (s *PostgresAuditStore) Init(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS ledger_audit_events (
			id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			payload JSONB NOT NULL,
			occurred_at TIMESTAMPTZ NOT NULL
		)
	`)
	return err
}

func (s *PostgresAuditStore) Append(ctx context.Context, event Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO ledger_audit_events (id, event_type, payload, occurred_at) VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING`,
		event.ID, event.Type, payload, event.Time,
	)
	return err
}

func (s *PostgresAuditStore) List(ctx context.Context, limit int) ([]Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, event_type, payload, occurred_at FROM ledger_audit_events ORDER BY occurred_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var event Event
		var payload []byte
		var occurred time.Time
		if err := rows.Scan(&event.ID, &event.Type, &payload, &occurred); err != nil {
			return nil, err
		}
		event.Time = occurred.UTC()
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *PostgresAuditStore) Close() error { return s.db.Close() }
