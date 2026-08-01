# Metrics reference

All metrics are Prometheus exposition format. Prefix: `grex_`.

## Endpoints

| Path | Registry contents |
|------|-------------------|
| `GET /metrics` | Server health: Go/process collectors, OpAMP message counters, API HTTP metrics, build/config info gauges |
| `GET /metrics/fleet` | Fleet health: agent/gateway gauges and counters, per-agent series (when under cap) |

Listener: **telemetry** (default `:9090`).

---

## Server metrics (`/metrics`)

### `grex_build_info`

| | |
|--|--|
| Type | Gauge (info pattern, always `1`) |
| Labels | `version`, `commit`, `go_version` |

Build identity from ldflags / runtime.

### `grex_config_info`

| | |
|--|--|
| Type | Gauge (always `1`) |
| Labels | `log_level`, `log_format`, `tls_enabled`, `mtls_enabled`, `heartbeat_interval`, `stale_missed_heartbeats`, `per_agent_series_limit` |

Non-secret settings in effect—useful to confirm rollouts without grepping logs.

### `grex_opamp_messages_total`

| | |
|--|--|
| Type | Counter |
| Help | OpAMP messages processed |

### `grex_opamp_message_errors_total`

| | |
|--|--|
| Type | Counter |
| Help | OpAMP messages that failed to read or decode |

### `grex_api_requests_total`

| | |
|--|--|
| Type | Counter |
| Labels | `route`, `method`, `code` |

### `grex_api_request_duration_seconds`

| | |
|--|--|
| Type | Histogram |
| Labels | `route`, `method`, `code` |
| Buckets | Prometheus default buckets |

Routes currently instrumented match mounted API paths (`/api/agents`,
`/api/agents/{id}`, `/api/status`, `/api/attributes`,
`/api/attributes/values`).

### Go / process collectors

Standard `prometheus` client Go and process collectors are registered on the
server registry (memory, GC, CPU, FDs, etc.).

### `grex_list_agents_store_fallback_errors_total`

| | |
|--|--|
| Type | Counter |
| Labels | `surface` (`api`/`ui`) |

One fleet-wide list request (`GET /api/agents` or the UI fleet page) whose
database merge failed — the response degraded to local registry data only
rather than failing the request. See
[Persistence: Fleet-wide list](../developer/persistence.md#fleet-wide-list)
and [Read API](../developer/read-api.md)'s `partial` response field. Only
increments when `database.host` is set.

### Auth metrics

**Not present yet.** Auth outcome series arrive with the auth milestone
([issue #11](https://github.com/dennisme/grex/issues/11)).

---

## Fleet metrics (`/metrics/fleet`)

### Connection and lifecycle

| Name | Type | Labels | Description |
|------|------|--------|-------------|
| `grex_agents_connected` | Gauge | `transport` (`ws`/`http`), `via` (`direct`/`gateway`) | Agents currently connected |
| `grex_agents_disconnected` | Gauge | — | Retained, not connected, not yet evicted |
| `grex_agents_evicted_total` | Counter | — | Evictions after missed check-in threshold (fires at soft-delete time when persistence is enabled — see [Persistence](../developer/persistence.md)) |
| `grex_agents_purged_total` | Counter | — | Soft-deleted rows permanently removed by the retention purge job. Only increments when `database.host` is set |
| `grex_agent_connects_total` | Counter | — | Connect transitions |
| `grex_agent_disconnects_total` | Counter | — | Disconnect transitions |
| `grex_gateway_connections` | Gauge | — | Open connections that sent a gateway connect message |
| `grex_gateway_connects_total` | Counter | `result` (`accepted`/`rejected`) | Per-agent gateway connect answers |

### Persistence

Only emitted when `database.host` is set. `grex_persistence_write_duration_seconds`
and `grex_persistence_write_timeout_seconds` share the `op` label
(`save_agent`, `soft_delete_agent`, `save_session`) so they're directly
comparable per operation — average/percentile duration approaching or
crossing the timeout line signals DB, network, or load trouble before
writes actually start failing. `grex_persistence_pool_*` show whether the
connection pool itself is the bottleneck (e.g. grex sized with far more
CPUs, hence a far larger default pgx pool, than Postgres can actually
sustain concurrent writes for).

| Name | Type | Labels | Description |
|------|------|--------|-------------|
| `grex_persistence_write_duration_seconds` | Histogram | `op` | Write duration, recorded on every attempt, success or timeout |
| `grex_persistence_write_timeout_seconds` | Gauge | `op` | Configured timeout for that operation, set once at startup |
| `grex_persistence_pool_acquired_conns` | Gauge | — | Connections currently acquired (in use) from the pool |
| `grex_persistence_pool_max_conns` | Gauge | — | Configured maximum pool size |

### Health and compliance

| Name | Type | Labels | Description |
|------|------|--------|-------------|
| `grex_agent_health` | Gauge | `instance_uid` | `1` healthy / `0` unhealthy; **omitted** until health reported |
| `grex_agent_last_seen_timestamp_seconds` | Gauge | `instance_uid` | Unix time of last check-in |
| `grex_agents_awaiting_full_state` | Gauge | — | Registered without description yet |
| `grex_agents_noncompliant` | Gauge | — | Missing ≥1 required attribute |
| `grex_agents_supervisor_managed` | Gauge | — | Declared the OpAMP Supervisor's `opamp.managed_by` attribute — reliable when true, not when false (see `fleet.SupervisorManaged`) |
| `grex_agent_missing_attributes_total` | Counter | `attribute` | Required attribute missing (on compliance change) |
| `grex_agent_reserved_attribute_conflicts_total` | Counter | `attribute` | Collision with reserved filter keys |

### Reports

| Name | Type | Labels | Description |
|------|------|--------|-------------|
| `grex_agent_reports_total` | Counter | `type` | Report kinds: `status`, `health`, `effective_config`, `package_statuses` |

### Cardinality controls

| Name | Type | Labels | Description |
|------|------|--------|-------------|
| `grex_fleet_size` | Gauge | — | Total registered agents (always emitted) |
| `grex_agent_series_capped` | Gauge | — | `1` if per-agent series omitted due to limit, else `0` |

When `grex_fleet_size` &gt; `metrics.per_agent_series_limit`, **all**
`instance_uid` series are omitted (not partially sampled). Aggregates remain.
Details: [Cardinality](cardinality.md).

---

## Why two endpoints

1. **Scrape economics** — server internals cheap/fast; fleet snapshot costlier
2. **Blast radius** — fleet cardinality blowout trips only the fleet job
3. **Limit scoping** — `per_agent_series_limit` is fleet-only and visible
4. **Routing** — different tenants/retention for SLO vs fleet detail

Compose Prometheus config implements this split; see [Scraping](scraping.md).
