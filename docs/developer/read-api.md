# Read API

Base URL: **UI listener** (default `:8080`). Content-Type:
`application/json`. **Read-only** — no write endpoints in 1.0.

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
`auth.role_mapping`); `deploy/compose/certs/user-admin.pem` is one of the
dev certs `gen-certs.sh` mints for exactly this. `-k` skips CA verification
here because the dev CA isn't in your system trust store, not because
grex's TLS is optional.

```sh
CERTS=deploy/compose/certs

curl -k --cert "$CERTS/user-admin.pem" --key "$CERTS/user-admin-key.pem" \
  'https://localhost:8080/api/agents?connected=true&limit=50' | jq

curl -k --cert "$CERTS/user-admin.pem" --key "$CERTS/user-admin-key.pem" \
  'https://localhost:8080/api/agents?match=service.name=~"otel.*"' | jq

curl -k --cert "$CERTS/user-admin.pem" --key "$CERTS/user-admin-key.pem" \
  "https://localhost:8080/api/agents/${INSTANCE_UID}" | jq

curl -k --cert "$CERTS/user-admin.pem" --key "$CERTS/user-admin-key.pem" \
  'https://localhost:8080/api/status' | jq

curl -k --cert "$CERTS/user-admin.pem" --key "$CERTS/user-admin-key.pem" \
  'https://localhost:8080/api/attributes?prefix=service' | jq
```

If `ui_tls.client_ca_file` isn't set (mTLS off), the same requests work
over plain `http://` with no `--cert`/`--key`/`-k`.
