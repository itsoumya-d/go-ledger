# CI and database matrix

`go-ledger` runs four CI lanes on every push:

| Lane | What it proves |
|---|---|
| `test` | `go test -race`, `go vet`, `go build`, `govulncheck` |
| `docker` | the multi-stage image builds |
| `terraform` | GCP, Azure and AWS reference stacks format and validate |
| `web` | the React/Redux/d3 control plane typechecks and builds |
| `web-e2e` | Cypress runs against the built preview server |
| `db-matrix` | PostgreSQL and MySQL service containers accept queries |

## Database matrix

The local matrix is in `ci/db-matrix/`:

```bash
docker compose -f ci/db-matrix/docker-compose.yml up -d
./ci/db-matrix/smoke.sh
docker compose -f ci/db-matrix/docker-compose.yml down -v
```

CI runs the same checks with GitHub Actions service containers, so a broken
PostgreSQL or MySQL client path fails before a release.

Production CI also targets the analytical warehouses used by Metabase's test
matrix:

| Warehouse | How it is exercised |
|---|---|
| PostgreSQL | service container + `psql` smoke query |
| MySQL | service container + `mysql` smoke query |
| Snowflake | warehouse credentials in the protected CI environment; `SELECT 1` and a dialect-specific type check |
| BigQuery | workload identity federation; `SELECT 1` and a dry-run query |
| Redshift | IAM role; `SELECT 1` and a distribution-key check |

Cloud warehouse jobs are gated on the protected environment so they do not run
on forked pull requests. The local containers keep the fast feedback loop.
