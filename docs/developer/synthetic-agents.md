# Synthetic agents

`cmd/grex-synth` runs short-lived synthetic OpAMP agents against a grex
server or gateway to measure connection scale. Each agent is a real
opamp-go client, not a mock: it connects, heartbeats, optionally simulates a
supervisor restart, then disconnects. The run prints a report of how many
agents connected, connect-latency percentiles, and failures bucketed by
message.

It exists for load work like the 1M-agent scaling test. Helm and the
server-side infrastructure that test needs are out of scope here; this is
just the client-side load generator.

## Quick start

Against a plaintext local grex (OpAMP on `127.0.0.1:4320`):

```sh
go run ./cmd/grex-synth -url ws://127.0.0.1:4320/v1/opamp -agents 100 -duration 30s
```

Against a gateway requiring mTLS:

```sh
go run ./cmd/grex-synth \
  -url wss://gateway:4320/v1/opamp \
  -agents 50000 -ramp 5000 \
  -heartbeat 30s -duration 10m -restart-after 5m \
  -cert client.crt -key client.key -ca ca.crt
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-url` | (required) | OpAMP endpoint. Scheme selects the transport: `ws`/`wss` use a WebSocket client, `http`/`https` an HTTP-polling client |
| `-agents` | `1` | Number of synthetic agents, each its own connection and `instance_uid` |
| `-ramp` | `0` | New agents started per second. `0` starts them all at once (a connection burst) |
| `-heartbeat` | `30s` | OpAMP heartbeat interval |
| `-duration` | `1m` | How long the run lasts before all agents disconnect |
| `-restart-after` | `0` | Simulate a supervisor restart this far into the run. `0` disables it. Must be less than `-duration` |
| `-service-name` | `otelcol-contrib` | Agent `service.name` identifying attribute |
| `-cert` / `-key` | (none) | Client certificate and key for mTLS. Set both or neither |
| `-ca` | (none) | CA certificate for verifying the server |

Each agent also declares `opamp.managed_by=opentelemetry-opampsupervisor`
and the `AcceptsRestartCommand` capability, so grex records it as
supervisor-managed (what job targeting requires — see
[Fleet state](fleet-state.md)).

## What the report shows

```text
── synth report ──
agents:     50000
connected:  49998
failed:     2
restarted:  49998
connect p50/p99/max: 3.1ms / 84ms / 1.2s
errors:
      2  connect timed out before deadline
```

- **connected / failed** — agents that reached a successful connection versus
  those that never did before the run's deadline.
- **restarted** — agents that completed a simulated restart (only when
  `-restart-after` is set).
- **connect p50/p99/max** — connection-establishment latency over connected
  agents. The max is where ephemeral-port and accept-queue pressure shows up.
- **errors** — distinct failure messages with a count, so a mass failure is
  one line, not one line per agent.

There is no heartbeat count: opamp-go sends heartbeats internally with no
per-beat callback, so the tool reports liveness as staying connected through
the run rather than a number it can't measure.

## Scaling: one process is bounded

A single node is limited by ephemeral ports and memory, not the tool. Plan
on roughly **50k agents per node** and shard across nodes for larger fleets.

Outbound TCP is unique per `(srcIP, srcPort, dstIP, dstPort)`. With one
source IP connecting to one gateway endpoint, only the source port varies, so
the ephemeral-port range is the ceiling:

- Linux default `net.ipv4.ip_local_port_range` (32768–60999) ≈ 28k
  connections. Widen to `1024 65535` for ~64k. That is a hard per-tuple limit.
- File descriptors hit first if `ulimit -n` is left at its 1024 default —
  raise it.
- Past ~60k, per-connection memory (~40–60 KB each: goroutines, WebSocket
  buffers, TLS state) becomes the wall.

The tool assumes **one source IP per process** on purpose: it maps cleanly to
one connection burst per pod and keeps the tool simple. To reach 1M agents,
run ~20 pods at 50k each rather than tuple-fanning a single fat node. The
server side has its own, separate ceiling (accept queue, per-connection
memory, TLS handshake CPU).

## Simulated restart, not a delivered command

`-restart-after` makes each agent disconnect and reconnect once, timing the
reconnect. It does **not** wait for grex to send a restart command: job
arm/dispatch over OpAMP is not built yet (`internal/api/jobs.go`). The agent
already declares `AcceptsRestartCommand`, so when server-side dispatch lands,
wiring the opamp-go `OnCommand` callback turns this into a real end-to-end
restart. Until then it exercises the reconnect storm, which is the
scaling-relevant half.

## Tests

`internal/synth` holds the logic; `cmd/grex-synth` is flag parsing. The e2e
test (`internal/synth/run_test.go`) runs the agents against a real opamp-go
server and asserts they connect and reconnect — no mocks. `Config.Validate`
and the report aggregation are unit-tested in `internal/synth/synth_test.go`.

## Smoke test

`scripts/synth-smoke.sh` is the black-box check: it builds `grex` and
`grex-synth`, starts grex with a plaintext OpAMP listener (no TLS, no
database), runs a batch of agents against it, and fails unless every agent
connected with no failures.

```sh
just synth-smoke
# tune the batch: AGENTS=200 DURATION=10s just synth-smoke
```

CI runs the same script via `.github/workflows/synth-smoke.yaml`, which
triggers **only** when the tool, its package, or the script itself changes
(`cmd/grex-synth/**`, `internal/synth/**`, `scripts/synth-smoke.sh`). The
full Go suite (`golang-tests.yaml`) already covers those paths on every PR;
this workflow just adds the built-binary end-to-end check when synth code
moves.
