# Observability

`go-ledger` exposes Prometheus metrics at `/metrics` and a Kubernetes
`ServiceMonitor` in `k8s/servicemonitor.yaml`.

Dashboard coverage:

| Signal | Metric | SLO |
|---|---|---|
| Request rate | `ledger_http_requests_total` | — |
| Error rate | `ledger_http_requests_total{status=~"5.."}` | < 0.5% |
| p99 latency | `ledger_http_request_duration_seconds` | < 250 ms |
| Event backlog | `gcp.pubsub.subscription.num_undelivered_messages` | < 1,000 for 5m |
| Audit writes | `postgresql.rows_inserted{db="ledger"}` | 100% of events |

`datadog-dashboard.json` is a portable dashboard definition for teams that use
Datadog instead of the Prometheus/Grafana stack. The metric names match the
Prometheus exporter; map them to the Datadog agent's OpenMetrics check or to the
`go-ledger` service check.
