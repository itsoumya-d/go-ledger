#!/usr/bin/env bash
set -euo pipefail

echo "postgres:"
PGPASSWORD=postgres psql -h 127.0.0.1 -U postgres -d ledger -c "SELECT 1 AS postgres_ok;"

echo "mysql:"
mysql -h 127.0.0.1 -uroot -proot ledger -e "SELECT 1 AS mysql_ok;"

echo "matrix smoke test passed"
