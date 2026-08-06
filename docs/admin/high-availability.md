# High availability

!!! warning "Mostly design, not yet shipped"
    This page describes the **target** architecture for running grex with no
    single point of failure. Most of it is not built. `replicaCount` stays
    pinned to 1 in the Helm chart, which has no `database.*` values to wire
    up Postgres for a production deploy at all — so nothing here actually
    runs with more than one replica today, regardless of what the binary
    itself can already do. Two pieces are proven to work: the Envoy
    least-connections layer (Compose dev stack), and — further along than
    this page used to say — `internal/persistence`'s agent-state schema,
    write path, and read-side API fallback (`GET /api/agents[/{id}]` merges
    local memory with Postgres) are real and wired into `cmd/grex`, not
    stubs. See the table below for exactly what that covers and what still
    doesn't exist; everything else here is tracked as design in
    [SPEC: design.md](../spec/design.md), not implemented.

## Target topology

```text
   otelcol agent / Supervisor
        │                           opamp-go client tries every address
        │                           in a DNS answer (falls back to plain
        │                           net.Dialer) — connect-time failover
        │                           for free, no LB needed at this hop
        ▼
   multi-answer DNS (A/AAAA)        large agent population self-averages
        │                           across the gateway pool; only a risk
        │                           under mass-reconnect bursts (see
        │                           Scaling with gateways)
        ▼
   OpAMP gateway pool (N)           opampgateway extension: least-conn
        │                          only across its OWN upstream pool
        │                          (server.connections), never across
        │                          grex replicas
        ▼
   cloud TCP load balancer          via the cloud controller manager
   (flow-hash / round robin)        (Service type=LoadBalancer) — required
        │                           plumbing to get external traffic into
        │                           the cluster, not a tuning knob
        ▼
   Envoy (N instances)              THE load-bearing LB in this whole
        │                           chain: least-connections across grex
        │                           replicas (TCP passthrough — grex's
        │                           own mTLS termination is unchanged).
        │                           Fixes the small-N problem DNS/round
        │                           robin can't — a gateway's upstream
        │                           pool is few connections, one bad pick
        │                           is 100% skew, unlike the large agent
        │                           population above.
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

Only one Envoy tier is load-bearing here: gateway-to-grex. Agent-to-gateway
does not need one by default — DNS plus the client's own connect-time
fallback across the RRset is enough, because the agent population is large
enough to self-average the way a gateway's small upstream-connection count
cannot. A second Envoy tier in front of the gateway pool is only worth
adding if mass-reconnect skew is an observed problem, not a default part of
this topology.

## Layers

| Layer | Decided shape | Status | Reference |
|---|---|---|---|
| Gateway tier | observIQ `opampgateway` extension; its own least-conn only spreads agents across its own upstream pool (`server.connections`), never across grex replicas | **Real today** | [OpAMP and gateway](../developer/opamp-and-gateway.md), [Scaling with gateways](scaling-with-gateways.md) |
| Outer cloud LB | `Service.type=LoadBalancer` via the CCM; flow-hash/round-robin, not least-conn — its job is only getting a connection into the cluster | Required, **not wired into the Helm chart yet** | [design.md: Load balancing](../spec/design.md#load-balancing-an-lb-in-front-of-grex-is-required-not-optional) |
| Inner LB (Envoy) | TCP proxy, `LEAST_REQUEST` (least-connections for a raw TCP cluster) across grex replicas | **Proven in Compose** (`deploy/compose/envoy.yaml`); not yet in the Helm chart | [design.md: Load balancing](../spec/design.md#load-balancing-an-lb-in-front-of-grex-is-required-not-optional), [Scaling with gateways](scaling-with-gateways.md#load-balancing-across-grex-replicas) |
| grex app tier | Always stateless; N replicas share one logical Postgres endpoint, never sharded per-agent | Not built — `replicaCount` pinned to 1 | [design.md: Agent sharding scheme](../spec/design.md#agent-sharding-scheme) |
| Shared Postgres | CloudNativePG shape: one writable endpoint, HA via primary + replica(s), placement (rack/AZ/site) is the operator's choice | **Schema and write path built** (`agents`/`agent_session`/`agent_effective_config`, dirty-tracked batched flush plus an independent keepalive-cadence `agent_session` refresh on its own ticker, multi-writer-safe `UPSERT`, River-based soft-delete purge — `internal/persistence`, wired in `cmd/grex` behind `database.host`; verified against Compose's two-replica stack with `database.host` set on both). Writes are also timeout-bounded and run through a semaphore-bounded worker pool (sized from `pool.Config().MaxConns`) rather than one at a time, so a single stuck write can't block the rest of a tick — see [Observability: Metrics](../observability/metrics.md#persistence) for the `grex_persistence_*` metrics that surface it if the DB/pool becomes the bottleneck. Not built: CloudNativePG HA/placement itself is an operator concern outside grex, the Helm chart has no `database.*` values to configure this for a real deployment yet, and SQL-level write batching (deferred, see design.md) | [design.md: Post-1.0 roadmap](../spec/design.md#post-10-roadmap-state-database-and-sharding) |
| Reads self-heal | Any replica answers from local memory merged with a DB query; stale/missing state falls back to `ReportFullState` | **Partially built.** The API-serving-time merge is real (`GET /api/agents[/{id}]` already merge local registry with a `StateStore` query, soft-deleted rows excluded). A DB-only agent's `connected` flag is staleness-checked at read time (`api.StaleConnected`, same threshold `Registry.Sweep` uses locally) against `SessionUpdatedAt`, not `LastSeen` — `persistence.SessionSnapshotter` keeps `agent_session` fresh on its own ticker, independent of dirty-tracking, rewriting each agent's row before its stored timestamp can age past that same threshold, since a quiet healthy agent's `LastSeen` alone would go stale in the database well before any real problem exists. A crashed owning replica's stale `connected: true` no longer persists forever via sibling replicas' API responses. Not built: the OpAMP connect-time half — checking DB freshness for an agent connecting to a *different* replica than last time and requesting `ReportFullState` if stale; today `needsFullState` only checks this replica's own local registry | [design.md: Agent sharding scheme](../spec/design.md#agent-sharding-scheme) |
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
