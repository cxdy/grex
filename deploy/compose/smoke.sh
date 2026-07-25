#!/usr/bin/env bash
# Smoke test for the compose dev stack. Asserts grex and Dex answer their
# health endpoints and that every collector is attempting OpAMP connections
# to grex. Exits non-zero on the first failure.
set -euo pipefail

cd "$(dirname "$0")/../.."

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

echo "waiting for services to settle..."
for _ in $(seq 1 30); do
    unhealthy=$(docker compose ps --format '{{.Service}} {{.Health}}' |
        awk '$2 != "" && $2 != "healthy" {print $1}')
    [ -z "$unhealthy" ] && break
    sleep 2
done
[ -z "${unhealthy:-}" ] || fail "services not healthy: $unhealthy"

curl -fsS http://127.0.0.1:9090/healthz | grep -q ok || fail "grex /healthz"
curl -fsS http://127.0.0.1:9090/readyz | grep -q ok || fail "grex /readyz"
curl -fsS http://127.0.0.1:9090/metrics | grep -q '^go_' || fail "grex /metrics has no go_ series"
curl -fsS http://127.0.0.1:5556/dex/.well-known/openid-configuration |
    grep -q '"issuer"' || fail "dex openid-configuration"

# Full chain: three collectors connect through the OpAMP gateway, grex
# answers their connect delegations and registers each agent.
# grep -c reads the whole stream, avoiding a SIGPIPE to docker under
# pipefail; -q would exit at the first match and break the pipeline.
wait_for_count() {
    label="$1" want="$2" svc="$3" pattern="$4"
    for _ in $(seq 1 30); do
        got=$(docker compose logs --no-color "$svc" | grep -c "$pattern" || true)
        [ "$got" -ge "$want" ] && return 0
        sleep 2
    done
    fail "$label: want >= $want, got ${got:-0}"
}

wait_for_count "gateway connects accepted by grex" 3 grex "gateway connect accepted"
wait_for_count "agents registered via gateway" 3 grex '"agent registered".*"via_gateway":true'
wait_for_count "gateway authentication results" 3 opamp-gateway "authentication result"

for svc in otelcol-agent-1 otelcol-agent-2 otelcol-gateway; do
    docker compose logs --no-color "$svc" | grep -ic opamp > /dev/null ||
        fail "$svc logs show no OpAMP activity"
done

echo "smoke test passed"
