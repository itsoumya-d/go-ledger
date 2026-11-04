# Runbook — ledger-platform

## Service level objectives

| SLI | Target | Measurement |
|---|---|---|
| Availability | 99.9% | `up{job="go-ledger"}` over 30 days |
| p99 latency | < 250 ms | `ledger_http_request_duration_seconds` |
| Error rate | < 0.5% | `ledger_http_requests_total{status=~"5.."}` |
| Audit durability | 100% | PostgreSQL audit row count vs emitted events |

## Alerts

- **LedgerDown:** `up{job="go-ledger"} == 0` for 2 minutes.
- **HighErrorRate:** 5xx ratio > 1% for 5 minutes.
- **LatencyBurn:** p99 > 250 ms for 10 minutes.
- **EventBusBacklog:** Pub/Sub subscription backlog > 1,000 for 5 minutes.
- **AuditWriteFailure:** audit insert errors > 0 for 5 minutes.

## On-call first 10 minutes

1. Check `/healthz` and `/metrics` on the affected pod.
2. Check the event bus backlog and the audit table.
3. Roll back the last deployment if the error rate started at deploy time.
4. If the ledger is inconsistent, freeze payment creation and reconcile postings.

## Vulnerability and patching workflow

- Dependabot/ Renovate opens dependency PRs weekly.
- `govulncheck` runs in CI on every push.
- Container images are rebuilt weekly and scanned with Trivy.
- Critical CVEs are patched within 24 hours; high within 7 days.
- Windows/Linux platform patching is handled by the platform team's maintenance
  window; this service runs as a non-root, read-only-root-filesystem container.

## Recovery

- The service is stateless; the ledger state is in PostgreSQL.
- Replay events from the bus into a new audit store if the projection is lost.
- Restore PostgreSQL from the latest PITR snapshot and replay idempotent events.
