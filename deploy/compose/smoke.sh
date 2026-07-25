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

# Full chain asserted via grex metrics: three collectors connect through the
# OpAMP gateway, grex answers their connect delegations and registers each
# agent.
metric() {
    curl -s http://127.0.0.1:9090/metrics/fleet |
        awk -v name="$1" 'index($0, name) == 1 {print $NF; exit}'
}

wait_metric() {
    name="$1" op="$2" want="$3"
    for _ in $(seq 1 30); do
        got=$(metric "$name")
        if [ -n "$got" ]; then
            case "$op" in
            eq) [ "${got%.*}" -eq "$want" ] && return 0 ;;
            ge) [ "${got%.*}" -ge "$want" ] && return 0 ;;
            esac
        fi
        sleep 2
    done
    fail "$name: want $op $want, got ${got:-absent}"
}

wait_metric 'grex_agents_connected{transport="ws",via="gateway"}' eq 3
wait_metric 'grex_gateway_connections' eq 2
wait_metric 'grex_gateway_connects_total{result="accepted"}' ge 3
wait_metric 'grex_agents_noncompliant' eq 0
wait_metric 'grex_agent_reports_total{type="status"}' ge 3

# Prometheus scrapes both grex endpoints as separate healthy jobs, plus the
# three collectors' internal telemetry.
for _ in $(seq 1 30); do
    up=$(curl -s http://127.0.0.1:9091/api/v1/targets |
        python3 -c "import json,sys; d=json.load(sys.stdin); print(sum(1 for t in d['data']['activeTargets'] if t['health']=='up' and t['labels']['job'] in ('grex-server','grex-fleet','otelcol')))" 2>/dev/null || echo 0)
    [ "$up" = 5 ] && break
    sleep 2
done
[ "$up" = 5 ] || fail "prometheus targets up: want 5, got ${up:-0}"

for svc in otelcol-agent-1 otelcol-agent-2 otelcol-gateway; do
    docker compose logs --no-color "$svc" | grep -ic opamp > /dev/null ||
        fail "$svc logs show no OpAMP activity"
done

echo "smoke test passed"
