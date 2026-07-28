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

# /healthz and /readyz stay open on the telemetry listener with no client
# cert at all, even though the listener terminates TLS.
curl -fsSk https://127.0.0.1:9090/healthz | grep -q ok || fail "grex /healthz"
curl -fsSk https://127.0.0.1:9090/readyz | grep -q ok || fail "grex /readyz"

# /metrics requires a client cert mapped to a role. scripts/gxcurl supplies
# the cert/key/-k boilerplate; see docs/developer/testing.md.
scripts/gxcurl -u prometheus -fsS https://127.0.0.1:9090/metrics |
    grep -q '^go_' || fail "grex /metrics has no go_ series"
code=$(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:9090/metrics)
[ "$code" = "403" ] || fail "grex /metrics without a client cert: want 403, got $code"

# UI listener: mTLS auth matrix from deploy/compose/grex.yaml's role_mapping.
ui_matrix() {
    name="$1" identity="$2" want="$3"
    got=$(scripts/gxcurl -u "$identity" -s -o /dev/null -w '%{http_code}' https://127.0.0.1:8080/api/status)
    [ "$got" = "$want" ] || fail "$name: want $want, got $got"
}
code=$(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:8080/api/status)
[ "$code" = "403" ] || fail "grex UI with no cert: want 403, got $code"
ui_matrix "wrong-role cert (unmapped SPIFFE ID)" user-mallory 403
ui_matrix "viewer cert" user-alice 200
ui_matrix "admin cert" user-admin 200

curl -fsS http://127.0.0.1:5556/dex/.well-known/openid-configuration |
    grep -q '"issuer"' || fail "dex openid-configuration"

# Full chain asserted via grex metrics: three collectors connect through the
# OpAMP gateway, grex answers their connect delegations and registers each
# agent.
metric() {
    scripts/gxcurl -u prometheus -s https://127.0.0.1:9090/metrics/fleet |
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
wait_metric 'grex_agents_awaiting_full_state' eq 0

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
