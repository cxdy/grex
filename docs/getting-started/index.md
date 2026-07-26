# Getting started

This tutorial gets you from zero to a running grex process with the web UI
and metrics endpoints available. For a full fleet (collectors, gateway, Dex,
Prometheus), use the [Compose stack](compose-stack.md) next.

## Prerequisites

- **Go 1.26+** (see `go.mod`)
- [just](https://github.com/casey/just) (recipe runner; see `justfile`)
- A terminal and a web browser

Optional later: Docker with Compose v2 for the full stack.

## 1. Clone and build

```sh
git clone https://github.com/dennisme/grex.git
cd grex
just build
```

`just build` compiles all packages with version/commit/date stamped into
`internal/buildinfo` via `-ldflags`.

## 2. Create a config file

```sh
cp config.example.yaml config.yaml
```

The example file documents every field and its default. Leave defaults for
this tutorial. Important listen addresses:

| Listener | Default | Purpose |
|----------|---------|---------|
| OpAMP | `:4320` | Collector connections |
| UI | `:8080` | Web UI + JSON API |
| Telemetry | `:9090` | Health probes + Prometheus metrics |

## 3. Run grex

```sh
go run ./cmd/grex -config config.yaml
```

Or run the binary produced by `just build` (path depends on your module
layout; typically under the package build cache, or use
`go build -o grex ./cmd/grex` then `./grex -config config.yaml`).

You should see log lines indicating the three listeners started. Leave the
process running.

## 4. Check liveness and metrics

In another terminal:

```sh
curl -sS http://127.0.0.1:9090/healthz
# ok

curl -sS http://127.0.0.1:9090/readyz
# ok

curl -sS http://127.0.0.1:9090/metrics | head
curl -sS http://127.0.0.1:9090/metrics/fleet | head
```

`/metrics` is **server** health (Go runtime, OpAMP counters, API latency).
`/metrics/fleet` is **fleet** series. See
[Observability](../observability/index.md).

## 5. Open the UI and API

- Web UI: [http://127.0.0.1:8080/](http://127.0.0.1:8080/)
- Status: [http://127.0.0.1:8080/status](http://127.0.0.1:8080/status)
- API: `curl -sS http://127.0.0.1:8080/api/status | jq`

With no collectors connected, the fleet table is empty and status counts are
zero. That is expected.

!!! note "UI is open"
    Authentication for the UI and API is
    [not implemented yet](https://github.com/dennisme/grex/issues/11). Do not
    expose the UI listener to untrusted networks without a network boundary.

## 6. (Optional) Connect a collector

Point an OpenTelemetry Collector `opamp` extension at grex’s OpAMP endpoint
(`ws://host:4320/v1/opamp` or HTTP polling). For TLS/mTLS and a ready-made
multi-collector topology, use the [Compose stack](compose-stack.md).

When an agent connects, it appears on the fleet page after the next UI poll
(default `ui.poll_interval` is `5s`).

## What you learned

- grex is a single binary with **three listeners**
- Config is YAML + optional `GREX_*` environment overrides
- Health and metrics live on the **telemetry** port
- UI and JSON API live on the **UI** port
- Fleet rows appear only after OpAMP agents check in

## Next steps

- [Compose stack](compose-stack.md) — full local environment
- [Deploy with Helm](../admin/helm.md) — Kubernetes (production shape)
- [User guide](../user/index.md) — using the fleet UI
- [Admin: configuration](../admin/configuration.md) — all settings
- [Developer: local development](../developer/local-development.md) — tests and layout
