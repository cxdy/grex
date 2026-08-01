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
# across both replicas. Distribution across replicas is reported, not
# asserted — see the note further down.
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

# Report (don't hard-fail on) distribution across the two replicas.
# opamp-gateway opens its 2 upstream connections microseconds apart —
# tighter than a TCP handshake, so Envoy's LEAST_REQUEST active-connection
# count (only incremented once a connection is established, not at
# selection time) sometimes has no signal yet for the second pick and both
# land on the same backend. Confirmed via opamp-gateway's own logs
# (~80us between the two "connecting to upstream" lines) and isolated with
# synthetic staggered-vs-simultaneous probes through the same Envoy — not
# fixable by LB config (concurrency/health-check/DNS tuning already fixed
# two separate real races upstream of this one), it's a small-N tie
# inherent to any current-load LB facing near-zero-delta near-simultaneous
# arrivals. Deliberately not working around it here by switching to
# ROUND_ROBIN (would give up LEAST_REQUEST's "new replica gets more until
# it catches up" property, the actual reason it was chosen for production)
# or bumping opamp-gateway.yaml's connections beyond 2 (would only mask
# the race behind more trials, not demonstrate anything different). See
# docs/spec/design.md's Load balancing section.
g1=$(metric_at "127.0.0.1:9090" 'grex_agents_connected{transport="ws",via="gateway"}')
g2=$(metric_at "127.0.0.1:9092" 'grex_agents_connected{transport="ws",via="gateway"}')
if [ "${g1:-0}" = "0" ] || [ "${g2:-0}" = "0" ]; then
    echo "NOTE: all agents landed on one grex replica this run (grex=${g1:-0}, grex-2=${g2:-0})." \
        "Known small-N startup race, not a failure — see comment above." >&2
else
    echo "gateway-relayed agents split across replicas: grex=$g1, grex-2=$g2"
fi

# With persistence wired (deploy/compose/grex.yaml's database: block), each
# replica's GET /api/agents should show the full fleet via the cross-replica
# DB merge (MergeAgents), not just the subset it personally holds a live
# connection to. Retries: the write path is a batched flush
# (persistenceFlushInterval, 5s in cmd/grex/main.go), not synchronous.
agents_total_partial() {
    host="$1"
    scripts/gxcurl -u alice -s "https://$host/api/agents?limit=10" |
        python3 -c "import json,sys; d=json.load(sys.stdin); print(d['total'], d['partial'])" 2>/dev/null
}

for host in 127.0.0.1:8080 127.0.0.1:8081; do
    ok=
    for _ in $(seq 1 30); do
        result=$(agents_total_partial "$host")
        total=${result%% *}
        partial=${result##* }
        if [ "$total" = "3" ] && [ "$partial" = "False" ]; then
            ok=1
            break
        fi
        sleep 2
    done
    [ -n "$ok" ] || fail "$host: GET /api/agents total/partial = ${result:-absent}, want 3/False (cross-replica DB merge)"
done
echo "cross-replica DB merge confirmed: both replicas report the full fleet via GET /api/agents"

# supervisor_managed (docs/spec/design.md's known-gap fix, see
# deploy/compose/opamp-supervisor-build/Dockerfile's temporary fork build)
# depends on the OpAMP Supervisor's opamp.managed_by non-identifying
# attribute surviving relay through opamp-gateway: otelcol-agent-2 (the one
# Supervisor-managed agent) connects via the gateway same as agent-1 and
# otelcol-gateway (compose.yaml), not directly to grex. want=1: only
# agent-2 runs the Supervisor; agent-1/otelcol-gateway run the bare opamp
# extension and must not match. A gateway that silently dropped the
# attribute would show 0 here, not 1.
supervisor_managed_total() {
    host="$1"
    scripts/gxcurl -u alice -s "https://$host/api/agents?supervisor_managed=true&limit=10" |
        python3 -c "import json,sys; print(json.load(sys.stdin)['total'])" 2>/dev/null
}

for host in 127.0.0.1:8080 127.0.0.1:8081; do
    ok=
    for _ in $(seq 1 30); do
        total=$(supervisor_managed_total "$host")
        if [ "$total" = "1" ]; then
            ok=1
            break
        fi
        sleep 2
    done
    [ -n "$ok" ] || fail "$host: GET /api/agents?supervisor_managed=true total = ${total:-absent}, want 1 (opamp.managed_by survived the gateway relay)"
done
echo "supervisor_managed confirmed: opamp.managed_by survives relay through opamp-gateway"

# River UI is mounted only when database.host is set (deploy/compose/
# grex.yaml sets it) — reuses the purge job's own River client. Same
# mTLS-gated UI listener as everything else (server.go wraps the whole
# uiMux once), so it needs a client cert same as any other UI route; no
# separate riverui-specific auth mechanism was added.
code=$(scripts/gxcurl -u alice -s -o /dev/null -w '%{http_code}' https://127.0.0.1:8080/riverui/)
[ "$code" = "200" ] || fail "grex /riverui/: want 200, got $code"
code=$(curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:8080/riverui/)
[ "$code" = "403" ] || fail "grex /riverui/ with no cert: want 403, got $code"

# POST /api/jobs: the one mutating endpoint today (docs/developer/read-api.md).
# No role enforcement exists yet — any identity can create a job — so this
# only proves the endpoint round-trips through a real Postgres, not an auth
# check. Created in "planned" status; nothing dispatched (arm/dispatch
# aren't built yet), so no otelcol-agent-* container is actually restarted.
job_status=$(scripts/gxcurl -u admin -s -X POST https://127.0.0.1:8080/api/jobs \
    -H 'Content-Type: application/json' \
    -d '{"filter":"service.name=otelcol-contrib","action":"restart","submitted_by":"smoke-test"}' |
    python3 -c "import json,sys; print(json.load(sys.stdin)['status'])" 2>/dev/null)
[ "$job_status" = "planned" ] || fail "POST /api/jobs: want status=planned, got ${job_status:-absent}"
echo "POST /api/jobs confirmed: job created in planned status"

# grex-browser (deploy/compose/grex-browser.yaml) has no ui_tls at all:
# plain HTTP, no client cert needed, not even -k for a self-signed cert.
# Not part of the Envoy/gateway pool, but shares the same Postgres, so it
# shows the same fleet via the cross-replica DB merge.
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8082/api/status)
[ "$code" = "200" ] || fail "grex-browser /api/status (plain http, no cert): want 200, got $code"
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8082/riverui/)
[ "$code" = "200" ] || fail "grex-browser /riverui/ (plain http, no cert): want 200, got $code"

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
