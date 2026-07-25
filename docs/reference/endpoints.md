# HTTP endpoints

Defaults assume stock listen addresses. Paths are fixed; ports come from
`listeners.*`.

## OpAMP listener (`listeners.opamp`, default `:4320`)

| Path | Description |
|------|-------------|
| `/v1/opamp` | OpAMP WebSocket and plain HTTP transport |
| other | `501 Not Implemented` |

Optional TLS/mTLS per `tls.*`.

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

**Auth:** none today ([issue #11](https://github.com/dennisme/grex/issues/11)).

## Telemetry listener (`listeners.telemetry`, default `:9090`)

| Path | Description |
|------|-------------|
| `GET /healthz` | Liveness (`200` + `ok`) |
| `GET /readyz` | Readiness (`200` / `503`) |
| `GET /metrics` | Prometheus server registry |
| `GET /metrics/fleet` | Prometheus fleet registry |
| `GET /debug/pprof/…` | pprof (only if `debug.pprof_enabled`) |
