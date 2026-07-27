# Package map

All application code lives under `cmd/grex` and `internal/*`. Packages are
small and dependency direction is roughly:

```text
cmd/grex
  → server, opamp, api, ui, fleet, metrics, config, buildinfo, persistence
api → fleet, buildinfo, persistence (StateStore fallback only, optional)
ui  → api (filters), fleet, persistence (StateStore fallback only, optional)
opamp → fleet
metrics → fleet (collector only)
server → config, prometheus
fleet → (stdlib + uuid + opamp protobufs types)
persistence → fleet (Agent, Events), pgx, river
```

---

## `cmd/grex`

**Role:** process entrypoint and dependency wiring.

- Parses `-config`
- Builds dual Prometheus registries and info gauges
- Constructs registry, OpAMP handler, API, UI, server
- Signal handling, drain delay, graceful shutdown
- `slog` setup from config

No business logic beyond orchestration.

---

## `internal/config`

**Role:** load, override, validate configuration.

- `Load(path)` → defaults + YAML + `GREX_*` env + `validate()`
- Rejects unknown YAML fields
- Validates listener addresses, TLS path pairing, log enums, positive
  durations/limits

Tests: `config_test.go` (env overrides, validation errors).

---

## `internal/fleet`

**Role:** source of truth for agent snapshots.

| Type | Purpose |
|------|---------|
| `Registry` | Concurrent map by `instance_uid`; apply OpAMP updates; eviction loop |
| `Agent` | Full in-memory snapshot |
| `AgentView` / `SummaryView` / `DetailView` | API/UI projection |
| `ConnMeta` | Remote addr, TLS subject, via-gateway, transport |
| `Events` | Metrics hooks (connect/disconnect/evict/reports/attributes) |
| `ReservedAttributeKeys` | `healthy`, `connected`, `via_gateway` collision set |

Key behaviors: check-in tracking, disconnect vs stale eviction,
`ReportFullState` when description missing, required attribute compliance,
reserved attribute conflict counting.

Tests: extensive registry lifecycle coverage in `registry_test.go`.

---

## `internal/persistence`

**Role:** durability layer under `fleet.Registry`, opt-in.

| Type | Purpose |
|------|---------|
| `StateStore` | interface `PostgresStore` implements: `SaveAgent`, `GetAgent`, `ListAgents`, `DeleteAgent`, `SoftDeleteAgent` |
| `DirtyTracker` | implements `fleet.Events`, marks changed `instance_uid`s |
| `Flusher` | own ticker, drains the dirty set into `StateStore` |
| `PurgeWorker` / `NewPurgeClient` | River periodic job, purges soft-deleted rows past `fleet.soft_delete_duration` |

`internal/api`'s `GET /api/agents/{id}` and `internal/ui`'s agent-detail
page (`GET /agents/{id}`, `GET /partials/agents/{id}`) are wired to
`StateStore.GetAgent` so far — a registry miss falls back to it instead of
an automatic 404. `GET /api/agents` (the list endpoint) still doesn't read
from it. See [Persistence](persistence.md) for the schema and the
multi-instance write safeguards.

Tests: real-Postgres integration coverage (`postgres_test.go`,
`purge_test.go`), throwaway container per test, skipped if docker isn't
available.

---

## `internal/opamp`

**Role:** bridge `opamp-go` server ↔ fleet registry.

- Mounts protocol at `/v1/opamp` (WebSocket + HTTP)
- Tracks per-connection agent sets and gateway connections
- Handles `com.bindplane.opamp-gateway` `connect` / `connectResult`
- Emits message and gateway metrics via a small `Metrics` interface

Constants: `GatewayCapability = "com.bindplane.opamp-gateway"`.

Tests: unit `handler_test.go`, end-to-end `e2e_test.go`.

---

## `internal/api`

**Role:** JSON read API over the registry.

| Route | Handler |
|-------|---------|
| `GET /api/agents` | Paginated, filtered list (`SummaryView`) |
| `GET /api/agents/{id}` | Full `DetailView`; falls back to `persistence.StateStore` (if configured) on a registry miss |
| `GET /api/status` | Build info + fleet counts |
| `GET /api/attributes` | Distinct attribute keys (optional `prefix`) |
| `GET /api/attributes/values` | Distinct values for `key` (optional `prefix`) |

Filtering: bool fields, bare attribute params, Prometheus-style `match=`,
legacy `attr_key`/`attr_value`. See [Read API](read-api.md).

Mount accepts an optional `wrap` for HTTP metrics instrumentation.

---

## `internal/ui`

**Role:** server-rendered fleet UI.

- `go:embed` of `templates/*.html` and `static/*`
- Routes: `/`, `/agents/{id}`, `/status`, htmx partials, `/static/`
- Reuses `api.ParseFilters` so UI and API filter semantics match
- Presentation helpers: relative time, status labels, YAML display, sort links
- `/agents/{id}` and its htmx partial fall back to `persistence.StateStore`
  (if configured) on a registry miss, same pattern as `internal/api`

No fleet mutation; no separate SPA build.

---

## `internal/metrics`

**Role:** Prometheus registration and collection.

| Piece | Registry | Notes |
|-------|----------|-------|
| `NewRegistry` | server | Go + process collectors |
| `NewEvents` | splits counters across server/fleet | Event-driven |
| `FleetCollector` | fleet | Scrape-time gauges from registry |
| `HTTPMetrics` | server | `grex_api_requests_*` |
| `NewInfoGauge` | either | `grex_build_info`, `grex_config_info` |

See [Metrics](../observability/metrics.md).

---

## `internal/server`

**Role:** HTTP servers for the three listeners.

- Telemetry mux: healthz, readyz, metrics, metrics/fleet, optional pprof
- OpAMP mux: `/v1/opamp` + 501 elsewhere
- UI: caller-supplied root handler
- TLS config for OpAMP when certs configured
- `BeginDraining` flips readiness; `Shutdown` closes listeners
- `Fatal()` channel for listener failures

---

## `internal/buildinfo`

**Role:** package-level `Version`, `Commit`, `Date` strings set by ldflags.

Used by API status, UI, and `grex_build_info`.

---

## `internal/testcert`

**Role:** generate ephemeral cert material for TLS unit tests.

Not used in production paths.

---

## `deploy/compose` (not a Go package)

Runtime configs, cert generation, custom OpAMP gateway OCB build, Prometheus
scrape config, `smoke.sh`. Treat as the integration fixture for developers.

## `deploy/charts/grex` (not a Go package)

Helm chart for Kubernetes: Deployment, multi-port Service, ConfigMap, optional
Ingress / ServiceMonitors / OpAMP gateway. Packaged into GitHub Pages at
`/charts/` by the docs workflow. Operator docs:
[Deploy with Helm](../admin/helm.md),
[Helm chart reference](../reference/helm-chart.md).
