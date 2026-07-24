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

# The OpAMP endpoint is a stub until the protocol milestone, so a completed
# session is not expected; each collector must at least be dialing grex.
for svc in otelcol-agent-1 otelcol-agent-2 otelcol-gateway; do
    # grep -c reads the whole stream, avoiding a SIGPIPE to docker under
    # pipefail; -q would exit at the first match and break the pipeline.
    docker compose logs --no-color "$svc" | grep -ic opamp > /dev/null ||
        fail "$svc logs show no OpAMP activity"
done

echo "smoke test passed"
