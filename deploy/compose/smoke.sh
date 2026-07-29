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
# OpAMP gateway, which multiplexes over 2 upstream connections that Envoy
# least-connections balances across both grex replicas (grex, grex-2 —
# deploy/compose/envoy.yaml). Metrics are per-process, so checks below sum
# across both replicas, plus a distribution check that neither replica
# ends up with zero agents.
GREX_HOSTS="127.0.0.1:9090 127.0.0.1:9092"

metric_at() {
    host="$1" name="$2"
    scripts/gxcurl -u prometheus -s "https://$host/metrics/fleet" |
        awk -v name="$name" 'index($0, name) == 1 {print $NF; exit}'
}

metric_sum() {
    name="$1"
    total=0 any=
    for host in $GREX_HOSTS; do
        got=$(metric_at "$host" "$name")
        [ -n "$got" ] || continue
        any=1
        total=$(awk -v t="$total" -v g="$got" 'BEGIN{printf "%d", t+g}')
    done
    [ -n "$any" ] && echo "$total"
}

wait_metric_sum() {
    name="$1" op="$2" want="$3"
    for _ in $(seq 1 30); do
        got=$(metric_sum "$name")
        if [ -n "$got" ]; then
            case "$op" in
            eq) [ "$got" -eq "$want" ] && return 0 ;;
            ge) [ "$got" -ge "$want" ] && return 0 ;;
            esac
        fi
        sleep 2
    done
    fail "$name (summed across grex, grex-2): want $op $want, got ${got:-absent}"
}

wait_metric_sum 'grex_agents_connected{transport="ws",via="gateway"}' eq 3
wait_metric_sum 'grex_gateway_connections' eq 2
wait_metric_sum 'grex_gateway_connects_total{result="accepted"}' ge 3
wait_metric_sum 'grex_agents_noncompliant' eq 0
wait_metric_sum 'grex_agent_reports_total{type="status"}' ge 3
wait_metric_sum 'grex_agents_awaiting_full_state' eq 0

# Least-connections actually distributed the 2 upstream connections across
# both replicas: neither grex nor grex-2 should be sitting at zero
# gateway-relayed agents.
g1= g2=
for _ in $(seq 1 30); do
    g1=$(metric_at "127.0.0.1:9090" 'grex_agents_connected{transport="ws",via="gateway"}')
    g2=$(metric_at "127.0.0.1:9092" 'grex_agents_connected{transport="ws",via="gateway"}')
    [ -n "$g1" ] && [ -n "$g2" ] && [ "${g1%.*}" -ge 1 ] && [ "${g2%.*}" -ge 1 ] && break
    sleep 2
done
[ -n "$g1" ] && [ "${g1%.*}" -ge 1 ] || fail "grex: want >=1 gateway-relayed agent, got ${g1:-absent}"
[ -n "$g2" ] && [ "${g2%.*}" -ge 1 ] || fail "grex-2: want >=1 gateway-relayed agent, got ${g2:-absent}"

# Prometheus scrapes both grex replicas as separate targets per job (2 each
# for grex-server/grex-fleet), the three collectors' internal telemetry,
# and Envoy's stats endpoint.
for _ in $(seq 1 30); do
    up=$(curl -s http://127.0.0.1:9091/api/v1/targets |
        python3 -c "import json,sys; d=json.load(sys.stdin); print(sum(1 for t in d['data']['activeTargets'] if t['health']=='up' and t['labels']['job'] in ('grex-server','grex-fleet','otelcol','envoy')))" 2>/dev/null || echo 0)
    [ "$up" = 8 ] && break
    sleep 2
done
[ "$up" = 8 ] || fail "prometheus targets up: want 8, got ${up:-0}"

for svc in otelcol-agent-1 otelcol-agent-2 otelcol-gateway; do
    docker compose logs --no-color "$svc" | grep -ic opamp > /dev/null ||
        fail "$svc logs show no OpAMP activity"
done

echo "smoke test passed"
