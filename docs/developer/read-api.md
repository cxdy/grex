# Read API

Base URL: **UI listener** (default `:8080`). Content-Type:
`application/json`. Mostly read-only: the one exception is `POST
/api/jobs` below, the first piece of the not-yet-finished jobs/mutation
feature (see [design doc](../spec/design.md#jobs-schema-and-execution)).

!!! note "Auth"
    mTLS with SPIFFE IDs is shipped (see
    [Authentication](../admin/authentication.md)); OIDC is not yet
    ([issue #11](https://github.com/dennisme/grex/issues/11)). When
    `ui_tls.client_ca_file` is set (the compose dev stack sets it), every
    route below requires a client certificate mapped to a role.

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
  "offset": 0,
  "partial": false
}
```

Summary items omit `effective_config` and `packages`.

When `database.host` is set, this list merges the local registry with one
`ListAgents` read from the database, so agents held only by a sibling grex
replica are still included (see
[Persistence](persistence.md#fleet-wide-list)). `partial` is `true`
when that database read failed: the response then reflects **only** this
replica's local registry, and agents live solely on a sibling replica are
missing from `total`/`agents` until the database is reachable again. This
never happens (`partial` is always `false`) when `database.host` is unset.
A failure here does not fail the request — HTTP status stays 200, and
`grex_list_agents_store_fallback_errors_total{surface="api"}` increments
(see [Metrics reference](../observability/metrics.md)).

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

## `POST /api/jobs`

Creates a job in `planned` status. Nothing is dispatched: matching agents
against `filter` and sending anything to them are both separate,
not-yet-built steps (arming, then dispatch — see [design
doc](../spec/design.md#jobs-schema-and-execution)). This is the only
mutating endpoint today.

### Request body

| Field | Required | Description |
|-------|----------|-------------|
| `filter` | Yes | Same filter language as `GET /api/agents`. Stored as-is; not evaluated until the job is armed |
| `action` | Yes | e.g. `restart`. No fixed enum — new action types are additive |
| `submitted_by` | Yes | No identity/auth wiring yet — this is whatever the caller sends, unverified |
| `action_config` | No | Action-specific knobs (`jsonb`), e.g. restart's `reconnect_timeout`/`backoff_cap`. Opaque to this endpoint, stored verbatim |

```json
{
  "filter": "service.name=otelcol-contrib",
  "action": "restart",
  "action_config": { "reconnect_timeout": "5m" },
  "submitted_by": "alice"
}
```

`201` with the created job:

```json
{
  "id": "3fa4c1e2-...",
  "filter": "service.name=otelcol-contrib",
  "action": "restart",
  "action_config": { "reconnect_timeout": "5m" },
  "status": "planned",
  "target_mode": null,
  "submitted_by": "alice",
  "created_at": "2026-08-01T15:33:41Z",
  "armed_at": null,
  "dispatch_at": null,
  "cancelled_at": null
}
```

| Status | When |
|--------|------|
| 201 | Created |
| 400 | Missing `filter`/`action`/`submitted_by`, or malformed JSON body |
| 503 | `database.host` unset — jobs have no in-memory fallback, unlike agent reads |
| 500 | Backend error |

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

Against the compose dev stack, the UI listener requires a client
certificate mapped to a role (`deploy/compose/grex.yaml`'s
`auth.role_mapping`). Use
[`scripts/gxcurl`](testing.md#scriptsgxcurl-mtls-curl-wrapper) for these —
it supplies the `--cert`/`--key`/`-k` boilerplate so you never hand-build
those paths; every example below goes through it except the last, which
has no mTLS involved at all.

```sh
scripts/gxcurl -u admin 'https://localhost:8080/api/agents?connected=true&limit=50' | jq

scripts/gxcurl -u admin 'https://localhost:8080/api/agents?match=service.name=~"otel.*"' | jq

scripts/gxcurl -u admin "https://localhost:8080/api/agents/${INSTANCE_UID}" | jq

scripts/gxcurl -u admin 'https://localhost:8080/api/status' | jq

scripts/gxcurl -u admin 'https://localhost:8080/api/attributes?prefix=service' | jq

scripts/gxcurl -u admin -X POST 'https://localhost:8080/api/jobs' \
  -H 'Content-Type: application/json' \
  -d '{"filter":"service.name=otelcol-contrib","action":"restart","submitted_by":"alice"}' | jq
```

If `ui_tls.client_ca_file` isn't set (mTLS off), the same requests work
over plain `http://` with no cert at all — `gxcurl` doesn't apply here
since there's no cert to supply:

```sh
curl -s 'http://localhost:8080/api/status' | jq
```
