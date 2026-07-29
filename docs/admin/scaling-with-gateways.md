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
plus HTTP status). In 1.0, grex **accepts** every agent on an authenticated
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

## Load balancing across grex replicas

A gateway's own least-connections logic only spreads its agents across its
*own* upstream connection pool (`server.connections`) — it has no notion of
multiple grex backends, `server.endpoint` is a single address. Getting a
gateway's upstream connections to actually land on more than one grex
replica needs a least-connections-aware LB in front of grex; without one,
distribution is either manual static assignment or unweighted DNS luck.
See [design.md's Load balancing
section](../spec/design.md#load-balancing-an-lb-in-front-of-grex-is-required-not-optional)
for the full reasoning and the decision to use Envoy (TCP proxy mode) for
this.

The compose stack demonstrates this locally: `deploy/compose/envoy.yaml`
runs a two-endpoint (`grex`, `grex-2`) least-connections cluster in front
of grex, and `opamp-gateway.yaml`'s `server.endpoint` points at Envoy
rather than a single grex instance. `deploy/compose/smoke.sh` asserts
agents actually land on both replicas, not just one. This is a local-dev
reference for the pattern, not a production topology — an outer cloud TCP
load balancer (via the cloud controller manager) is still required to get
external gateway traffic into a cluster in the first place, and this
compose setup does not exercise that hop.

See [High availability](high-availability.md) for how this layer fits into
the full target multi-replica architecture (shared state, dispatch
routing, and what's still unbuilt).

## What is not provided

- Automatic gateway discovery or grex-side load balancing of gateways —
  still true; the LB described above is external infrastructure, not
  something grex itself does
- HA multi-instance grex with shared state (single process, memory
  registry) — the compose stack above runs multiple grex processes for
  load-balancing purposes only; they do not share state (see
  [design.md's Agent sharding scheme](../spec/design.md#agent-sharding-scheme)
  for the still-unbuilt Postgres-backed shared state this needs)
- Runbooks for multi-cluster production topologies (deferred)

Scale horizontally at the **gateway tier** first; treat grex as the central
read-only control plane for one fleet.
