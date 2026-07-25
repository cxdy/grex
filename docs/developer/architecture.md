# Architecture

## Single binary, three listeners

grex intentionally uses **three TCP listeners** so each surface has its own
network exposure and (eventually) auth boundary:

```text
┌────────────────────────────────────────────────────────────┐
│                          grex                              │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ OpAMP :4320 │  │ UI :8080     │  │ Telemetry :9090   │  │
│  │ /v1/opamp   │  │ UI + /api/*  │  │ /healthz /readyz  │  │
│  │ TLS / mTLS  │  │ (open today) │  │ /metrics          │  │
│  └──────┬──────┘  └──────┬───────┘  │ /metrics/fleet    │  │
│         │                │          │ /debug/pprof?     │  │
│         ▼                ▼          └───────────────────┘  │
│         in-memory fleet.Registry                           │
└────────────────────────────────────────────────────────────┘
```

Wiring is centralized in `cmd/grex/main.go`:

1. Load `config`
2. Create **two** Prometheus registries (server vs fleet)
3. Construct `fleet.Registry` with heartbeat settings + metrics events
4. Attach `opamp.Handler` to the registry
5. Mount `api` + `ui` on one HTTP mux
6. `server.New(...).Start()` for all listeners
7. Run registry background loop; wait for signal or fatal listener error

## Data flow

```text
Collector / gateway
    │  OpAMP AgentToServer
    ▼
opamp.Handler  ──writes──►  fleet.Registry
                                │
                ┌───────────────┼────────────────┐
                ▼               ▼                ▼
           api.Handler     ui.Handler    metrics.FleetCollector
           (JSON)          (HTML)        (scrape-time gauges)
```

- **Writes** happen on OpAMP callbacks (and registry eviction ticks)
- **Reads** happen on API/UI handlers and Prometheus collect
- Registry is concurrency-safe; metrics event hooks must not re-enter the
  registry while it holds locks (documented on `fleet.Events`)

## In-memory state only

1.0 does not persist agents. Correctness after restart depends on:

- Agents reconnecting (direct), or
- Gateways continuing to relay check-ins while grex requests full state

There is no multi-instance shared state. Horizontal scale of grex itself is
out of scope until persistence/mutation needs force a redesign.

## Library boundaries

| Concern | Library / approach |
|---------|-------------------|
| OpAMP protocol | [`open-telemetry/opamp-go`](https://github.com/open-telemetry/opamp-go) server packages |
| Metrics | `prometheus/client_golang` |
| Config | `gopkg.in/yaml.v3` |
| UI | stdlib `html/template`, vendored htmx, hand-written CSS, `go:embed` |
| Logging | stdlib `log/slog` to stderr |

grex does not fork OpAMP; it implements callbacks and the gateway custom
capability on top of opamp-go.

## Auth boundaries (intended)

Design assigns:

- Collectors → OpAMP mTLS (partially implemented)
- Humans/API → UI mTLS then OIDC ([issue #11](https://github.com/dennisme/grex/issues/11))
- Scrapers → telemetry network policy (no app-level auth)

Today only the OpAMP TLS path is real. See
[Admin: authentication](../admin/authentication.md).

## Related reading

- [Package map](package-map.md)
- [Fleet state](fleet-state.md)
- [Observability overview](../observability/index.md)
- [SPEC design](../spec/design.md)
