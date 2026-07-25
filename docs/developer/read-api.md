# Read API

Base URL: **UI listener** (default `:8080`). Content-Type:
`application/json`. **Read-only** — no write endpoints in 1.0.

!!! note "Auth"
    No authentication yet
    ([issue #11](https://github.com/dennisme/grex/issues/11)).

Handlers are instrumented with `grex_api_requests_total` and
`grex_api_request_duration_seconds` when mounted through
`metrics.HTTPMetrics.Instrument`.

---

## `GET /api/agents`

Paginated, filtered list of agents as `SummaryView` objects.

### Query parameters

| Param | Default | Description |
|-------|---------|-------------|
| `limit` | `100` | Page size; max `1000`; must be positive if set |
| `offset` | `0` | Non-negative |
| `healthy` | — | `true` / `false` |
| `connected` | — | `true` / `false` |
| `via_gateway` | — | `true` / `false` |
| `match` | — | Repeatable Prometheus-style matcher |
| `<attribute key>` | — | Exact match on attribute (legacy bare form) |
| `attr_key` + `attr_value` | — | Legacy freeform exact match |
| `sort`, `order` | — | Reserved for UI; not filters |

Invalid pagination or bool values → **400**.

Filtering applies **before** pagination. `total` is the filtered set size.
Sort for paging stability is by `instance_uid` ascending in the handler
(UI may re-sort for display).

### Matchers

Prometheus-style, full-value match (RE2), operators: `=`, `!=`, `=~`, `!~`.

```http
GET /api/agents?match=service.name=otelcol&match=deployment.environment=~"prod|staging"
```

Spaces around operators are allowed. Regex values may be quoted.

Multiple matchers and bools are **ANDed**.

### Response

```json
{
  "agents": [ /* SummaryView */ ],
  "total": 42,
  "limit": 100,
  "offset": 0
}
```

Summary items omit `effective_config` and `packages`.

---

## `GET /api/agents/{id}`

Full `DetailView` for `instance_uid` `{id}`.

| Status | When |
|--------|------|
| 200 | Found |
| 400 | Missing id |
| 404 | Unknown / evicted |

---

## `GET /api/status`

```json
{
  "version": "…",
  "commit": "…",
  "go_version": "…",
  "started_at": "…",
  "uptime_seconds": 123,
  "fleet": {
    "total": 0,
    "connected": 0,
    "disconnected": 0,
    "healthy": 0,
    "unhealthy": 0,
    "health_unknown": 0,
    "awaiting_full_state": 0
  }
}
```

---

## `GET /api/attributes`

Distinct attribute keys across the fleet.

| Param | Description |
|-------|-------------|
| `prefix` | Optional case-insensitive substring filter for autocomplete |

```json
{ "keys": ["host.name", "service.name", "…"] }
```

---

## `GET /api/attributes/values`

Distinct values for one key.

| Param | Description |
|-------|-------------|
| `key` | **Required** attribute key |
| `prefix` | Optional substring filter |

```json
{ "values": ["…"] }
```

Missing `key` → **400**.

---

## Agent JSON shape (detail)

See `fleet.AgentView` for field names. Notable points:

- `connection.via_gateway`, `connection.transport`, `connection.remote_addr`,
  `connection.tls_subject`
- `capability_flags` object alongside raw `capabilities` uint64
- `health_reported` / `description_reported` distinguish “false” from “unknown”
- `missing_attributes` lists required keys not present

---

## Client examples

```sh
curl -sS 'http://127.0.0.1:8080/api/agents?connected=true&limit=50'
curl -sS 'http://127.0.0.1:8080/api/agents?match=service.name=~"otel.*"'
curl -sS "http://127.0.0.1:8080/api/agents/${INSTANCE_UID}"
curl -sS 'http://127.0.0.1:8080/api/attributes?prefix=service'
```
