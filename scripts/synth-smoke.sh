#!/usr/bin/env bash
# Black-box smoke for cmd/grex-synth: build grex and grex-synth, start grex
# with a plaintext OpAMP listener (no TLS, no database), point grex-synth at
# it, and assert every synthetic agent connected with no failures. This
# exercises the real binaries end to end, not the in-process test harness in
# internal/synth.
set -euo pipefail

AGENTS="${AGENTS:-50}"
DURATION="${DURATION:-5s}"
OPAMP_PORT="${OPAMP_PORT:-14320}"
UI_PORT="${UI_PORT:-18080}"
TELEMETRY_PORT="${TELEMETRY_PORT:-19090}"

workdir="$(mktemp -d)"
grex_pid=""

cleanup() {
    [[ -n "$grex_pid" ]] && kill "$grex_pid" 2>/dev/null || true
    [[ -n "$grex_pid" ]] && wait "$grex_pid" 2>/dev/null || true
    rm -rf "$workdir"
}
trap cleanup EXIT

echo "== building grex and grex-synth =="
go build -o "$workdir/grex" ./cmd/grex
go build -o "$workdir/grex-synth" ./cmd/grex-synth

cat > "$workdir/config.yaml" <<EOF
listeners:
  opamp: "127.0.0.1:${OPAMP_PORT}"
  ui: "127.0.0.1:${UI_PORT}"
  telemetry: "127.0.0.1:${TELEMETRY_PORT}"
EOF

echo "== starting grex (plaintext, no database) =="
"$workdir/grex" -config "$workdir/config.yaml" > "$workdir/grex.log" 2>&1 &
grex_pid=$!

echo "== waiting for grex readiness =="
ready=""
for _ in $(seq 1 50); do
    if curl -fsS "http://127.0.0.1:${TELEMETRY_PORT}/readyz" >/dev/null 2>&1; then
        ready=1
        break
    fi
    if ! kill -0 "$grex_pid" 2>/dev/null; then
        echo "grex exited before becoming ready; log:" >&2
        cat "$workdir/grex.log" >&2
        exit 1
    fi
    sleep 0.2
done
if [[ -z "$ready" ]]; then
    echo "grex did not become ready in time; log:" >&2
    cat "$workdir/grex.log" >&2
    exit 1
fi

echo "== running ${AGENTS} synthetic agents for ${DURATION} =="
report="$workdir/report.txt"
"$workdir/grex-synth" \
    -url "ws://127.0.0.1:${OPAMP_PORT}/v1/opamp" \
    -agents "$AGENTS" \
    -duration "$DURATION" \
    | tee "$report"

connected="$(awk '/^connected:/ {print $2}' "$report")"
failed="$(awk '/^failed:/ {print $2}' "$report")"

if [[ "$connected" != "$AGENTS" ]]; then
    echo "FAIL: connected=$connected, want $AGENTS" >&2
    exit 1
fi
if [[ "$failed" != "0" ]]; then
    echo "FAIL: failed=$failed, want 0" >&2
    exit 1
fi

echo "PASS: $connected/$AGENTS agents connected, 0 failures"
