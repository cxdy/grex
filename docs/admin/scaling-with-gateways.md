# Scaling with OpAMP gateways

Direct agent connections to grex work and are supported. For large fleets,
grex is designed to sit behind **OpAMP gateway** collectors that multiplex
many agent sessions over a small number of upstream WebSocket connections.

## Why gateways

- Fewer TLS handshakes and sockets on grex
- Agents talk to a local/regional gateway; only gateways reach the control plane
- grex keys fleet state by **`instance_uid`**, never by TCP connection, so
  multiplexing is first-class

## Capability

Gateways speak the observIQ custom capability:

```text
com.bindplane.opamp-gateway
```

When a gateway admits a new agent, it sends a **`connect`** custom message
(headers + remote address). grex answers **`connectResult`** (accept/reject
+ HTTP status). In 1.0, grex **accepts** every agent on an authenticated
gateway connection.

See [OpAMP and gateway](../developer/opamp-and-gateway.md) for protocol
details and compose packaging notes.

## Metrics that matter

| Metric | Use |
|--------|-----|
| `grex_gateway_connections` | Open connections that identified as gateways |
| `grex_gateway_connects_total{result=…}` | Per-agent connect delegations |
| `grex_agents_connected{via="gateway"\|"direct",transport=…}` | Mix of path types |

Full catalog: [Metrics](../observability/metrics.md).

## Operational notes

- Gateway-relayed disconnect is detected via **missed check-ins**, not TCP
  close of the agent (grex only sees the gateway socket). Tune
  `fleet.heartbeat_interval` and `fleet.stale_missed_heartbeats` accordingly.
- Client certificate identity on grex is the gateway’s when mTLS is enabled.
- The compose stack builds a custom OpAMP gateway image to fix upstream TLS
  wiring; read comments under `deploy/compose/opamp-gateway-build/`.

## What is not provided

- Automatic gateway discovery or grex-side load balancing of gateways
- HA multi-instance grex with shared state (single process, memory registry)
- Runbooks for multi-cluster production topologies (deferred)

Scale horizontally at the **gateway tier** first; treat grex as the central
read-only control plane for one fleet.
