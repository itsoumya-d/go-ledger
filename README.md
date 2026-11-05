# go-ledger

**Live page:** https://itsoumya-d.github.io/go-ledger/

**A compact, tested Go service for idempotent payment intents, double-entry ledger postings, Open Banking flows, and telco usage rating.**

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

33 Go test functions (42 cases including validation subtests) cover idempotency, double-entry
postings, insufficient funds, concurrent capture, the HTTP flow, validation, Open Banking
consent/payment/idempotency and GET endpoints, GraphQL, Prometheus, the event bus, PostgreSQL audit
store, tiered rating, proration, tax rounding, invoice idempotency and ledger posting. Local
statement coverage is **63.6%**; CI fails below 56%. The coverage number is deliberately published as a floor, not a quality claim:
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
