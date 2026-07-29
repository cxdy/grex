# High availability

!!! warning "Mostly design, not yet shipped"
    This page describes the **target** architecture for running grex with no
    single point of failure. Most of it is not built: today grex runs as a
    single replica (`replicaCount` pinned to 1 in the Helm chart), in-memory
    fleet state, no shared database. The one piece proven to work is the
    Envoy least-connections layer, demonstrated in the Compose dev stack —
    everything else here is tracked as design in
    [SPEC: design.md](../spec/design.md), not implemented.

## Target topology

```text
   otelcol agents
        │
        ▼
   OpAMP gateway(s)                 (opampgateway extension, own
        │                            least-conn across its own N
        │                            upstream connections only)
        ▼
   cloud TCP load balancer          via the cloud controller manager
   (flow-hash / round robin)        (Service type=LoadBalancer) — required
        │                           plumbing to get external traffic into
        │                           the cluster, not a tuning knob
        ▼
   Envoy (N instances)              least-connections across grex
        │                           replicas (TCP passthrough — grex's
        │                           own mTLS termination is unchanged)
        ▼
   grex (N replicas)                stateless app tier; memory is a
        │                           cache over Postgres, never its own
        │                           durable store
        ▼
   Postgres (CloudNativePG)         one logical writable endpoint,
   primary + replica(s),            HA via replica(s); sharding is a
   cross-AZ/rack                    non-goal — never per-agent routing
```

A client-facing LB (global or per-DC) may sit in front of the gateway tier
too — that hop is about routing clients to a nearby gateway and is
independent of everything below it; dropping it for a single-DC deployment
changes nothing about the rest of this topology.

## Layers

| Layer | Decided shape | Status | Reference |
|---|---|---|---|
| Gateway tier | observIQ `opampgateway` extension; its own least-conn only spreads agents across its own upstream pool (`server.connections`), never across grex replicas | **Real today** | [OpAMP and gateway](../developer/opamp-and-gateway.md), [Scaling with gateways](scaling-with-gateways.md) |
| Outer cloud LB | `Service.type=LoadBalancer` via the CCM; flow-hash/round-robin, not least-conn — its job is only getting a connection into the cluster | Required, **not wired into the Helm chart yet** | [design.md: Load balancing](../spec/design.md#load-balancing-an-lb-in-front-of-grex-is-required-not-optional) |
| Inner LB (Envoy) | TCP proxy, `LEAST_REQUEST` (least-connections for a raw TCP cluster) across grex replicas | **Proven in Compose** (`deploy/compose/envoy.yaml`); not yet in the Helm chart | [design.md: Load balancing](../spec/design.md#load-balancing-an-lb-in-front-of-grex-is-required-not-optional), [Scaling with gateways](scaling-with-gateways.md#load-balancing-across-grex-replicas) |
| grex app tier | Always stateless; N replicas share one logical Postgres endpoint, never sharded per-agent | Not built — `replicaCount` pinned to 1 | [design.md: Agent sharding scheme](../spec/design.md#agent-sharding-scheme) |
| Shared Postgres | CloudNativePG shape: one writable endpoint, HA via primary + replica(s), placement (rack/AZ/site) is the operator's choice | Not built — `internal/persistence` is stubs only, no schema | [design.md: Post-1.0 roadmap](../spec/design.md#post-10-roadmap-state-database-and-sharding) |
| Reads self-heal | Any replica answers from local memory merged with a DB query; stale/missing state falls back to `ReportFullState` | Not built (needs shared Postgres above) | [design.md: Agent sharding scheme](../spec/design.md#agent-sharding-scheme) |
| Dispatch routing | `agent_connections` ownership table + Postgres `LISTEN`/`NOTIFY` handoff, so a job created on any replica reaches the replica that actually holds the agent's live socket | Not built. Known gap: not durable (a notification to a replica that isn't listening at that instant is lost); an owning replica dying mid-dispatch is detectable via the 1.1 compliance check, not self-healing | [design.md: Dispatch routing](../spec/design.md#dispatch-routing-agent_connections-and-cross-replica-handoff), [Jobs: schema and execution](../spec/design.md#jobs-schema-and-execution) |
| Config source of truth | `config_sources` table (`helm`/`git`), sync and apply as two separate human-triggered steps, so IaC stays the real source of truth instead of grex becoming a second one | Not built | [design.md: Config source of truth](../spec/design.md#config-source-of-truth-sync-and-apply) |

## Try it today

The Compose dev stack runs two grex instances behind Envoy and asserts
agents land on both — this exercises the load-balancing layer only, not
shared state or dispatch:

```sh
just compose-up
deploy/compose/smoke.sh
```

See [Local development](../developer/local-development.md) and
[Scaling with gateways](scaling-with-gateways.md#load-balancing-across-grex-replicas).

## What's still open

- The outer cloud LB hop isn't wired into the Helm chart (no
  `Service.type=LoadBalancer` template, no Envoy chart dependency).
- No shared-state Postgres schema exists yet, so `replicaCount > 1` doesn't
  actually work in production regardless of the load-balancing layer above.
- Dispatch routing's crash-mid-dispatch case has no automatic retry, only a
  human-run compliance check — see design.md for why an automatic version
  of this is really the parked Declarative/policy layer idea, not something
  jobs do on their own.

## See also

- [Scaling with gateways](scaling-with-gateways.md)
- [SPEC: design.md](../spec/design.md) — full reasoning behind every
  decision above
