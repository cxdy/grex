# HTTP endpoints

Defaults assume stock listen addresses. Paths are fixed; ports come from
`listeners.*`.

## OpAMP listener (`listeners.opamp`, default `:4320`)

| Path | Description |
|------|-------------|
| `/v1/opamp` | OpAMP WebSocket and plain HTTP transport |
| other | `501 Not Implemented` |

Optional TLS/mTLS per `opamp_tls.*`.

## UI listener (`listeners.ui`, default `:8080`)

### Web UI

| Path | Description |
|------|-------------|
| `GET /` | Fleet overview |
| `GET /partials/agents` | Fleet htmx partial |
| `GET /agents/{id}` | Agent detail |
| `GET /partials/agents/{id}` | Agent htmx partial |
| `GET /status` | Server status |
| `GET /partials/status` | Status htmx partial |
| `GET /static/…` | Embedded static assets |

### JSON API

| Path | Description |
|------|-------------|
| `GET /api/agents` | List agents |
| `GET /api/agents/{id}` | Agent detail |
| `GET /api/status` | Server + fleet summary |
| `GET /api/attributes` | Attribute keys |
| `GET /api/attributes/values` | Attribute values for a key |

Details: [Read API](../developer/read-api.md).

**Auth:** optional mTLS with SPIFFE IDs, per `ui_tls.client_ca_file` and
`auth.role_mapping`. Applies to every UI/API path when configured. See
[Authentication](../admin/authentication.md).

## Telemetry listener (`listeners.telemetry`, default `:9090`)

| Path | Description | Auth |
|------|-------------|------|
| `GET /healthz` | Liveness (`200` + `ok`) | Always open, even with `telemetry_tls.client_ca_file` set |
| `GET /readyz` | Readiness (`200` / `503`) | Always open, even with `telemetry_tls.client_ca_file` set |
| `GET /metrics` | Prometheus server registry | Optional mTLS with SPIFFE IDs, per `telemetry_tls.client_ca_file` |
| `GET /metrics/fleet` | Prometheus fleet registry | Same as `/metrics` |
| `GET /debug/pprof/…` | pprof (only if `debug.pprof_enabled`) | Same as `/metrics` |
