# go-ledger

**Live page:** https://itsoumya-d.github.io/go-ledger/

**A compact, tested Go service for idempotent payment intents, double-entry ledger postings, Open Banking flows, telco usage rating, and gaming-loyalty rewards.**

This is a reference implementation, not a production payment system. It exists to make the
financial-correctness ideas concrete: idempotency keys, double-entry postings, insufficient-funds
handling, capture-once semantics under concurrency, consent-bound payments, and an auditable event
trail.

## What it proves

- **Idempotent creation:** replaying the same `Idempotency-Key` and payload returns the original
  intent; reusing the key with a different payload returns `409`.
- **Double-entry postings:** capture writes a debit and a credit that sum to zero.
- **Capture exactly once:** concurrent captures produce one posting pair, not ten.
- **Insufficient funds:** a failed capture leaves balances and postings untouched.
- **Open Banking consent flow:** a payment requires an authorised consent, a bearer token, and an
  idempotency key; a replayed payment returns the original payment.
- **Audit trail:** every consent and payment emits a typed event to an in-memory, GCP Pub/Sub, or
  Azure Service Bus bus; PostgreSQL persists the same events when `DATABASE_URL` is set.
- **Observability:** `/metrics` exposes Prometheus counters and latency histograms.
- **Query surface:** `/graphql` exposes intents, balances, postings, and mutations.
- **Usage rating:** marginal tiers, included allowance, proration, GST and idempotent invoice generation; invoices post to the double-entry ledger exactly once.
- **Transactional Outbox & DLQ:** payment captures and invoice settlements atomically enqueue typed domain events into a transactional outbox with at-least-once delivery, exponential-backoff retries, dead-letter queue (DLQ) quarantine, manual replay endpoints, and Prometheus metrics (`ledger_outbox_*`).
- **Gaming loyalty:** idempotent point earn/redeem (by `event_id` / `Idempotency-Key`), derived balances, deterministic leaderboard, and prefix search over an in-memory player projection; every earn/redeem appends an audit entry.
- **Deployment evidence:** Kubernetes manifests (HPA, PDB, NetworkPolicy, ServiceMonitor) and
  Terraform reference stacks for GCP, Azure, and AWS.

## API

```bash
# fund an account (dev helper)
curl -s -X POST localhost:8080/v1/accounts/customer_a/fund \
  -H 'Content-Type: application/json' \
  -d '{"amount_cents":1000}'

# create an intent (idempotent)
curl -s -X POST localhost:8080/v1/payment_intents \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: order-42' \
  -d '{"account_id":"customer_a","amount_cents":400,"currency":"USD"}'

# capture
curl -s -X POST localhost:8080/v1/payment_intents/pi_1/capture

# balance
curl -s localhost:8080/v1/accounts/customer_a/balance
```

### Open Banking

```bash
# consent
curl -s -X POST localhost:8080/openbanking/v1/consents \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"account_id":"customer_demo","permissions":["ReadAccounts","WritePayments"]}'

# payment (idempotent)
curl -s -X POST localhost:8080/openbanking/v1/payments \
  -H 'Authorization: Bearer dev-token' \
  -H 'Idempotency-Key: payment-1' \
  -H 'Content-Type: application/json' \
  -d '{"consent_id":"consent_1","account_id":"customer_demo","amount_cents":400,"currency":"USD"}'

# graphql
curl -s -X POST localhost:8080/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ balance(accountId:\"customer_demo\") }"}'

# metrics
curl -s localhost:8080/metrics | head
```

The Open Banking security model, FAPI/mTLS notes, and event flow are documented in
[`docs/openbanking.md`](docs/openbanking.md). The SLO, alerting, patching, and recovery notes are in
[`docs/runbook.md`](docs/runbook.md).

## Usage rating

The rating layer models wholesale telco billing:

```text
billable = max(0, usage_units - plan.included_units)
usage    = sum over marginal tiers of units_in_tier * tier.unit_price_cents
base     = round(plan.base_monthly_cents * active_days / days_in_month)
subtotal = base + usage
tax      = round(subtotal * plan.tax_rate_bps / 10000)
total    = subtotal + tax
```

Invoices are idempotent by `Idempotency-Key`. `POST /v1/rated_invoices/{id}/capture`
creates and captures a payment intent for the invoice total, so the invoice reaches
the double-entry ledger exactly once.

```bash
curl -s -X POST localhost:8080/v1/rated_invoices \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: invoice-nov-2026' \
  -d '{"account_id":"customer_demo","plan_id":"wholesale_100","period_start":"2026-11-01","period_end":"2026-11-30","usage_units":1200,"active_days":30}'
```

## Gaming loyalty

Rewards, idempotency, leaderboard, and search. Earn/redeem are idempotent by
`event_id` (or the `Idempotency-Key` header when `event_id` is omitted): a replay
returns the stored balance with no double-credit, and reusing an id with a
different payload returns `409`. Points must be positive; redeeming more than
the balance returns `422` and leaves the balance untouched. The leaderboard is
sorted by balance descending with `user_id` ascending as a deterministic
tie-break. Player search does a case-insensitive prefix match on name with
optional `game_id` / `level` filters (empty `q` returns all players up to
`limit`). Every successful earn/redeem appends an audit entry; replays and
rejected redeems do not.

The ElasticSearch projection is an in-memory reference implementation, not a
live search cluster.

```bash
# earn points (idempotent by event_id)
curl -s -X POST localhost:8080/v1/loyalty/earn \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"evt_1","user_id":"user_001","game_id":"justdice","points":100}'
curl -s -X POST localhost:8080/v1/loyalty/earn \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"evt_1","user_id":"user_001","game_id":"justdice","points":100}'
# replay: {"event_id":"evt_1",...,"balance":100,"replayed":true} — no double-credit

# earn via Idempotency-Key header
curl -s -X POST localhost:8080/v1/loyalty/earn \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: key-earn-1' \
  -d '{"user_id":"user_001","game_id":"justdice","points":50}'

# redeem points
curl -s -X POST localhost:8080/v1/loyalty/redeem \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"redeem_1","user_id":"user_001","game_id":"justdice","points":40}'

# balance
curl -s 'localhost:8080/v1/loyalty/balance?user_id=user_001'

# leaderboard
curl -s 'localhost:8080/v1/loyalty/leaderboard?limit=5'

# player search (prefix on name, optional game/level filter)
curl -s 'localhost:8080/v1/search/players?q=alice&game_id=justdice'
curl -s 'localhost:8080/v1/search/players?q=ali&level=5'
```

## Transactional Outbox & DLQ

To prevent dual-write anomalies across distributed microservices, state mutations atomically enqueue events into a transactional outbox. A background or on-demand dispatcher flushes events to the message broker (`EventBus`) with exponential backoff and dead-letter queue (DLQ) quarantine:

```bash
# enqueue an event
curl -s -X POST localhost:8080/v1/outbox/enqueue \
  -H 'Content-Type: application/json' \
  -d '{"type":"patient.appointment_booked","payload":{"practice_id":"p101","patient_id":"pat_202"},"max_attempts":3}'

# flush pending events to the event bus
curl -s -X POST localhost:8080/v1/outbox/flush

# inspect outbox statistics (enqueued, pending, published, dead-letter, retries)
curl -s localhost:8080/v1/outbox/stats

# inspect dead-lettered events
curl -s localhost:8080/v1/outbox/dead_letters

# replay a quarantined dead-letter event
curl -s -X POST localhost:8080/v1/outbox/dead_letters/{id}/replay
```

## Architecture

```mermaid
flowchart LR
  A[POST /payment_intents] --> B[Idempotency key index]
  B --> C[PaymentIntent]
  C --> D[Capture]
  D --> E[Double-entry postings]
  E --> F[Customer balance]
  E --> G[Merchant settlement]

  H[POST /openbanking/v1/payments] --> I[Consent check]
  I --> B
  D --> J[EventBus]
  J --> K[GCP Pub/Sub]
  J --> L[Azure Service Bus]
  J --> M[PostgreSQL audit]
  D --> N[Prometheus metrics]
```

The ledger store is in-memory and guarded by a mutex. That is deliberate: the point is the
invariants, not a database integration. The PostgreSQL audit store, cloud event buses, Terraform,
and Kubernetes manifests are reference deployment evidence; they are not a claim of production
scale or live cloud credentials.

## Run

```bash
go test -race ./...
go vet ./...
go run .
```

Optional cloud wiring:

```bash
# PostgreSQL audit store
DATABASE_URL='postgres://user:pass@localhost:5432/ledger?sslmode=disable' go run .

# GCP Pub/Sub
EVENT_BUS=gcp GCP_PROJECT_ID=my-project GCP_TOPIC_ID=ledger-payments go run .

# Azure Service Bus
EVENT_BUS=azure \
  AZURE_SERVICE_BUS_CONNECTION_STRING='Endpoint=sb://ns.servicebus.windows.net/;SharedAccessKeyName=...;SharedAccessKey=...' \
  AZURE_QUEUE=payments go run .
```

## CI

GitHub Actions runs:

- `go test -race -coverprofile=coverage.out -covermode=atomic ./...` with a **56%** statement gate
- `go vet ./...`
- `go build ./...`
- `govulncheck ./...`
- Docker build
- `terraform fmt -check` and `terraform validate` for the GCP, Azure, and AWS stacks
- The PostgreSQL audit-store round trip against a real `postgres:16` service

## Test coverage

43 Go test functions (57 cases including validation subtests) cover idempotency, double-entry
postings, insufficient funds, concurrent capture, the HTTP flow, validation, Open Banking
consent/payment/idempotency and GET endpoints, GraphQL, Prometheus, the event bus, PostgreSQL audit
store, tiered rating, proration, tax rounding, invoice idempotency and ledger posting, loyalty
earn/redeem idempotency and conflict, redeem-insufficient, leaderboard ordering, search
prefix/filters, concurrent earn, audit entries, and the loyalty HTTP flow. Local
statement coverage is **68.7%**; CI fails below 56%. The coverage number is deliberately published as a floor, not a quality claim:
the in-memory ledger, cloud event-bus adapters, and `main` remain the largest uncovered areas.

## Limitations

- The ledger store is in-memory; state is lost on restart. PostgreSQL currently stores the audit
  trail, not the posting tables.
- The Open Banking bearer token is a development shim; production uses OIDC/JWKS and FAPI-grade
  mTLS binding.
- The Terraform stacks are reference modules; they are not applied to a live account in CI.
- No real money, no card data, no settlement files, no PCI scope.
- Single-currency balances; no FX.
- The dev funding endpoint is not a production API.

If you are evaluating this for a payments or platform role, the interesting parts are
`CreateIntent`, `Capture`, `OpenBankingService.CreatePayment`, `RatingService.CreateInvoice`,
`RatingService.CaptureInvoice`, the concurrency tests, and the Kubernetes/Terraform evidence —
not the HTTP wrapper.

## License

MIT. See [LICENSE](LICENSE).
