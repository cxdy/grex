# OpAMP and gateway

## Protocol stack

grex uses [`opamp-go`](https://github.com/open-telemetry/opamp-go) as the
OpAMP server implementation. Application code lives in `internal/opamp`:

- Construct `opampserver.New` with a slog adapter
- On attach, return an `http.Handler` for `/v1/opamp` plus `ConnContext` so
  the plain HTTP transport can see the underlying `net.Conn`
- Implement server callbacks to update `fleet.Registry`

### Transports

| Transport | Label in metrics/UI | Notes |
|-----------|---------------------|-------|
| WebSocket | `ws` | Primary; required for gateway-relayed traffic |
| Plain HTTP | `http` | Spec interoperability |

## Capabilities grex uses (1.0)

Agents may report status, health, effective config, package statuses, and
own-telemetry **metadata**. grex **does not** send server offers for remote
config, packages, or connection settings in 1.0.

When an agent entry has no description, grex requests full state
(`ReportFullState`) so post-restart convergence works for gateway-relayed
agents that never reconnect from their point of view.

## Gateway capability

```text
com.bindplane.opamp-gateway
```

### `connect` (gateway → grex)

JSON custom message including roughly:

- `request_uid` (optional correlation)
- `remote_address` of the agent as seen by the gateway
- `headers` from the agent’s HTTP upgrade/request

The connect payload does **not** include `instance_uid`. grex logs the
forwarded metadata; it cannot join those fields onto a specific agent entry
at connect time. Agent rows carry gateway connection metadata +
`via_gateway=true`.

### `connectResult` (grex → gateway)

```json
{
  "request_uid": "...",
  "accept": true,
  "http_status_code": 200
}
```

1.0 policy: **accept all** agents on a gateway connection that has already
been established (and mTLS-authenticated when TLS is configured). Rejects
would be counted on `grex_gateway_connects_total{result="rejected"}`.

### Connection bookkeeping

The handler keeps:

- `connAgents` — instance uids seen on a connection (for disconnect)
- `gatewayConns` — connections that sent a gateway connect message
- `connTransports` — `ws` / `http` per connection

Closing a **direct** connection marks its agents disconnected immediately.
Closing a **gateway** connection marks relayed agents disconnected; further
liveness is still governed by check-in timers for partial failures.

## Identity

- Primary key: OpAMP **`instance_uid`**
- Description attributes: identifying + non-identifying maps
- Role heuristic (`fleet.RoleOf`): `service.component` → name contains
  `gateway` → else `Collector`

## Compose packaging

`deploy/compose/opamp-gateway-build/` builds a collector with the
`opampgateway` extension. A module `replace` points at a fork that wires
upstream TLS into the websocket dialer (extension config existed but was not
applied in the stock build grex targets). Comments in the manifest explain
when to drop the replace.

## Testing hooks

- Unit tests simulate messages and gateway JSON
- `e2e_test.go` exercises more of the server stack
- Compose smoke validates a real multi-container path

When changing gateway behavior, update metrics labels carefully and keep
accept/reject paths explicit for future policy hooks.
