#!/usr/bin/env bash
# scripts/smoke.sh: runtime smoke verification against a started go-ledger instance
set -euo pipefail

PORT="${PORT:-8899}"
BIN="./bin/go-ledger-smoke"

mkdir -p bin
echo "==> Building go-ledger for smoke test..."
go build -o "$BIN" .

echo "==> Starting go-ledger on port ${PORT}..."
PORT="$PORT" "$BIN" &
SERVER_PID=$!

cleanup() {
    echo "==> Cleaning up server PID ${SERVER_PID}..."
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    rm -f "$BIN"
}
trap cleanup EXIT

echo "==> Waiting for service to become healthy..."
READY=0
for i in $(seq 1 30); do
    if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 0.2
done

if [ "$READY" -ne 1 ]; then
    echo "ERROR: Server failed to start and respond to /healthz within 6s"
    exit 1
fi

echo "==> Validating /healthz..."
HEALTH_RESP=$(curl -sf "http://127.0.0.1:${PORT}/healthz")
echo "$HEALTH_RESP" | grep -q '"status":"ok"' || {
    echo "ERROR: Unexpected /healthz response: $HEALTH_RESP"
    exit 1
}

echo "==> Validating /v1/plans..."
PLANS_RESP=$(curl -sf "http://127.0.0.1:${PORT}/v1/plans")
echo "$PLANS_RESP" | grep -q '"id":' || {
    echo "ERROR: Unexpected /v1/plans response: $PLANS_RESP"
    exit 1
}

echo "==> Smoke test passed successfully."
