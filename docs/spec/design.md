# grex 1.0 Design Spec

!!! warning "Living document"
    This design spec **changes frequently** as grex evolves. Treat it as the
    current plan of record, not a frozen contract. Implementation may lag or
    diverge briefly; prefer the rest of this documentation site and the source
    for what ships today. Any change that affects behavior, config, or the
    architecture described here, including this file, **must** update the
    relevant pages under `docs/` in the same PR; docs are not a follow-up
    task.

grex is an OpAMP control plane for OpenTelemetry Collector fleets, written in Go and
licensed under Apache 2.0. It implements the server side of the
[OpAMP specification](https://opentelemetry.io/docs/specs/opamp/) and gives operators
a read-only view of fleet health in 1.0.

## Goals for 1.0

- Run as the OpAMP server that collectors (agents and gateways) connect to.
- Scale agent connections through OpAMP gateway collectors running the
  observIQ [`opampgateway` extension](https://github.com/observIQ/bindplane-otel-contrib/tree/main/extension/opampgateway),
  which multiplexes many agent sessions over a few upstream connections.
  Direct agent connections remain supported.
- Show the fleet in a web UI: which collectors are connected, their identity,
  health, effective configuration, and capabilities.
- Expose extensive metrics about the server and the fleet so operators can
  assess fleet health from their existing monitoring stack.
- Secure the front door: TLS termination, mTLS for collector clients, and
  OIDC/OAuth login for UI users (GitHub App OAuth is the first provider).
- Ship as both binaries and Docker images with automated, semver releases.
- Ship a Helm chart for Kubernetes production deployments (Compose remains the
  local multi-collector lab).
- Provide a one-command local dev environment with Docker Compose that includes
  multiple collectors connected to grex.

## Non-goals for 1.0 (deferred)

- **Mutation of any kind from the UI.** No remote config editing, no restarting
  agents, no package/agent upgrades. The UI presents data only.
- **Remote configuration push** (`AcceptsRemoteConfig` flows). The server
  records what agents report; it does not offer configs.
- **Package management** (agent binary/plugin distribution).
- **Connection settings offers** (certificate rotation for agents).
- **Persistent storage.** Fleet state is held in memory and rebuilt from agent
  reconnects. A database can be added when mutation features need durable state.
- **Multi-tenancy.** One fleet per grex instance.
- **Custom OIDC role mapping UIs.** Role assignment is static configuration.

### Post-1.0 roadmap: why the Supervisor matters

Every mutation feature above is deferred because grex's 1.0 client-side
target is the bare `opamp` extension, and the bare extension has a hard
ceiling: it only implements `ReportsStatus`/`ReportsEffectiveConfig`/
`ReportsHealth`. It cannot receive or act on anything the server offers,
because it has no process-management capability of its own; it *is* the
collector process, it can't restart itself, rewrite its own config file and
reload, or replace its own binary. Building any of the deferred features
against the bare extension would mean grex inventing bespoke client-side
tooling from scratch.

The [OpAMP Supervisor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/specification/README.md)
(adopted for one dev agent in milestone 9, see Local development) already
solves the client side of every one of these, as a spec-compliant, actively
maintained upstream component:

| 1.1+ feature (today's non-goal) | OpAMP capability | What the Supervisor already does |
|---|---|---|
| Remote config push | `AcceptsRemoteConfig` | Merges Server-offered config into the collector's config file, restarts the collector, reports `RemoteConfigStatus` back. Reverts to last-known-good if the new config doesn't come up healthy (configurable). |
| Restart agents from the UI | `AcceptsRestartCommand` | Executes a restart command against the process it supervises. |
| Package/agent upgrades | `AcceptsPackages` | Downloads the offered executable, verifies its Cosign signature against Sigstore/Rekor before installing, reverts on a failed post-upgrade health check. |
| Connection settings offers (cert rotation, endpoint migration) | `AcceptsOpAMPConnectionSettings` / `AcceptsOtherConnectionSettings` | Applies Server-offered TLS certs and OTLP exporter connection settings to the managed collector's config. |

Adopting the Supervisor now does **not** mean these features are free or
already done. It removes the client-side blocker; grex still needs, for
each one, the server-side work: constructing and versioning the offers,
deciding what "safe to send" means (e.g. staged rollout, canary a subset of
the fleet before fleet-wide), the API/UI surface to trigger a mutation, and
extending the `viewer`/`admin` role split (currently both read-only, see
AuthN/AuthZ) to actually gate write access once there is something to
write. The point of adopting it early is that when that server-side work
starts, the hard, easy-to-get-wrong parts (safe config merge/revert, signed
package verification, process supervision) are already solved upstream
rather than something grex has to design and harden itself.

One benefit isn't gated on any of that server-side work and is arguably
worth having sooner: **crash-survivable health reporting.** A bare-extension
collector's OpAMP reporting dies the instant its process does, so today grex
sees only a stale heartbeat followed by eviction, no way to tell "crashed,"
"network dropped," and "stopped on purpose" apart. The Supervisor's watchdog
survives the collector crashing, can report the crash itself, and can
capture the collector's last stdout/stderr for diagnosis. That's a fleet
health-visibility improvement independent of any mutation feature — see the
Metrics section for how `grex_agent_health`/`grex_agents_awaiting_full_state`
already surface state transitions; a Supervisor-reported crash reason is a
natural future addition there.

**Metrics gap, verified against upstream source, not just the spec doc.**
Neither side of a remote-config push gives adequate observability today:

- The Supervisor's entire self-instrumentation is two metrics
  (`supervisor.agent.health_status`, `supervisor.agent.fallback_status`,
  both 1/0), alpha stability. Nothing about config-apply outcomes, restart
  counts, OpAMP connection status, or package-update outcomes.
- Config-push lifecycle exists only as protocol data
  (`RemoteConfigStatus`: `UNSET`/`APPLYING`/`APPLIED`/`FAILED` plus a config
  hash and error string), not a metric on either end. No `REVERTED` status
  exists in the protocol; a revert looks like a fresh `APPLIED` unless the
  receiving server tracks hash history itself.
- `opamp-go` has no built-in metrics at all, it's a bare protocol library.
  Server-side visibility is entirely the server implementation's
  responsibility (grex's, when this is built) — same conclusion as the
  Supervisor side, one layer up.
- Package-update status has no real insertion point yet upstream: the
  Supervisor's `packageManager` is currently a stub (every method ignores
  its real parameters), matching the spec README's own note that
  `accepts_packages`/`reports_package_statuses` are accepted in config but
  disabled at runtime.

Precise upstream insertion points for closing the Supervisor-side gap
(single choke-point function for config status, two call sites for
restarts, one pair of callbacks for connection status) are written up in
`supervisor-metrics.md`, ready to become an upstream PR using the same
playbook as the earlier OpAMP gateway TLS fix. Even with that PR, grex still
needs its own server-side metrics built from the `RemoteConfigStatus`
messages it receives (e.g. `grex_agent_remote_config_status_total{status}`)
— the Supervisor-side counters only cover what the Supervisor can see
locally, they don't give grex anything for free.

### Post-1.0 roadmap: state database and sharding

Both **persistent storage** and **multi-tenancy** are 1.0 non-goals (see
above): fleet state lives in memory, keyed by `instance_uid`, one fleet per
instance. Once a mutation feature needs durable state, or a deployment needs
more than one fleet per instance, grex needs a real database behind the
registry. Two schema questions come up immediately and are worth deciding
the shape of early, even though neither is built yet: how permissions are
stored, and how agent state is partitioned once it outgrows one process's
memory. Both are options here, not decisions — see Open questions.

#### Permission table schema

Today's role model (see AuthN/AuthZ) is entirely static: `auth.role_mapping`
maps a SPIFFE ID (or prefix) or OIDC `groups` value to one of two roles,
`viewer`/`admin`, both currently read-only. A database-backed permission
table is what lets that move from "edit YAML, redeploy" to "edit a row,"
and what multi-tenancy needs (a role scoped to one tenant's fleet, not
every fleet on the instance).

- **Flat identity-to-role table.** Nearly the same shape as the config file
  today. Simplest possible migration off the static config, cheapest to
  query (`what role does this caller have` is one indexed lookup). Doesn't
  express "admin on fleet A, viewer on fleet B" without bolting on a scope
  column later.
- **Scoped RBAC.** Roles carry a set of permissions (`agents:read`,
  `agents:write`, ...), and bindings attach an identity to a role *within a
  scope* (tenant, or a fleet/agent-group once that concept exists) — the
  shape Kubernetes RBAC uses. Matches where multi-tenancy is headed, but
  more tables and a permission-check function, for capability grex doesn't
  use until there's more than one role that actually differs in behavior.
- **ReBAC / relationship graph** (Zanzibar/OpenFGA-style). Most flexible,
  handles delegation and hierarchy well, but a new infrastructure
  dependency and query model for a product that ships two roles, one of
  which is a no-op. Not worth it at this stage.

**Decided: flat, one table, not yet built.** Role set stays exactly
`viewer`/`admin` — no new roles, both still read-only. A single
`role_mapping` table, one row per config entry as it exists today:

- `id` — PK.
- `identity_kind` (`spiffe` | `oidc_group`) — generalized name rather than
  `spiffe_id`, so the OIDC identity source (milestone 7) slots in later
  with no rename, same reasoning as `tenant_id` below.
- `identity_value` — the SPIFFE ID string or OIDC `groups` claim value.
- `match` (`exact` | `prefix`) — mirrors today's `spiffe.RoleRule.Match`.
- `role` (`viewer` | `admin`).
- `tenant_id` — nullable, unused until multi-tenancy exists.
- `created_at`, `updated_at`.

An earlier two-table split (`identities` + `role_bindings`) was considered
and dropped: it only earns its keep once an identity needs metadata
independent of its role (audit info, last-authenticated, etc.), and
nothing needs that yet. One row per (identity, tenant, role) still lets an
identity appear in multiple rows once tenants exist, without a separate
identities table to get there.

`auth.default_role` stays in `config.yaml`, not a DB row — it's a single
global scalar; moving it to the database would mean a settings table for
one field, not worth it.

Surfacing the caller's own role in the UI (a new requirement, not just an
admin-facing config concern) needs no schema at all: whatever resolves a
caller's role per request today against static config does the identical
lookup against `role_mapping` once it's built, and the UI only needs that
already-resolved value rendered somewhere (header/badge) — pure wiring.

#### Agent sharding scheme

`replicaCount` is pinned to 1 in the Helm chart today because fleet state is
one process's memory. Once state lives in a database, the next question is
how a multi-replica grex app tier relates to its database.

**Decided: sharding Postgres is a non-goal for grex.** One logical
Postgres endpoint per fleet, HA via a primary plus replica(s) — the
CloudNativePG shape on Kubernetes fits well, since it's what grex already
targets for deployment (Helm chart, k8s-first). A DNS-controlled endpoint
always points at whichever node is currently primary; replicas exist for
failover, not partition ownership. Grex's app code never routes a
per-agent write or read to a specific shard, and never will — this rules
out the per-agent hash-partitioning-across-shards idea considered
earlier (which also had a real blocker: the OpAMP gateway's `connect`
message carries no `instance_uid`, so a gateway-relayed agent couldn't be
routed to the correct shard at connect time without an upstream protocol
change). A consumer standing up their own Postgres deployment is free to
shard it however they want as their own infrastructure choice; grex
itself is built assuming one writable logical endpoint, optionally with
a separate read-only one (below), full stop.

This also means the fleet-wide list read below is one query against one
endpoint, not a scatter-gather across shards — the complexity that
sharding would have added there doesn't exist in this shape.

**Read replica endpoint, not yet supported, worth documenting as future
work once it is.** CloudNativePG-style clusters typically expose a
separate read-only endpoint load-balanced across replicas, distinct from
the primary. That's a natural place to route DB-read traffic (see
`ListAgents` below) — letting UI/API-serving grex replicas scale
independently of write load, without adding any complexity to the write
path. Not built: `internal/config.Database` has exactly one
host/port today, and the Helm chart doesn't reference `database` config
at all yet. A future `database.read_host`-shaped addition (or similar)
is the natural next step, not urgent.

**Reads: a single grex replica's `ListAgents` merges local memory with
one database query.** Local memory only covers agents this process holds
a live connection to; the database covers what every other replica has
flushed. Local memory wins on overlap (it's fresher — the database write
path is intentionally batched, see Agent state schema below). No fan-out
or shard merge needed, per the non-goal above — one query, one merge.

**Reconnect-elsewhere: reads self-heal, dispatch-routing doesn't.** A
multi-replica app tier (a stateless pool sharing the one Postgres endpoint
above) means an agent's WS connection — whether
direct or through a gateway — can drop and reconnect to a *different*
grex process at any time; nothing guarantees connection reuse or
affinity to the replica that last held it. Whichever replica held the
agent's state must get it into the shared database before another
replica needs it, and that can't be guaranteed by flushing faster or even
synchronously on disconnect: a hard crash or network partition gives no
disconnect event at all, so there's nothing to race against. The fix
isn't closing that window, it's not needing to: this is the same
situation `ReportFullState` already handles today (a process with no —
or, here, insufficiently fresh — description for an `instance_uid` asks
for one and the agent resends everything), just triggered by "connected
to a different replica" instead of "grex restarted." On connect, a
replica checks the shared database (not only local memory) for that
`instance_uid`: fresh enough → fast path, no penalty; stale or missing
(the previous replica hadn't flushed yet, or the agent is genuinely new)
→ `ReportFullState`, self-heals. Correctness stops depending on flush
timing at all; flush-on-disconnect is still worth doing as a best-effort
optimization (fewer graceful reconnects need the fallback), just not
load-bearing.

**A related, narrower gap, found and closed separately:** the
connect-time self-heal above is about the *agent's own* reconnect. It says
nothing about a *third party reading the API* for an agent that hasn't
reconnected at all — e.g. its owning replica crashed hard and it simply
never comes back. `Registry.Sweep` only walks the local in-memory map, it
never evaluates a DB-only row, so nothing was correcting a stale
`connected: true` a crashed replica's last flush left behind; it would
have been visible via every sibling replica's API indefinitely. Fixed:
`api.StaleConnected` applies the identical threshold `Sweep` uses locally
(`Registry.HeartbeatInterval`) to DB-only agents at merge/read time
(`internal/api`'s `MergeAgents` and `getAgent`, `internal/ui`'s equivalent
lookup), one shared definition of "stale" for both local and DB-only
agents rather than two.

Fixing that surfaced a second, deeper gap in what data the check had to work
with: it started out checking `agents.last_seen`, which only advances on a
dirty-tracked flush (identity/health field changes) — a perfectly healthy,
quiet agent sending lightweight heartbeats with no reportable field change
would never re-flush `last_seen` at all after its first report, making
every connected agent look stale within one `HeartbeatInterval` of going
quiet, not just a genuinely crashed one. This is exactly the case Agent
state schema below anticipated ("agent_session ... can be snapshotted
wholesale each tick rather than tracked in the dirty set") but hadn't been
built. Fixed by building it: `persistence.SessionSnapshotter` writes
`agent_session` for every registered agent on its own ticker, independent
of dirty tracking, and `fleet.Agent.SessionUpdatedAt` (sourced from
`agent_session.updated_at`, not `agents.last_seen`) is what
`StaleConnected` actually checks now. Also required loosening
`agent_session`'s own write guard from a strict advance to allowing ties
(`<=` not `<`): `Registry.Sweep` marks a disconnect without ever advancing
`LastSeen` (by design, `LastSeen` must only ever mean "last real message"),
so a disconnect write's own event time is, by construction, tied with
whatever's already stored — rejecting ties would have silently dropped
every disconnect.

This is a **read**-side reconciliation story only. It says nothing about
**dispatch**: once mutation ships, sending a restart/config-push to a
specific agent needs its *live* socket, and nothing above says which
replica currently holds it — that's the harder, separate half of
per-agent sharding already flagged as the reason to defer it (see Jobs:
schema and execution's "dispatch needs the target's live connection"),
resolved below.

#### Load balancing: an LB in front of grex is required, not optional

A multi-replica grex app tier only works if connections actually land on
more than one replica in the first place. That doesn't happen by itself —
an LB (or equivalent) between the gateway tier and grex is required
infrastructure, not an optional extra layer, for two independent reasons:

- **The observIQ `opampgateway` extension's own load balancing is scoped
  to its own upstream pool, not across grex replicas.** `server.connections`
  (default `1`, configurable) is how many upstream WebSocket connections
  *one gateway* opens, and the gateway does least-connections *across those
  N connections* to spread its own agents evenly. `server.endpoint` is a
  single address — the gateway has no concept of "here are several grex
  backends, choose one." Which replica each of those N connections lands
  on is entirely down to whatever `server.endpoint` resolves to: a bare
  grex-1 address pins every one of that gateway's connections to grex-1
  regardless of the gateway's internal balancing.
- **grex does not do this either.** Already explicit in the admin docs:
  "Automatic gateway discovery or grex-side load balancing of gateways" is
  listed as not provided (`docs/admin/scaling-with-gateways.md`). Nothing
  on the grex side fans a gateway's connections out across replicas.

So spreading connections across grex replicas requires an LB (ideally
least-connections, for the same long-lived-WS reasoning as the rebalancing
discussion above — a round-robin decision only fires at connect time, and
these connections are held open indefinitely) sitting at whatever address
`server.endpoint` points to. Without it, distribution across replicas is
either manual static assignment (each gateway's `server.endpoint`
hand-pointed at a specific replica, no automatic failover) or entirely
accidental (bare DNS round robin at dial time, no load awareness, worse
the smaller a gateway's `server.connections` count is — the common
default of `1` means a single unlucky pin is 100% of that gateway's
traffic on one replica).

This is a distinct hop from any client-facing LB (global or per-DC) in
front of the gateway tier — dropping the latter for a single-DC topology
says nothing about this one; they solve different problems and neither
substitutes for the other.

Even a fully Kubernetes-native path still needs a cloud TCP load balancer
(`Service.type=LoadBalancer`, provisioned through the cloud controller
manager) as the outer hop whenever gateways connect from outside the
cluster network — that hop is unavoidable plumbing, not a design choice,
and is flow-hash/round-robin rather than least-connections. That's fine:
its job is only "get the connection into the cluster," not "pick the
grex replica" — the inner hop below is what needs to be least-connections
aware.

**Decided: Envoy as the inner load-balancing tier**, TCP proxy mode in
front of the grex pods, doing the actual least-connections selection.
Chosen over the IPVS-mode-kube-proxy alternative (no new component, but
not portable across every CNI/managed-k8s flavor) and bare HAProxy
(passthrough-simple, but less useful if Gateway API / Envoy tooling is
already the team's chosen k8s ingress layer) — team already knows Envoy
well, so operational familiarity wins here over marginal simplicity.

**Local dev follow-up (not yet built):** the Compose stack needs Envoy's
admin/stats interface enabled and a Prometheus scrape job added
alongside the existing `grex-server`/`grex-fleet` jobs
(`docs/observability/scraping.md`), so Envoy's own connection-distribution
metrics are visible next to grex's. Exercising this locally also means
Compose running more than one grex instance, which it doesn't do today
(single `grex` service, matching `replicaCount: 1`). Cross-replica
comparison itself needs no new grex metric: `grex_agents_connected`
(Gauge, `docs/observability/metrics.md:101`) is already local in-memory
state per process, scraped per target, comparable across replicas via
Prometheus's own `instance` label today, independent of the Postgres-backed
state work above. Implementation tracked separately.

#### Dispatch routing: agent_connections and cross-replica handoff

Reads self-heal across replicas because any replica can answer from the
shared database. Dispatch can't: a `ServerToAgent` message (config offer,
restart command) only reaches the agent through whichever replica's
process currently holds its live OpAMP socket in memory, and a database
query alone doesn't say which one that is right now.

**Decided: an `agent_connections` ownership table, plus Postgres
`LISTEN`/`NOTIFY` for cross-replica handoff.** Two separate problems, two
mechanisms:

- **Ownership: which replica holds this agent's socket, right now.** A
  new `agent_connections` table, one row per live `instance_uid`, not
  reused from the state-freshness story above since it tracks a different
  thing (socket ownership, not data freshness). Every direct-agent or
  gateway-relayed connection registers here on connect and deregisters (or
  lets a lease expire) on disconnect. Same staleness/lease-expiry problem
  the read-side story already has for a crashed replica that never gets a
  clean disconnect event — reuse whatever TTL/heartbeat mechanism that
  side lands on rather than inventing a second one.
  - `instance_uid` — PK, the socket being tracked.
  - `replica_id` — whichever grex process (pod name, or similar stable
    identity) currently holds the connection.
  - `connected_at`, `last_seen` — `last_seen` is what a lease-expiry sweep
    checks; a replica that crashes without deregistering leaves a stale
    row here until it ages out, same shape as agent staleness eviction.
- **Handoff: getting the message to that replica.** A River worker on
  *any* replica can claim a `job_targets` dispatch row, but only the owning
  replica (per `agent_connections`) has the live socket. That worker looks
  up the owner, then publishes on a Postgres `LISTEN`/`NOTIFY` channel keyed
  to that `replica_id`; the owning replica's listener (subscribed to its
  own channel since startup) picks up the payload and writes it to the
  live connection it already holds. No second broker, consistent with why
  River was chosen over a Kafka-backed queue in the first place — Postgres
  is already the dependency, `LISTEN`/`NOTIFY` doesn't add one.

**Not durable yet, and known.** `LISTEN`/`NOTIFY` delivers to whatever's
listening *right now*; a notification published while the owning replica
is mid-restart or partitioned is simply lost; nothing re-sends it. First
cut accepts that gap, since it degrades no worse than "agent didn't get
the message this dispatch attempt," which River's existing retry/backoff
(see above) already covers on the next attempt as long as
`agent_connections` still points at a live owner by then. Making this
durable, an outbox row per notification, replica acks it, a sweep re-sends
unacked ones past some window, is real future work, not yet designed;
called out here as the known gap rather than deferred silently.

#### Agent state schema

Once fleet state is durable (see state database above), this is the shape
`internal/fleet.Agent` maps to on disk. Decided, not yet built — unlike
permission-table shape and jobs schema above, which stay open. Splits
along the identity/session line the in-memory struct blurs today:

- `agents` — one row per `instance_uid`, durable now that the Supervisor
  gives it a stable value. `first_seen`, `last_seen`; health fields
  (`healthy`, `health_error`, `health_status`, `health_start_time`,
  `health_status_time`, `health_reported`); raw `capabilities` bitmask,
  decoded read-side same as today, no per-bit columns; `identifying`/
  `non_identifying` attributes as JSONB; `missing_attributes text[]` plus
  one `compliance_updated_at` (a per-attribute-key history table was
  considered and rejected — nothing needs "how long has key X been
  missing" yet, and an array plus one timestamp avoids a join on every
  agent-list render); `evicted_at`, null while live, set at soft-delete.
  `packages` stays JSONB here too, same reasoning.
- `agent_effective_config` — `(instance_uid, filename, body)`, one row
  per config file, so the API/UI keep rendering each file separately
  without pulling every agent's config on every list query.
- `agent_session` — `(instance_uid, connected, remote_addr, tls_subject,
  via_gateway, transport, description_reported, sequence_num,
  updated_at)`. As built, `StateStore` is never loaded wholesale into
  `Registry` at startup — reads merge local memory with a live `StateStore`
  query per request instead (see Agent sharding scheme's reconnect-elsewhere
  section). A stored `connected=true` for an agent this replica doesn't
  hold locally is only trustworthy as long as it's fresh: `updated_at`
  (`fleet.Agent.SessionUpdatedAt` once read back) is what `api.StaleConnected`
  checks to correct a stale `connected=true` at read time — the "restored
  connected=true is simply wrong" concern this bullet originally named is
  real, it's just handled at read time against a live row, not at a load
  time that doesn't exist. `sequence_num` is kept for display/debugging
  only; nothing may read it back to resume ordering checks after a
  reconnect, since the agent's own counter isn't guaranteed continuous
  across a restart either.

**Write path: batched, not per-report.** `Report()`/`SetConnected()` run
on the OpAMP message-handling hot path; a synchronous write to Postgres
on every check-in would couple grex's OpAMP latency/availability to the
database's, which is exactly what this design avoids elsewhere (the
in-memory registry is authoritative, the database is a durability layer
under it, not a dependency of the live path). Instead: those calls add
the touched `instance_uid` to a small in-memory dirty set (a map insert
under the lock already held, no I/O), and a separate ticker — its own,
**not** `Sweep`'s — periodically snapshots and clears that set and
issues one batched `INSERT ... ON CONFLICT DO UPDATE` covering everything
changed since the last flush. `Sweep` and the persistence flush are
different concerns (liveness/eviction vs. durability) and must not share
a ticker even if their intervals end up similar. Whatever hasn't flushed
at a crash is simply lost from the database — acceptable, since agents
already re-report everything on reconnect (`ReportFullState`) regardless
of what the database had; durability here needs to survive seconds of
staleness, not be per-message-perfect. `agent_session` churns far more
than `agents`/`agent_effective_config` and its precision matters less (a
connect/disconnect a few seconds stale in the database is fine) —
**built**, not just an option anymore: `persistence.SessionSnapshotter`
snapshots it wholesale every tick for every registered agent, independent
of the dirty set. Necessary, not just cheaper: `agents`/`agent_session`
are dirty-tracked together via the same `Report()` events, so a quiet,
healthy agent sending heartbeats with no reportable field change would
otherwise never re-flush `agent_session` either, and `Connected` would go
stale in the database well before any real problem exists (see Agent
sharding scheme's reconnect-elsewhere section for where this was found and
fixed). Not routed through River: this is continuous background
persistence, not a discrete one-shot task.

**Bounded, observable writes: timeout, concurrency, and metrics — built.**
`Flusher.flushOnce` and `SessionSnapshotter.snapshotOnce` both used to loop
serially, one write at a time, no timeout: a single stuck write (a hung
transaction holding a row lock) could block every other agent queued
behind it in that tick, for an unbounded time. Fixed with three pieces,
together:

- **Timeout.** Every `SaveAgent`/`SoftDeleteAgent`/`SaveSession` call is
  wrapped in `context.WithTimeout(ctx, interval)` — the timeout equals
  that component's own tick interval, so a write can't outlive its own
  next retry opportunity. Self-healing already covers the rest: the next
  tick re-attempts with whatever's currently true, nothing is lost by
  cutting a stuck write off rather than leaving it to hang.
- **Bounded concurrency.** Both loops now run their batch through
  `runConcurrent`, a semaphore-bounded worker pool, instead of a plain
  `for` loop. A stuck write now only occupies one of the bounded slots
  instead of blocking everything queued after it. The bound itself is
  read from `pool.Config().MaxConns` in `cmd/grex/main.go` — self-
  consistent by construction, grex never asks Postgres for more
  connections than pgxpool already knows it has, and no new config field.
  Note this doesn't relate the bound to the *database's* actual capacity:
  pgxpool's own default (unset `pool_max_conns`) is `max(4,
  runtime.NumCPU())` — the grex process's own CPU count, nothing to do
  with Postgres's. A grex container sized with far more CPUs than
  Postgres can sustain concurrent writes for would still saturate the DB;
  see the pool metrics below for how that shows up.
- **Metrics**, same interface-in-`persistence`/implemented-by-`metrics.Events`
  pattern as `PurgeMetrics`: `grex_persistence_write_duration_seconds{op}`
  (Histogram, every attempt, success or timeout) and
  `grex_persistence_write_timeout_seconds{op}` (Gauge, set once at
  construction) — the comparison line an operator needs, average/
  percentile duration approaching or crossing the timeout signals DB,
  network, or load trouble before writes actually start failing.
  `grex_persistence_pool_acquired_conns`/`grex_persistence_pool_max_conns`
  (`persistence.PoolCollector`, scrape-time, mirrors
  `metrics.FleetCollector`'s shape) give the sharper, more direct signal
  for the CPU-vs-database-capacity mismatch above: acquired pinned at max
  means the pool itself, not just one slow query, is the bottleneck.

Explicitly not done here, separate future work: SQL-level batching
(`pgx.Batch` or multi-row `VALUES`) to cut the total round-trip count
further. Considered and deferred — it trades away per-agent fault
isolation unless implemented carefully, and total round-trip count wasn't
the demonstrated bottleneck; the metrics above exist partly so that
decision has real data behind it if it's revisited.

**Multi-writer safety: a conditional `UPSERT`, no separate lock.** Once
more than one replica can flush the same `instance_uid` (see
reconnect-elsewhere above — no connection affinity, an agent can talk to
a different grex every connection), concurrent flushes for the same row
need to converge correctly regardless of which one reaches Postgres
first. One atomic statement handles it, no explicit locking needed:

```sql
INSERT INTO agents (instance_uid, ..., updated_at)
VALUES ($1, ..., $N)
ON CONFLICT (instance_uid) DO UPDATE
SET ..., updated_at = EXCLUDED.updated_at
WHERE agents.updated_at < EXCLUDED.updated_at;
```

`ON CONFLICT DO UPDATE` already takes the row lock as part of this one
statement; Postgres serializes concurrent attempts on the same row on its
own. The `WHERE` guard is the entire mechanism — a stale flush simply
becomes a no-op (0 rows affected) instead of clobbering a newer write, no
history kept, nothing else to coordinate. The value written to
`updated_at` must be event time (`Agent.LastSeen`, the actual OpAMP
message timestamp already tracked), not flush wall-clock time — flush
scheduling jitter differs per replica and per tick, and using it instead
would reject genuinely newer data over an artifact of when a batch
happened to run. Same shape applies to `agent_session` and
`agent_effective_config`, each keyed on its own relevant timestamp. Worst
case — an agent reconnecting to a different replica on every connection —
converges correctly under this: whichever write carries the latest event
time wins, independent of arrival order at Postgres.

**Soft delete and retention** (reverses 1.0's "no tombstones" behavior;
see Decided). A stale agent is soft-deleted (`evicted_at` set) instead of
removed outright, kept for `fleet.soft_delete_duration` (new config
field, default `7d`) so a caller can still see "last seen, agent gone"
history. `GET /api/agents` never includes soft-deleted agents. `GET
/api/agents/{instance_uid}` excludes them by default too, but accepts
`?show_soft_deleted=true` to fetch one directly — the direct-lookup
endpoint is where "what happened to this agent" questions land, so it
gets the escape hatch the list endpoint doesn't.

Hard-delete (purging rows past `soft_delete_duration`) runs as its own
periodic job, not on `Sweep`'s heartbeat-interval ticker: heartbeat
intervals are tens of seconds, far finer-grained than a multi-day
retention window needs, so checking every tick buys no precision.
River (already in the stack, see Jobs below) has a native periodic-job
scheduler — a good fit. Hourly cadence is plenty (worst case a row lives
`soft_delete_duration` plus about an hour).

Metrics: `grex_agents_evicted_total` keeps its current name and trigger —
it already fires at the "agent left the fleet" moment, which is now the
soft-delete moment, so no rename. One new counter,
`grex_agents_purged_total`, covers the hard-delete/GC job. Kept separate
so a dashboard can show fleet churn (soft-delete rate) independently of
storage cleanup (purge rate) — the two can drift arbitrarily far apart
depending on `soft_delete_duration`.

Read API additions, not yet built:

- A `last_seen_within` filter (duration, e.g. `?last_seen_within=5m`) on
  `GET /api/agents`, same well-known-filter mechanism as
  `healthy`/`connected`/`via_gateway`. `connected` is necessarily coarse —
  it only flips on `Sweep`'s heartbeat-interval cadence, so it can read
  "connected" for up to one full `heartbeat_interval` after an agent's
  last real message. A precise timestamp-window filter is a cheap
  addition (`last_seen` is already a column) and useful independently of
  the reset-on-load rule above — it doesn't replace that rule, it's for
  callers who want their own recency threshold rather than trusting the
  boolean.
- `?show_soft_deleted=true` on `GET /api/agents`, same flag name as the
  single-agent endpoint's escape hatch but exclusive, not additive: when
  set, the list returns *only* soft-deleted agents (a graveyard view —
  "what vanished this week") instead of the default live-only set, never
  both mixed together. Other filters (attribute match, `last_seen_within`)
  still apply, now against the soft-deleted set — e.g.
  `?show_soft_deleted=true&service.name=otelcol-contrib` finds a specific
  agent that's gone. Agent JSON exposes `evicted_at` (null for live
  agents) so a caller can tell live and soft-deleted entries apart even
  without the flag, and see when a soft-deleted one was evicted.
- A computed `supervisor_managed` field/filter, same pattern as `role`:
  best-effort, not authoritative. **Known gap:** OpAMP gives a server no
  reliable, declared signal that an agent is Supervisor-managed versus
  running the bare `opamp` extension directly. The heuristic available
  today correlates `identifying_attributes["service.instance.id"]` with
  `instance_uid` (the Supervisor's self-telemetry template sets them
  equal; the bare extension never does, so a match is a strong but
  undeclared signal) and/or default capability bits
  (`reports_own_metrics`/`reports_heartbeat`), which are plain
  user-editable config on both sides and the weaker of the two. Both are
  workarounds, acceptable to ship on for now, but brittle: neither is a
  documented contract, so either can silently stop working across an
  upstream Supervisor change with no deprecation path. A proper fix
  needs an upstream change to the reference Supervisor (a dedicated,
  declared `AgentDescription` attribute); tracked separately, not yet
  filed.
- `GET /api/agents` (list endpoint) gaining the same `StateStore`
  fallback as `GetAgent`, merged with local registry data instead of a
  plain miss-then-lookup: registry entries win on overlap (same
  precedent as `GetAgent`'s single-agent fallback — local, authoritative
  data always beats a possibly-stale DB row), DB-only rows fill in
  agents this instance never saw connect locally, dedup happens by
  `instance_uid` before pagination (paginate the merged set, not each
  source separately), and existing filters (`role`, `last_seen_within`,
  etc.) apply identically to both the registry-owned and DB-only
  agents rather than only the local set. Jobs/dispatch routing (which
  replica owns issuing a remote-config push or restart to a given
  agent) is out of scope here, deferred as a separate, future concern
  once this read path is fixed.

#### Jobs: schema and execution

Every 1.0 non-goal that mutates something (remote config push, restart,
package upgrade) needs the same shape underneath: a user (via API first,
UI second) picks a target set of agents by attribute filter — the same
filter language `GET /api/agents` already uses — and an action to perform,
and grex carries that out per matched agent and reports back per-agent
outcome. This requires the Supervisor work above to have already landed as
the standard client by the time mutation ships (see **Hard requirement:
Supervisor adoption** below — not a soft assumption), so `instance_uid` is
stable across an agent's restarts; job targets don't need to account for an
in-flight job's targets churning through eviction/re-registration.

[River](https://github.com/riverqueue/river) is a sound base for the
execution side: Postgres-native (`SELECT ... FOR UPDATE SKIP LOCKED`, no
second broker to run), Go-native, and grex is already headed toward
Postgres for the state database above — one database, not two pieces of
infrastructure. Two things it doesn't give for free:

- **River jobs are flat; a mutation job is two levels.** A `jobs` row
  (user's intent: filter, action, submitted-by, overall status) expands to
  one `job_targets` row per matched `instance_uid` (per-agent status). Each
  `job_targets` row becomes one River job — the *dispatch attempt* for
  that agent — so River's retry/backoff covers "agent not currently
  connected, try again." Parent-child job dependencies (River Pro's
  Workflows) aren't needed: targets are independent, and the parent's
  overall status is a rollup query over its `job_targets`, not a DAG.
- **Dispatch and completion are different events.** A River job finishing
  only means grex handed a `ServerToAgent` message (config offer, restart
  command, package offer) to the agent's live OpAMP connection — it marks
  `job_targets.status = sent` or `send_failed` (agent not connected). The
  actual outcome arrives later and asynchronously, from the agent's own
  next check-in (`RemoteConfigStatus: APPLYING`/`APPLIED`/`FAILED`, or the
  equivalent for packages/restarts) — the OpAMP inbound-message handler is
  the second writer to `job_targets`, setting `applied`/`failed`, not the
  River worker.
- **Dispatch needs the target's live connection.** The one-logical-Postgres-
  endpoint decision above (Agent sharding scheme) rules out DB-level
  shard routing, but grex itself is still multi-replica — dispatching to
  a specific agent needs whichever replica currently holds its socket,
  not just a database query. See Dispatch routing: agent_connections and
  cross-replica handoff (under Agent sharding scheme) for the decided
  ownership-table-plus-`LISTEN`/`NOTIFY` shape; not yet built.

**Lifecycle, safety, and first action (decided, not yet implemented):**

- **Restart is the first action to implement**, once this shape exists:
  `AcceptsRestartCommand` is already fully handled client-side by the
  Supervisor (see Post-1.0 roadmap: why the Supervisor matters), so it's
  the least new client-side work of the three deferred mutation features.
- **Success is action-specific**, computed by the same OpAMP
  inbound-message handler that's already the second writer to
  `job_targets`. For restart specifically: success is the target's next
  health report showing a newer `health_start_time` than the dispatch
  time — no protocol extension needed, `Agent.HealthStartTime` is already
  tracked. The timeout before an un-reconnected target is marked `failed`
  (some multiple of `heartbeat_interval`) is configurable per job via
  `action_config` below, not a hardcoded constant; no default chosen yet.
- **Retry/backoff**: fibonacci, capped, for a `job_targets` dispatch
  attempt that finds its agent not currently connected. The cap is also a
  per-job `action_config` value, same mechanism as the reconnect timeout
  above, not a global constant; no default chosen yet.
- **`action_config`: a per-job, action-specific config blob** (`jsonb` on
  the `jobs` row), holding whatever knobs that action type needs —
  restart's `reconnect_timeout` and `backoff_cap` above, config-push's
  would hold different fields later. One schema shape shared across
  action types rather than a new column per action.
- **Owning-replica crash mid-dispatch: detectable, not self-healing.**
  Retry/backoff above only covers the target not being connected *when a
  worker tries to dispatch* (`send_failed`, retried immediately). A
  different case: the owning replica commits `job_targets.status = sent`
  (see Dispatch routing: agent_connections and cross-replica handoff),
  then dies before the agent's completion status arrives. The agent's
  connection dies with it, it reconnects to a different replica, and
  `agent_connections` self-heals for that reconnect same as reads do — but
  the `job_targets` row stays `sent` with no completion status, and
  nothing re-examines it. The timeout above (un-reconnected target marked
  `failed`) is meant to catch this once its number is chosen, but marking
  it `failed` is not itself specified to trigger a new dispatch attempt.
  The only thing that currently notices is the 1.1 compliance check
  below, run on demand by a human, who creates a follow-up job for
  whatever it lists as non-compliant. No automatic retry closes this loop
  today; that's consistent with jobs being point-in-time rather than
  continuous (see Known scope boundary below) — an automatic version of
  this is really the same shape as the parked Declarative/policy layer
  idea (see Open questions), not something jobs do on their own.
- **Authorization**: `viewer` can read jobs and their status; `admin` can
  read and create. The first place the `viewer`/`admin` split (see
  Permission table schema) actually differs in behavior. For restart,
  nothing in `action_config` or the result is sensitive, so `viewer` sees
  the full job. Action types whose `action_config` or result *can* carry
  secrets (config-push's config body, for one) are the harder case:
  field-level redaction was considered and rejected — an unredacted field
  silently added to some future exporter config would leak, with nothing
  likely to catch it before it ships — in favor of a coarser rule:
  `viewer` gets a summary only (job type, status, overall error/counts),
  never the raw `action_config` or per-target detail, for any action type
  marked potentially-sensitive. Binary per action type, not per field.
  Rough plan only; the mechanism itself isn't designed yet, revisit once
  config-push (the first action type that actually needs it) is real.
- **Surface: API only for now**, `POST /api/jobs`; no UI form yet. Request
  body: `{filter, action, action_config}` — filter and action required,
  `action_config` optional and action-specific.
- **`POST /api/jobs` has no pool-sizing or backpressure story yet, known
  gap.** Built (`internal/api/jobs.go`), but not load-tested past unit
  scale. Three specific things, not yet addressed: the DSN built in
  `cmd/grex/main.go` sets no `pool_max_conns`, so pgxpool falls back to its
  own default (`max(4, NumCPU())`) with no config knob to raise it; the
  handler's `CreateJob` call carries no explicit write timeout of its own
  (unlike every DB write on the Flusher path, which goes through
  `writeWithTimeout`), so a request queuing on pool exhaustion just holds
  its goroutine rather than failing fast with `503`/`429`; and the request
  body has no `http.MaxBytesReader` cap. None of this deadlocks or
  corrupts anything — the query itself is a single-row `INSERT` with a
  random UUID PK, no lock contention on the Postgres side — but a
  sustained high-volume burst (tested against: ~3300 req/s, i.e. 1M jobs
  in a 5-minute window) would queue rather than shed load, trending toward
  unbounded goroutine/memory growth instead of graceful degradation.
  Fix, if this rate becomes a real target: expose `pool_max_conns` through
  config, bound `CreateJob` with the same `writeWithTimeout` pattern
  (returning `503` on timeout), cap request body size. Deliberately not
  done here — evaluate for its own PR once real throughput needs are
  known, rather than tuning against a number nobody's asked for yet.
- **Create and execute are separate calls.** `POST /api/jobs` creates a
  job in `planned` status — filter and action recorded, nothing
  dispatched, matched-target count/list can be previewed before anything
  runs. A separate call arms it. This is deliberate: it's what lets a
  future UI show "this will restart N agents, are you sure?" against a
  real preview rather than a guess, and it's the same shape the safety
  behavior below needs regardless of whether a UI exists yet.
- **Target computation: `recompute` (default) or `freeze`**, an option on
  the arm call. `recompute` re-evaluates the filter against live fleet
  state; `freeze` locks the matched-target set at the moment the job was
  armed. Either way, recompute (when selected) happens at the *end* of
  the cancellation delay below, immediately before real dispatch — not at
  plan time and not at the moment arming was requested — since that's the
  moment work actually happens and the moment fleet membership should be
  read as of.
- **Every armed job waits 5 minutes before dispatch actually begins**,
  cancellable during that window. This is a blanket safety rule, not
  per-action or configurable per job: arming is not the same moment as
  running. Natural fit for River's scheduled-job support: arming inserts
  one job scheduled 5 minutes out whose work is "recompute targets if
  requested, then insert the real per-`job_targets` dispatch jobs";
  cancelling before it fires marks the job `cancelled` and prevents that
  scheduled job from ever doing its work.

**Hard requirement: Supervisor adoption, not a soft assumption.** A job's
compliance check needs its target's `instance_uid` to survive the action
it's checking for — restart's `health_start_time > dispatch_at` predicate
only means anything if the agent that reconnects *is* the same
`instance_uid` that was dispatched to. The bare OpAMP extension churns
`instance_uid` across a restart; the Supervisor doesn't (see Post-1.0
roadmap: why the Supervisor matters). So this isn't "works better with
Supervisor," it's "cannot report success or failure at all without it" —
there's no partial-credit fallback, and building one isn't grex's job:
agents already ship their own otelcol exporter metrics/logs to whatever
central system operators already watch, so "did N agents come back" stays
answerable there, outside grex, for anyone not running Supervisor.

**Decided: per-target rejection with a reason, not a whole-job reject and
not a silent filter.** A matched target that isn't `supervisor_managed`
(`fleet.SupervisorManaged`, `GET /api/agents?supervisor_managed=`) is
recorded as a rejected `job_targets` row with a reason at arm time, not
silently dropped (an operator expecting N restarts seeing fewer with no
explanation) and not a hard failure for the rest of the job's targets (one
non-compliant agent in a filter shouldn't block dispatch to the rest of the
matched fleet). The armed job's response/detail view surfaces the rejected
list — instance_uid plus reason — alongside the real dispatch targets, so
what got excluded and why is visible up front, before anything dispatches.

Deliberately the first entry in an extensible reason set, not a one-off
special case for Supervisor adoption: a future blast-radius rule (e.g. a
cap on how many agents one job can target) is expected to reuse this same
rejected-with-reason shape rather than invent its own rejection path.
The `job_targets.status = 'rejected'` value, its `reason` column, and the
partitioning logic itself (`api.PartitionJobTargets`) are built. Not yet
built: the arm endpoint that would actually call it — see Jobs: schema and
execution.

| Job | Requires Supervisor | Why |
|-----|---------------------|-----|
| restart | yes | success predicate (`health_start_time > dispatch_at`) is keyed on `instance_uid` surviving the action |

If every job type ends up requiring it — plausible, since config-push and
package-upgrade need the same identity continuity to report success — this
stops being a per-job table and becomes a single fleet-wide deployment
precondition for using jobs at all. Kept as a table for now in case some
future action type turns out not to need identity continuity (e.g. a
fire-and-forget broadcast with no per-agent success tracking).

**Known scope boundary: jobs are point-in-time, not continuous.**
`job_targets` is materialized exactly once — at freeze time, or at the
end-of-arm-delay recompute moment. Neither mode can guarantee "every
agent that will ever match this filter gets the action," because an
agent can join the fleet after that single snapshot instant, including
mid-dispatch while other targets are still retrying through backoff.
This is not a bug to fix inside the job model; it's a different feature
(a standing/continuous policy — "any agent matching X, whenever it
appears, gets restarted" — its own lifecycle, its own "done" semantics of
"never, until stopped"), deliberately out of scope here. A controller
like that could be built later without conflicting with anything above.

**Compliance check, not the gap's fix but its answer for 1.1.** A
read-only, on-demand `GET /api/jobs/{id}/compliance`: re-evaluates the
job's own `filter` against *live* fleet state (same filter engine
`GET /api/agents` uses, not the frozen `job_targets` list — this is what
catches agents that joined before, during, or after dispatch), and for
each currently-matching agent applies the action's own success predicate
against the job's `dispatch_at` (restart's is already decided:
`health_start_time > dispatch_at`; other actions get their own —
config-push's would be effective-config-hash match, package-upgrade's
would be reported-version match). Reports `compliant/total` plus the
list of non-compliant agents, so a human can act on it — most directly,
by using that list to create a new job against the stragglers. Strictly
read-only and on-demand: it reports the gap, it does not dispatch
anything itself. That boundary is what keeps this from becoming the
continuous-enforcement feature above by accident; it's also exactly what
would sit underneath one, if that's ever built — a compliance check
that already knows how to answer "is this agent in the desired state"
is the natural foundation for a future enforcer, not something that
competes with it.

**Migration tooling (decided, not yet implemented):** two independent
migrators against the one Postgres database, not one merged pipeline.
[`golang-migrate/migrate`](https://github.com/golang-migrate/migrate) owns
grex's own tables (permission tables, `jobs`/`job_targets`) via plain `.sql`
files. River owns its own tables (`river_job`, `river_leader`, etc) via its
own migrator (`river migrate-up` CLI, or `rivermigrate.New(...).Migrate()`
in-process) — River ships new migrations as it versions, and its migrator
tracks them against its own `river_migration` table, separate from
golang-migrate's `schema_migrations`. Copying River's raw SQL into
golang-migrate's own file set was considered and rejected: every River
version bump would then need its new migration files copied over by hand,
silently breaking the worker against a stale schema if one is missed;
running River's migrator on its own carries that forward automatically.
Both migrators run against the same database from the same deploy step;
this is tooling separation, not the "two pieces of infrastructure" the
Jobs section above is avoiding (that's about brokers, not migrators). This
part shipped: `internal/persistence` has real migrations for the agent
state schema above, the `role_mapping` table, `agent_connections`, and
`jobs`/`job_targets` (not stubs), both migrators are wired into the Compose
deploy step, and the write/read paths described in Agent state schema are
implemented and tested. The permission-table and jobs schemas now have real
migrations and CRUD repository code too, tested against real Postgres —
still not built: anything that reads or writes through them (role lookup,
the OpAMP connect/disconnect wiring, the arm/dispatch logic and its River
workers, the API handlers).

#### Config source of truth: sync and apply

Config-push (the job action from the table above) needs a config body from
somewhere. Two options for where that body lives:

- **Inline in the job request.** Simplest: `POST /api/jobs` carries the
  config text directly. Nothing new to build, but the config a fleet ends
  up running is whatever was pasted into an API/CLI call at dispatch time —
  no record of which git commit or Helm release it came from, and no way
  to answer "does the fleet still match what's in git" later.
- **A separate desired-config table, populated only by sync.** The
  config-push job references a stored row instead of carrying the config
  itself. Keeps a human from ever hand-editing config through grex: the
  only writer is a CLI command that reads from an existing IaC source
  (Helm values, a checked-out git repo) and uploads it as-is.

**Decided: separate table, `config_sources`, sync and apply as two CLI
calls.** This is the sync half of the parked Declarative/policy layer
question (see Open questions) — the apply half stays exactly the job model
already decided above (plan/arm/5-minute-delay/dispatch), not a new
continuous reconciler.

- `id` — PK.
- `source_type` (`helm` | `git`) — enum, not a free-text string, so the CLI
  and API agree on what `source_ref` means for each. A future source (e.g.
  an OCI artifact) adds an enum value, not a schema change.
- `source_ref` — the exact origin: a git commit SHA for `git`, a chart
  name + release + revision for `helm`. Captured automatically by
  `grex config sync` at sync time, never hand-entered — this is what makes
  "which commit is this fleet running" answerable later, the actual point
  of tying config to IaC instead of pasting a body into an API call.
- `selector` — the same agent-attribute filter language `GET /api/agents`
  and jobs already use; decides which agents this config is meant for.
- `config` — the rendered config body (or its hash, if the body is stored
  elsewhere and only the hash is needed for the compliance predicate
  already scoped under Jobs: schema and execution).
- `synced_by` — caller identity, same identity model as job creation.
- `created_at`.

`grex config sync` only ever inserts a `config_sources` row: pure write, no
dispatch, no side effect on any agent. `grex jobs apply --config
<config_sources.id>` creates a `jobs` row whose action is `config-push`
pointed at that row's `id` — everything after that is the job lifecycle
already decided (arm, 5-minute cancellable delay, dispatch, compliance
check against the config's hash). The desired state a human can audit
(`config_sources`, tied to a commit) stays separate from whether it was
ever applied (`jobs`/`job_targets`); `config_sources` alone never changes
an agent's config.

Still parked, not resolved here: a continuous/level-triggered mode
("whenever an agent matching this selector checks in out of compliance
with its `config_sources` row, auto-apply") is the harder half of the
Declarative/policy layer question below. This section only removes the
"where did this config come from" gap for the human-triggered path.

## Architecture

```text
  otelcol agents/gateways
  (OpAMP over WS, mTLS)             ┌───────────────────────────────┐
        │                           │            grex               │
        ▼                           │  ┌─────────┐  ┌────────────┐  │ ◄──── browser (HTTPS,
  OpAMP gateway collector(s)  ────► │  │ OpAMP   │  │ HTTP API   │  │       OIDC login via Dex)
  (opampgateway extension,          │  │ server  │  │  + UI      │  │
   N upstream WS conns, mTLS)       │  └────┬────┘  └─────┬──────┘  │ ◄──── /metrics
                                    │       ▼             ▼         │       (Prometheus)
  direct agents (optional)    ────► │     in-memory fleet state     │
                                    └───────────────────────────────┘
```

Single Go binary, three separate listeners on distinct ports so each has its own
auth boundary:

1. **OpAMP endpoint** — WebSocket and plain HTTP transports per the OpAMP spec,
   built on [`open-telemetry/opamp-go`](https://github.com/open-telemetry/opamp-go)
   server packages. TLS terminated here; client certificates (mTLS) required for
   collectors when enabled.
2. **UI listener** — serves the JSON read API and the web UI that consumes it,
   on one port. Guarded by OIDC login and roles. Built as two milestones (API,
   then UI) but not split across listeners: the split is about sequencing
   work and testing the API on its own before a UI sits on top of it, not
   about a different auth boundary.
3. **Telemetry endpoint** — Prometheus `/metrics` (server health) and
   `/metrics/fleet` (fleet series) plus health/readiness probes.

### Health probes

- `/healthz` — liveness only: process is up and its handlers respond. Never
  reflects readiness or a downstream dependency, so it cannot flap on
  transient issues and never changes during a graceful drain. Always 200
  once the telemetry listener is serving.
- `/readyz` — 200 normally; 503 once a graceful shutdown begins
  (`Server.BeginDraining`), before any listener closes. On SIGINT/SIGTERM
  grex flips `/readyz` to 503, waits a fixed `drainDelay` (5s) so an
  orchestrator's readiness probe has time to notice and stop routing new
  traffic, then closes listeners with the existing `shutdownGrace` (10s).
  Compose healthchecks intentionally probe `/healthz`, not `/readyz`, so
  `docker compose` does not report the container unhealthy during that
  drain window.
- **Fixed: persistence used to die the instant the signal arrived, before
  the drain window even started — now it survives the drain window and
  does one final flush.** `Registry.Run`/`Flusher.Run`/
  `SessionSnapshotter.Run` used to share the exact same signal-cancelled
  context the drain sequence gates on, so they stopped at the same instant
  `BeginDraining` fired — the entire `drainDelay` window (agents still
  connected, still sending messages) ran with zero persistence alive to
  catch any of it, a correctness gap independent of fleet size, not a
  scaling one. Now `main.go` gives them their own `persistCtx`, cancelled
  explicitly only after `drainDelay`'s sleep completes; `Flusher.Run`/
  `SessionSnapshotter.Run` each do one bounded final flush/snapshot on
  their own cancellation before returning (a fresh context, since a child
  of an already-cancelled one is instantly done too); `run()` waits
  (`sync.WaitGroup`) for that final flush to actually finish before
  proceeding to `shutdown(srv)`, so the process can't exit mid-flush.
- **New: `opamp.Handler.Drain()` actively refuses new agent connections
  the instant the shutdown signal arrives**, called from the same site as
  `BeginDraining`. Previously nothing in grex itself stopped new OpAMP
  connections during the drain window — only an orchestrator honoring
  `/readyz` kept new agents away, on its own probe cadence. A refused
  client's own connect attempt fails immediately; its existing
  exponential-backoff-with-jitter reconnect (`opamp-go`, not grex code)
  retries and lands on a different, still-ready replica behind the load
  balancer. Already-open connections are untouched — only new attempts are
  refused.

### Debug endpoints

- `/debug/pprof/*` — standard `net/http/pprof` handlers (index, cmdline,
  profile, symbol, trace, and the runtime profiles pprof's index dispatches
  to: heap, goroutine, block, mutex, threadcreate) mounted on the telemetry
  listener. Off by default, gated by `debug.pprof_enabled`
  (`GREX_DEBUG_PPROF_ENABLED`): profiling exposes memory contents and CPU
  profiling is itself a load, so it is opt-in and grex logs a warning on
  startup when enabled. Registered on grex's own mux, never on
  `http.DefaultServeMux`. Intended for operators who can reach the
  telemetry listener but should not be exposed publicly; the telemetry
  listener is already the least-trusted-network-exposed of the three by
  design (see the separate-ports decision), and this makes clear it still
  needs network-level restriction when pprof is turned on.

### OpAMP server

- Library: `open-telemetry/opamp-go`. grex implements the server callbacks;
  it does not fork or reimplement the protocol.
- Transports: WebSocket (primary) and plain HTTP polling, both required by the
  spec for interoperability. Gateway-relayed traffic is WebSocket only.
- Gateway support: grex implements the `com.bindplane.opamp-gateway` custom
  capability. When a gateway relays a new agent it sends a `connect` custom
  message carrying the agent's HTTP headers and remote address; grex answers
  with `connectResult` (accept/reject plus HTTP status). In 1.0 grex accepts
  every agent relayed over an authenticated (mTLS) gateway connection. The
  connect message carries no agent instance_uid, so the forwarded headers and
  remote address cannot be joined to a specific agent entry; grex logs them
  on receipt, and agent entries carry the gateway connection's metadata plus
  a via-gateway marker. The extension is alpha; its version is pinned and
  protocol changes are absorbed at upgrade time. Accepted risk: if the
  extension's maturity or its absence from the pinned observIQ distribution
  becomes a blocker, the fallback is forking the extension and fixing it
  (building it into an OCB image).
- Multiplexing: a single gateway connection carries many agents, so fleet
  state and per-agent handling key on `instance_uid`, never on the socket.
  Connection counts and agent counts are tracked as separate things.
- Capabilities accepted from agents in 1.0: status reporting, effective config
  reporting, health reporting, own-telemetry reporting metadata. Offers from the
  server (remote config, packages, connection settings) are not sent.
- Agent identity: `instance_uid` plus reported `AgentDescription` attributes
  (service.name, service.version, host, OS, etc.). `instance_uid` stability
  is a client-side concern grex has no control over: the bare `opamp`
  extension auto-generates a new UUID on every process start unless the
  deployment pins one, while an [OpAMP Supervisor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/specification/README.md)
  generates one once and persists it across collector (and Supervisor)
  restarts. Fleets that restart collectors without a Supervisor and without
  a pinned `instance_uid` will see eviction/re-registration churn, not a
  grex bug; see the Local development section for how the compose stack
  demonstrates the stable (Supervisor) case.
- Required attributes: a configurable list of attribute keys
  (`fleet.required_attributes`) that every agent's `AgentDescription` must
  carry, checked across identifying and non-identifying attributes. When an
  agent reports without one, grex logs a warning naming the agent and the
  missing keys and increments `grex_agent_missing_attributes_total`. The agent
  is still accepted and displayed; enforcement is observe-only in 1.0.
- Check-in tracking: the server expects each agent to check in (any
  AgentToServer message, heartbeat or otherwise) at least once per
  `heartbeat_interval`. Liveness is two-stage so gateway-relayed agents
  (no per-agent TCP close visible to grex) still surface correctly:
  - Miss one `heartbeat_interval` without a check-in → mark
    `connected=false` (UI: Disconnected). Last reported health is retained;
    missing a check-in is not the same as reporting Unhealthy.
  - Miss `stale_missed_heartbeats` consecutive intervals → stale: evicted
    from fleet state, removed from the UI, counted in
    `grex_agents_evicted_total`.
  Until eviction, a disconnected agent stays visible with its last-seen
  timestamp. A stale agent that returns re-registers as a fresh entry.
  Direct OpAMP connection close still marks disconnected immediately via
  the connection-close callback.

### Fleet state

- In-memory registry keyed by `instance_uid`, holding the latest
  `AgentDescription`, `sequence_num` (detects dropped messages), health
  (`healthy`/`last_error` plus the free-text `status`,
  `start_time_unix_nano`, and `status_time_unix_nano` fields), effective
  config as a filename-to-body map (not a flattened blob, so the API and UI
  can render each file separately), package statuses (name, versions,
  install status, error), capabilities (both the raw bitmask and a decoded
  struct of named booleans for the API), connection metadata (remote
  address, TLS client identity), and timestamps.
- Concurrency-safe; the OpAMP callbacks write, the API reads.
- No persistence in 1.0 (see non-goals). State survives agent reconnects
  because agents re-report on connect. Agents behind a gateway never observe
  a grex restart (their connections are to the gateway), so grex sets the
  `ReportFullState` flag on replies to any agent whose entry has no
  description, and the agent resends everything on its next check-in.

### Read API

- Purpose: JSON view over fleet registry state, consumed by the web UI and
  usable directly (scripting, other tooling) without a browser.
- Three read endpoints on the UI listener (same auth boundary as the UI).
  Until the auth milestones land, the UI listener is open.

#### `GET /api/agents`

Paginated, filtered fleet list. Sorted by `instance_uid` before paging so
pages are stable. Invalid pagination or filter values are 400.

- **Pagination:** `limit` (default 100, cap 1000), `offset` (default 0).
- **Filtering:** any query param other than `limit`/`offset` (and UI-only
  form helpers `attr_key`/`attr_value`) is a filter. Multiple params are
  ANDed. Filtering happens before pagination, so `total` is the filtered
  set size.
  - Well-known top-level booleans (`true`/`false`, invalid → 400):
    `healthy`, `connected`, `via_gateway`. These take precedence over any
    agent-reported attribute of the same name; the registry counts
    shadowing via `grex_agent_reserved_attribute_conflicts_total` (see
    Metrics).
  - Any other key is an exact-match filter against `AgentDescription`
    attributes (`identifying_attributes` first, then
    `non_identifying_attributes`), e.g.
    `?service.name=otelcol-contrib&deployment.environment=dev`.
  - UI freeform attribute form fields `attr_key` + `attr_value` are
    folded into a single attribute filter.
- **Response:**
  `{"agents": [...], "total": N, "limit": L, "offset": O}`.
- **Projection (compact):** list items omit bulky fields the table never
  needs — no `effective_config`, no `packages`. They include registry
  summary fields plus computed helpers:
  - `role` — see role heuristic below
  - `display_name` — `service.name`, else `host.name`, else instance uid
  - `host_name` — `host.name` when present
  - `version` — `service.version` when present
  - `capability_flags` (decoded booleans) alongside the raw `capabilities`
    bitmask

#### `GET /api/agents/{instance_uid}`

Full agent document (every registry field including `effective_config` and
`packages`, plus the same computed helpers). 404 when unknown.

#### `GET /api/status`

Server + fleet summary for the status page:

- grex `version`, `commit`, `go_version`, `started_at`, `uptime_seconds`
- fleet counts: `total`, `connected`, `disconnected`, `healthy`,
  `unhealthy`, `health_unknown` (not yet reported), `awaiting_full_state`

Read-only: no endpoint accepts a write in 1.0.

#### Role heuristic

OpAMP does not define agent vs gateway type. grex exposes a best-effort
`role` string:

1. If `service.component` is set (identifying or non-identifying), use it.
2. Else if `service.name` contains `gateway` (case-insensitive), `"Gateway"`.
3. Else `"Collector"`.

`via_gateway` is a **connection** fact, not a type: agents behind an OpAMP
gateway remain collectors.

### Web UI

- Purpose: visualize the fleet. Read-only. Server-rendered views over the
  read API; no fleet-state logic of its own beyond presentation helpers
  (relative time, role/display name already computed by the API).
- **Pages:**
  - **Fleet overview** (`/`) — filter bar + dense table. Columns: status
    (health + connected), display name (`service.name` primary, `host.name`
    secondary), role, version, attributes (identifying + non-identifying as
    compact chips), via (direct/gateway), transport, last seen, truncated
    instance uid. Connected and not-yet-stale disconnected agents appear;
    evicted agents do not. Filters map to the list API: `healthy`,
    `connected`, `via_gateway`, freeform attribute key/value
    (`attr_key`/`attr_value` → `?key=value`). Pagination prev/next with
    `limit=100`. Auto-refreshes via htmx poll.
  - **Agent detail** (`/agents/{instance_uid}`) — full attributes, health,
    capabilities, connection info, effective configuration (YAML per file
    key). Deep-linkable; 404 page when unknown. **No auto-refresh** (so
    config scroll position is preserved); a manual Refresh button reloads
    the partial on demand.
  - **Server status** (`/status`) — grex version/uptime and fleet summary
    counts from `GET /api/status`; auto-refreshes via htmx poll.
- **Live updates:** [htmx](https://htmx.org/) polls HTML partials on the
  fleet and status pages. Interval from `ui.poll_interval` (default `5s`,
  override `GREX_UI_POLL_INTERVAL`). Filter query string is preserved on
  poll URLs. `prefers-reduced-motion` respected for transitions.
- **Stack:** `html/template` + vendored htmx + hand-written CSS design
  tokens. No Node toolchain, no CDN at runtime. Assets embedded via
  `go:embed` (templates, CSS, htmx, optimized logo mark/favicon — not the
  full multi-MB README logo). If template count grows past what stdlib
  templates handle cleanly, [templ](https://github.com/a-h/templ) is the
  designated escape hatch.
- **Visual system:** dark ops UI complementary to the grex logo palette:
  - Background charcoal/slate `#263135` / elevated `#2A3438`
  - Teal borders/rings `#2E5B5E`
  - Cream text `#F5EFE6`
  - Mint/sage accent `#98C9B1` (healthy, focus, links)
  - Muted grey `#869296` (secondary text)
  - Semantic unhealthy/warning accents (coral/amber) for status
  - Dense dashboard spacing, system UI + monospace for uids/config
  - SVG status icons (no emoji); visible focus rings; 4.5:1 contrast on
    body text

### AuthN/AuthZ

- **Collectors (OpAMP endpoint):** TLS termination by grex. Optional required
  mTLS: client certificate verified against a configured CA bundle. Two hops
  in the gateway topology: agents present client certificates to the OpAMP
  gateway's listener, and the gateway presents its own client certificate to
  grex. grex records the gateway's certificate identity plus the forwarded
  per-agent remote address; direct agents get their own certificate identity
  recorded.
- **Users (UI/API):** shipped in two milestones, both feeding the same role
  table.
  - **mTLS (first):** required client certificates on the UI/API listener,
    same TLS plumbing as the OpAMP listener. Identity is the SPIFFE ID from
    the cert's URI SAN (`spiffe://<trust-domain>/<path>`), not the X.509
    subject; grex requires exactly one SPIFFE URI SAN per cert and rejects
    certs without one or with a malformed one. Real access control for
    `/api/agents` with no external dependency, useful on its own for
    non-browser API consumers.
  - **OIDC (second):** Authorization Code flow against
    [Dex](https://dexidp.io/) for browser session login. Dex federates the
    upstream identity provider; the first connector is GitHub, tied to an
    organization. Dex injects org/team membership as `groups` claims in the
    `id_token`, so grex is a plain OIDC client with claims-based role mapping
    and no provider-specific code. Any other identity provider Dex supports
    works without changes to grex.
- **Roles:** simple and static in 1.0.
  - `viewer` — can see everything the UI shows.
  - `admin` — same as viewer in 1.0 (mutation comes later); the role exists now
    so tokens/sessions carry it and 1.1+ can gate mutations on it.
  - Role assignment via configuration, same table shape for both identity
    sources: map of SPIFFE ID (or a path prefix of it) or OIDC `groups`
    claim values (e.g. GitHub `org:team`) to role, plus a default role for
    authenticated callers (may be "none" to deny by default). Callers with
    no mapped identity get no role and no access.
- **Deployment note:** Dex is a required companion service in production. It
  ships in the compose stack for dev and is documented as a prerequisite for
  deployment.
- Sessions: encrypted cookie sessions; no server-side session store (consistent
  with no-database constraint).

### Metrics

grex exposes Prometheus metrics on the telemetry listener as two separate
endpoints, one per group:

- `/metrics` — server health (Go runtime, process, OpAMP message counters).
- `/metrics/fleet` — fleet health (everything prefixed with agent/gateway
  semantics below).

They are separate so operators can scrape them as independent jobs:

1. Different scrape economics: server internals are cheap and useful at short
   intervals; fleet series change at heartbeat granularity and cost a fleet
   snapshot per scrape, so they suit a slower interval.
2. Independent protection: Prometheus `sample_limit` is per scrape job. A
   fleet cardinality blowout trips only the fleet job, leaving the server
   health samples needed to diagnose it intact.
3. Limit scoping: `metrics.per_agent_series_limit` governs only fleet data;
   a fleet-only endpoint makes that boundary visible.
4. Routing: fleet series can go to a different Prometheus/Mimir tenant with
   shorter retention than server SLO metrics.

The two groups:

**Fleet health** (the point of the product):

- `grex_agents_connected` (gauge, by `transport`: ws/http and `via`:
  direct/gateway; agent type is not derivable from the protocol and carries
  no label)
- `grex_gateway_connections` (gauge: multiplexed gateway connections open)
- `grex_gateway_connects_total` (counter, by result: accepted/rejected
  `connectResult` answers)
- `grex_agents_disconnected` (gauge: retained agents awaiting return, not yet
  evicted)
- `grex_agents_evicted_total` (counter: agents removed after missing the
  check-in threshold)
- `grex_agent_health` (gauge, by instance_uid: 1 healthy / 0 unhealthy;
  omitted for agents that have not yet sent a health report, so a server
  restart cannot read as a fleet-wide health drop)
- `grex_agents_awaiting_full_state` (gauge: agents registered without a
  description yet, i.e. the post-restart convergence window while
  `ReportFullState` requests are answered)
- `grex_agent_last_seen_timestamp_seconds` (gauge, by instance_uid)
- `grex_agent_reports_total` (counter, by `type`: status, health,
  effective_config)
- `grex_agent_missing_attributes_total` (counter, by attribute key: agent
  reports lacking a required AgentDescription attribute)
- `grex_agents_noncompliant` (gauge: agents currently missing at least one
  required attribute)
- `grex_agent_reserved_attribute_conflicts_total` (counter, by attribute key:
  an agent reported an AgentDescription attribute matching one of the read
  API's well-known filter fields, `fleet.ReservedAttributeKeys` —
  `healthy`/`connected`/`via_gateway` — so that attribute is permanently
  shadowed for API filtering. Fires once per key when the conflict set
  changes, not on every report, same pattern as the missing-attributes
  counter above)
- `grex_agent_connects_total` / `grex_agent_disconnects_total` (counters)

**Server health:**

- `grex_build_info{version,commit,go_version}` (gauge, always 1; standard
  Prometheus info-metric pattern, version comes from `-ldflags` at build time
  via `internal/buildinfo`, `dev`/`none`/`unknown` when unset)
- `grex_config_info{log_level,log_format,tls_enabled,mtls_enabled,
  heartbeat_interval,stale_missed_heartbeats,per_agent_series_limit}` (gauge,
  always 1; non-secret settings only, lets a dashboard confirm a config
  rollout actually took effect without grepping logs)
- `grex_opamp_messages_total` / `grex_opamp_message_errors_total`
- `grex_api_requests_total{route,method,code}` /
  `grex_api_request_duration_seconds{route,method,code}` — every read API
  handler is wrapped once via `internal/metrics.HTTPMetrics.Instrument`
  (built on `promhttp.InstrumentHandlerCounter`/`InstrumentHandlerDuration`),
  so new endpoints get request/latency metrics for free by wrapping them the
  same way.
- Auth outcomes (arrives with the auth milestone)
- Go runtime and process metrics (standard collectors)

Cardinality note: per-instance_uid series (`grex_agent_health`,
`grex_agent_last_seen_timestamp_seconds`) are capped by
`metrics.per_agent_series_limit` (default 1000); above the cap they are
omitted entirely and only aggregates remain. The cap firing is itself
observable, not just inferable from per-agent series disappearing:
`grex_fleet_size` (gauge, total registered agents) is always emitted, and
`grex_agent_series_capped` (gauge, 1/0) is explicit about whether the cap is
currently suppressing per-agent series. Alert on
`grex_agent_series_capped == 1`.

Exposing `/metrics` for Prometheus scrape is the only export path in 1.0; no
OTLP export of grex's own telemetry.

### Configuration

Single YAML config file plus environment variable overrides. Covers listeners,
TLS cert/key paths, mTLS CA bundle, OIDC provider settings (client id/secret,
issuer), role mappings, heartbeat interval, stale check-in threshold, required
agent attributes, metric cardinality cap.

## Local development

`docker compose up` brings up:

1. **grex** — built from the local source (compose `build:`), TLS enabled with
   generated dev certificates.
2. **OpAMP gateway × 1** — a collector running the observIQ `opampgateway`
   extension: TLS listener for the agents (dev certs), N upstream WebSocket
   connections to grex with its own client certificate. The image is a small
   OCB build (`deploy/compose/opamp-gateway-build/`) whose manifest replaces
   the extension module with a commit on
   `dennisme/bindplane-otel-contrib@dennisme/fix-server-tls-config`: extension
   v1.10.0 declares upstream TLS settings but never wires them into its
   websocket dialer, so the stock observIQ distribution image cannot reach a
   private-CA/mTLS OpAMP server. That is the
   accepted fork-and-fix risk, realized; the fork branch is a candidate for
   an upstream PR. Once a tagged release ships the fix, drop the `replaces`
   entry and switch back to the stock `observiq/observiq-otel-collector`
   image.
3. **otelcol agent × 2** — OpenTelemetry Collector containers pointed at the
   OpAMP gateway, each generating some internal telemetry so the fleet view
   has real data. The two agents intentionally run under the two different
   client-side OpAMP models grex must support, so both are exercised in dev:
   - **agent-1**: bare `opamp` extension, as today. The extension talks
     OpAMP directly (through the gateway) and implements only
     `ReportsStatus`/`ReportsEffectiveConfig`/`ReportsHealth`.
   - **agent-2**: [OpAMP Supervisor](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/specification/README.md)-managed.
     The Supervisor is a separate process that starts/stops/watchdogs the
     collector, and itself speaks OpAMP (through the gateway) on the
     collector's behalf; the collector's own `opamp` extension talks to the
     Supervisor over localhost, not to grex. This is the deployment model
     real production fleets are expected to use, since it's the only one
     that gets a stable `instance_uid` (see below) and process supervision.
   - No grex-side protocol changes needed either way: a Supervisor is just
     another OpAMP client relaying the same message shapes, and it typically
     reports more capability bits true by default (`reports_heartbeat`,
     `reports_effective_config`, `reports_own_metrics`) — grex already
     decodes the full `AgentCapabilities` bitmask (`fleet.Capabilities`), so
     this is purely observational.
   - Image: no OCB build needed (the Supervisor isn't a collector
     distribution). Multi-stage Dockerfile: pull the standalone, Cosign-signed
     `opampsupervisor` binary from
     [`open-telemetry/opentelemetry-collector-releases`](https://github.com/open-telemetry/opentelemetry-collector-releases/releases)
     (pinned to `v0.157.0`, matching the otelcol-contrib pin already used),
     and `COPY --from=otel/opentelemetry-collector-contrib:0.157.0` for the
     managed collector binary.
   - Config split: the Supervisor's own `supervisor.yaml` carries
     `server.endpoint`/TLS (same client cert/CA already generated for
     agent-2, pointed at the OpAMP gateway) and `agent.config_files`
     referencing agent-2's existing hostmetrics/otlp config (its `opamp:`
     extension block is removed — the Supervisor injects
     `$OPAMP_EXTENSION_CONFIG` itself, pointed at its own local
     `opamp_server_port`, not at the gateway).
   - `storage.directory` must be a named Docker volume (not an anonymous or
     container-local path), because that's where the Supervisor persists the
     `instance_uid` it generates on first run: without a persistent volume,
     every `docker compose down`/`up` (or container recreation) would mint a
     new UUID and orphan the old fleet entry, the exact churn risk noted in
     the metrics cardinality discussion. A plain `docker compose restart`
     already avoids this today (container, and its filesystem, isn't
     recreated) — the volume matters specifically across recreation.
4. **otelcol gateway × 1** — a collector configured in OTLP gateway topology,
   receiving from the two agents, its own OpAMP connection (bare `opamp`
   extension) also routed through the OpAMP gateway. This satisfies "more
   than one otelcol agent or gateway" and demonstrates mixed fleet roles.
5. **Dex** — the OIDC issuer grex authenticates against. In dev it runs with
   static test users (Dex `staticPasswords`) carrying group claims that map to
   both roles, so the full login flow is exercised offline without GitHub
   credentials. Production swaps the connector config to GitHub; grex config
   is unchanged.
6. **Prometheus** — scrapes grex with two jobs mirroring production layout:
   `grex-server` on `/metrics` at the default interval, `grex-fleet` on
   `/metrics/fleet` at heartbeat granularity with its own `sample_limit`. Two
   more jobs support the future state/jobs backend below: `grex-postgres`
   (real, scrapes `postgres-exporter`) and `grex-river` (a placeholder
   target, expected down until a future goal gives it something to scrape).
7. **Dev certificate generation** — an init step (script or one-shot container)
   that mints a local CA, server certs for grex and the OpAMP gateway, and
   client certs for the collectors so mTLS is exercised on both hops in dev,
   not just prod.
8. **Postgres + postgres_exporter** — dev-only infra for the future durable
   state and job dispatch backend (see Post-1.0 roadmap: state database and
   sharding, Jobs: schema and execution, and `internal/persistence`'s
   interface stubs). Nothing in grex reads or writes to this database yet;
   `internal/config`'s `database` block carries connection settings but is
   unused by any runtime path. Two migrators run against it independently
   before `grex` starts, per the migration-tooling decision above:
   `river-migrate` (`cmd/river-migrate`, one-shot) creates River's own
   tables via its own migrator, and `migrate` (`migrate/migrate` image)
   applies `internal/persistence/migrations` — agent state, `role_mapping`,
   `agent_connections`, and `jobs`/`job_targets` all now have real
   migrations (see Jobs: schema and execution), though nothing reads or
   writes through the latter three yet.

The stack currently runs with agents connected straight to grex; inserting the
OpAMP gateway service happens together with the OpAMP core milestone, since
the gateway's connect handshake needs grex to answer `connectResult`.

## Release engineering

- **CI (GitHub Actions):** on every PR and push to main run `golangci-lint`,
  `go test ./...` with race detector, and a build of the compose stack to keep
  the dev environment honest. Chart changes additionally run `helm lint`,
  `helm template`, and a kind end-to-end install via `deploy/charts/smoke.sh`
  (`.github/workflows/helm.yml` job `e2e-kind`): build the Dockerfile image,
  load into kind, `helm upgrade --install`, assert `/healthz` `/readyz`
  metrics API UI, and `helm test`. The same script is the local path for kind
  or k3d (`just helm-e2e-kind` / `just helm-e2e-k3d`).
- **Versioning:** semver computed with [`svu`](https://github.com/caarlos0/svu)
  from conventional commit history; maintainers run `just release-tag`
  (`git tag` + `git push` of `$(svu next)`), which triggers GoReleaser.
- **Releases:** [GoReleaser](https://goreleaser.com/) builds:
  - Binaries: linux/darwin, amd64/arm64, checksums, archives attached to the
    GitHub Release.
  - Docker images: multi-arch, pushed to GHCR, tagged with the semver tag and
    `latest`.
- **Helm chart:** source under `deploy/charts/grex`. `just release-tag` bumps
  Chart `version` / `appVersion` to the release SemVer (no leading `v`) so
  the default image tag matches GHCR. Published three ways on each release:
  (1) GitHub Pages chart repo at `https://dennisme.github.io/grex/charts/`
  via the docs workflow after the bump lands on `main`; (2) `.tgz` attached
  to the GitHub Release; (3) OCI push to `oci://ghcr.io/dennisme/charts/grex`.
  Pages path is shared with MkDocs docs and the static demo (single deploy).
- License scanning and dependency updates (Dependabot) enabled from the start.

## Feature list, whittled

| # | Feature | 1.0 |
|---|---------|-----|
| 1 | Go implementation on opamp-go | Yes |
| 2 | Web UI for fleet visualization | Yes, read-only |
| 3 | OIDC/OAuth login via Dex, GitHub connector first | Yes |
| 4 | Roles | Yes, static viewer/admin; no mutation anywhere |
| 5 | Docker Compose local dev | Yes |
| 6 | OpAMP gateway (observIQ opampgateway extension) between agents and grex | Yes |
| 7 | Extensive fleet + server metrics | Yes |
| 8 | Compose includes multiple collectors | Yes, 2 agents + 1 gateway |
| 9 | TLS termination, mTLS clients | Yes |
| 10 | Binary + Docker releases | Yes |
| 11 | CI/CD, golangci-lint, svu, GoReleaser | Yes |
| 12 | Helm chart (K8s deploy + chart repo on Pages `/charts/`) | Yes |
| — | Remote config push | No, 1.1+ |
| — | Package management | No |
| — | Persistent storage | No |
| — | Multi-tenancy | No |
| — | Multi-replica grex HA | No (in-memory state; chart `replicaCount: 1`) |

## Execution plan (milestones)

1. **Skeleton** — repo layout, config loading, logging, CI with lint + tests.
2. **OpAMP core** — opamp-go server wired up, in-memory fleet state, WS + HTTP
   transports, TLS + mTLS, `com.bindplane.opamp-gateway` capability
   (connect/connectResult handling, forwarded connection metadata), compose
   stack amended to route collectors through the OpAMP gateway service.
3. **Telemetry** — Prometheus metrics, health probes.
4. **Read API** — JSON endpoints over fleet registry state: fleet overview,
   agent detail, server status.
5. **Web UI** — embedded `html/template` + htmx pages rendering the read API
   (fleet overview with server-side filters, agent detail, server status;
   configurable poll interval; logo-complementary dark theme).
6. **Auth: mTLS** — **Shipped.** TLS termination plus optional client
   certificates on the UI/API listener (`ui_tls`) and the telemetry listener
   (`telemetry_tls`), reusing the mTLS plumbing built for the OpAMP listener
   in milestone 2 (now `opamp_tls`). Unlike the OpAMP listener, the UI and
   telemetry TLS handshakes accept a connection with no client certificate
   (`tls.VerifyClientCertIfGiven`, not `RequireAndVerifyClientCert`); grex
   still rejects the request afterwards, but only per-route, so `/healthz`
   and `/readyz` on the telemetry listener stay reachable for orchestrator
   probes that cannot present a certificate, while `/metrics`,
   `/metrics/fleet`, `/debug/pprof/*`, and every UI/API route require one.
   Identity comes from the client cert's SPIFFE ID (URI SAN,
   `spiffe://<trust-domain>/<path>`), not the X.509 subject: grex requires
   exactly one SPIFFE URI SAN per cert and rejects certs that lack one or
   carry a malformed one. Authorization maps SPIFFE ID (or a path prefix of
   it) to role via `auth.role_mapping` / `auth.default_role`, same shape as
   the `groups`-to-role table OIDC uses in milestone 7, so the role table's
   mechanism does not change when OIDC lands, only the identity source
   feeding it. `grex_auth_allowed_total{role}` / `grex_auth_denied_total{reason}`
   count outcomes. Agent-to-OpAMP-gateway mTLS/SPIFFE alignment is separate,
   later work, not part of this milestone.
   - **SPIFFE ID path format**: two namespaces under one trust domain, so the
     role table's prefix matching stays unambiguous between a human using a
     personal dev cert (the realistic case before milestone 7 ships) and a
     permanent automation caller (CI, scripts, dashboards) hitting the same
     listener at the same time:
     - Humans: `spiffe://<trust-domain>/user/<username>`
     - Services/automation: `spiffe://<trust-domain>/service/<name>`
     - Deliberately not `agent/...` for either: "agent" already means a
       specific thing (an OpAMP-managed collector) throughout this codebase,
       reusing it here would be confusing.
     - This trust domain should stay distinct from any trust domain used for
       collector/gateway identity if the OpAMP-gateway SPIFFE-forwarding
       design (set aside separately) is ever built: UI/API access and fleet
       infrastructure identity are different security boundaries with
       different issuance lifecycles, and sharing a trust domain risks a
       cert valid in one context being accidentally accepted in the other.
       E.g. `spiffe://grex-api.internal/...` vs `spiffe://grex-fleet.internal/...`.
7. **Auth: OIDC** — OIDC client against Dex, claims-based roles, Dex dev
   config with static users; browser session login on top of the mTLS
   groundwork from milestone 6.
8. **Release** — GoReleaser, svu, image publishing, first tagged 1.0.
9. **Local dev** — **Shipped.** Compose stack with collectors and dev certs
   (OpAMP gateway insertion landed with milestone 2). agent-2 runs under an
   OpAMP Supervisor (`deploy/compose/opamp-supervisor-build`,
   `supervisor.yaml`, `otelcol-agent-2.yaml`) instead of the bare `opamp`
   extension agent-1 still uses, exercising both client-side models and
   giving agent-2 a stable, volume-persisted `instance_uid`
   (`opamp-supervisor-data`) confirmed to survive container recreation.
10. **Helm chart** — production deployment shape for Kubernetes; Compose
    stays the dev/functional-testing reference. **Shipped** under
    `deploy/charts/grex` and published at
    `https://dennisme.github.io/grex/charts/` (GitHub Pages path on the
    existing docs/demo site — not a second Pages project). Chart covers:
    grex Deployment + Service exposing the three listeners (opamp, ui,
    telemetry) on their own named ports, matching the separate-ports design;
    Secret/volume mounts for TLS material (`tls.existingSecret`); ConfigMap
    for grex YAML from `values.config` / `listeners` / `tls`; optional
    `prometheus.io/*` Service annotations **or** two `ServiceMonitor`
    resources (`serviceMonitor.enabled`) for `/metrics` and
    `/metrics/fleet` as separate scrape jobs, matching the compose
    Prometheus config; Ingress for the UI listener only; an optional OpAMP
    gateway Deployment + Service (`opampGateway.enabled`) for fleets large
    enough to need connection multiplexing, off by default. Probes wire
    liveness to `/healthz` and readiness to `/readyz` on the telemetry
    port; `terminationGracePeriodSeconds` defaults above drain + shutdown
    grace. **Constraint:** `replicaCount` remains 1 while fleet state is
    in-memory (no HA). Operator docs: `docs/admin/helm.md` and
    `docs/reference/helm-chart.md`.
11. **Grafana + dashboard** — add a Grafana service to the compose stack
    pointed at the existing Prometheus service (already scraping
    `grex-server`, `grex-fleet`, and `otelcol` as separate jobs), and ship a
    checked-in dashboard JSON covering the metrics already defined in the
    Metrics section: fleet health (`grex_agents_connected` by
    `via`/`transport`, `grex_agents_noncompliant`,
    `grex_agents_awaiting_full_state`, `grex_agent_series_capped`), gateway
    (`grex_gateway_connections`, `grex_gateway_connects_total`), server
    health (`grex_opamp_messages_total`, `grex_api_requests_total`/
    `grex_api_request_duration_seconds`), and the `grex_build_info`/
    `grex_config_info` gauges as a table panel. Datasource and dashboard are
    provisioned via mounted config (Grafana's provisioning directories), not
    manual click-through, so `docker compose up` gives a working dashboard
    with zero setup. The same JSON is the artifact operators import into a
    production Grafana when using the Helm chart from milestone 10.

Each milestone is independently testable; TDD applies throughout (unit tests
per package, compose stack as the end-to-end harness).

## Decided

- Separate ports for OpAMP / UI / metrics listeners.
- UI stack: `html/template` + htmx, embedded, no Node toolchain.
- Auth: OIDC against Dex. Dex's GitHub connector ties access to an org;
  org/team membership arrives as `groups` claims and maps to roles.
- Metrics: Prometheus `/metrics` scrape only; no OTLP export in 1.0.
- Stale eviction (1.0, in-memory): an agent that misses N consecutive
  check-ins is evicted and disappears from the UI (no tombstones).
  Evictions are counted in `grex_agents_evicted_total`.
- Agent state retention (post-1.0, not yet implemented): once the durable
  agent state schema lands, stale eviction becomes soft-delete plus
  `fleet.soft_delete_duration` (default `7d`) retention before a separate
  periodic job purges the row, reversing the no-tombstones rule above.
  See Post-1.0 roadmap: Agent state schema.
- Permission table schema (post-1.0, not yet implemented): a single flat
  `role_mapping` table, role set unchanged (`viewer`/`admin`, both
  read-only). See Post-1.0 roadmap: Permission table schema.
- Agent sharding (post-1.0, not yet implemented): sharding Postgres is a
  non-goal for grex — one logical endpoint per fleet, HA via primary plus
  replica(s) (CloudNativePG shape), never per-agent shard routing. See
  Post-1.0 roadmap: Agent sharding scheme.
- Scaling topology: observIQ `opampgateway` extension collectors sit between
  agents and grex, multiplexing agent sessions over few upstream connections.
  grex implements the gateway's custom connect capability and keeps direct
  connections working.
- Post-1.0 migration tooling: `golang-migrate` for grex's own tables, River's
  own migrator for River's tables, run independently against one Postgres
  database. See Jobs: schema and execution. Not yet implemented.
- Config source of truth (post-1.0, not yet implemented): a `config_sources`
  table (`source_type` enum `helm`/`git`, `source_ref`, `selector`,
  `config`), written only by `grex config sync`; `grex jobs apply` creates
  the actual `config-push` job against it. Sync and apply stay two separate
  calls so config never mutates a fleet by itself. See Config source of
  truth: sync and apply.
- Dispatch routing (post-1.0, not yet implemented): an `agent_connections`
  table records which grex replica currently holds each agent's live
  socket; a River worker on any replica looks up the owner and hands off
  the message via Postgres `LISTEN`/`NOTIFY` on a per-replica channel, no
  second broker. Not durable yet — a notification to a replica that isn't
  listening at that instant is lost, covered for now by River's existing
  retry/backoff rather than redelivery. See Dispatch routing:
  agent_connections and cross-replica handoff.
- Load balancing gateway-to-grex connections (required infrastructure, not
  built by grex): the `opampgateway` extension's own least-connections
  logic only spreads a gateway's agents across its own upstream connection
  pool (`server.connections`), never across multiple grex replicas — and
  grex explicitly does not do gateway-side load balancing either. An LB
  is required for any multi-replica grex deployment; without one,
  distribution across replicas is manual static assignment or unweighted
  DNS-at-dial-time luck. Decided: Envoy, TCP proxy mode, least-connections,
  as the inner tier behind whatever cloud TCP LB gets a connection into
  the cluster. Local dev (Envoy metrics + scrape job, multi-instance
  Compose) not yet built. See Load balancing: an LB in front of grex is
  required, not optional.

## Benchmarks (future)

Not built. Scoped here so "benchmark at 1M agents" has a plan instead of
being one vague future ask. Two decoupled tracks, not one benchmark — they
stress different components and don't need the same tooling:

- **DB/schema scale**: does `agents`, `agent_connections`, `job_targets`
  still query/insert fast with ~1M rows (index plans, the bulk
  `INSERT ... unnest` pattern `CreateJobTargets` uses, filter-matching
  queries). No fake agents needed — bulk-seed rows directly (SQL `COPY` or
  the same `unnest` bulk-insert trick, not a per-row loop) and run real
  queries against them. Fully laptop-local, cheap, and the most directly
  useful of the two: it exercises exactly the schema/queries this work
  landed. `cmd/seed-agent` today loops one `SaveAgent` call per row (fine
  for a handful of manual test agents, its actual job) and needs a bulk
  sibling before it's useful at this scale.
- **Live-connection/protocol scale**: 1M real (or faked) OpAMP client
  connections held open, exercising `fleet.Registry`, heartbeats, `Sweep`,
  and eventually job dispatch. Needs a fake-OpAMP-client load generator
  (doesn't exist yet) — enough protocol to look like an agent (connect,
  periodic heartbeat, synthetic `instance_uid`), nothing Supervisor-side,
  consistent with grex's own non-goal of managing real collector
  processes. Not laptop-scale: 1M concurrent sockets/goroutines needs OS
  fd/ulimit tuning and real RAM on whatever runs the fake clients, a
  multi-VM or cloud environment, not a single machine.

**Publish results for both topologies**, direct-to-grex and
agents-behind-`opampgateway` (see Scaling topology under Decided above) —
they're different scaling problems wearing the same "1M agents" number.
Direct topology puts the full 1M-socket burden on grex itself. Gateway
topology multiplexes those 1M sockets down to a much smaller upstream pool
(`server.connections`) into grex, so grex's own bottleneck shifts to
holding a 1M-entry `fleet.Registry` map and demuxing gateway-relayed
messages back to the right `instance_uid`, not raw socket count. The
gateway (plus the fake clients dialing it) is where the socket/fd
concurrency — and the beefy VM(s) — actually needs to live in that
topology.

Depends on one still-open design point: whether arm's `recompute` step
(see Jobs: schema and execution) reads live `fleet.Registry` state, the
DB-fallback-merged view `GET /api/agents` is gaining (see `GET /api/agents`
gaps under Agent state schema), or both. If recompute ends up querying the
same merged view the read API already will, the ingest→arm→`job_targets`
loop can be validated against DB-seeded rows alone — no live connections
needed — and the live-connection track becomes a pure
protocol/network benchmark, decoupled from jobs correctness entirely.
Decide this when arm is actually built, not here.

## Scaling gaps at 1M agents

Found by code review, not the live-connection benchmark above (that's not
built yet) — so these are concrete, source-grounded gaps, not speculation,
but not measured under real load either. Listed most severe first,
specifically so each can become its own PR rather than one big rewrite. No
`O(N²)` or worse found anywhere agents are iterated (checked every
`for range agents`-shaped loop in the tree) — every gap below is O(N) work
running against a fixed period, which still fails at large enough N.

1. **Fixed: `fleet.Registry`'s single `sync.RWMutex` was the one that broke
   first, now sharded.** `Registry.agents` is now `Registry.shards`, a
   `Config.ShardCount`-sized (default 256, configurable) slice of
   independently locked shards; `instance_uid` routes to a shard via
   `fnv32a` (not parsed as a UUID — shard routing doesn't depend on an ID
   format detail this package otherwise has no reason to assume). `Report`,
   `Get`, and `SetConnected` for an agent now lock only that agent's shard.
   This fixes two contention sources, not just the one originally flagged:
   `Sweep` (`registry.go`) walking one shard at a time no longer blocks
   `Report` calls landing on other shards, *and* — the one the original
   framing missed — concurrent `Report` calls for *different* agents no
   longer serialize through one shared lock even with no `Sweep` involved
   at all. `List` and `Sweep` still touch every shard, locking one at a
   time, never all at once.

   **Real tradeoff taken, not a free lunch:** `List()` was atomic (one
   lock, one instant-in-time snapshot of every agent); now it's only
   atomic per shard, since two shards can be mid-update at slightly
   different instants when `List` assembles its result across all of
   them. Accepted: this system already tolerates staleness everywhere
   (heartbeat-based, seconds of imprecision is the existing norm), and a
   sub-request skew across shards is the same kind of imprecision, not a
   new class of problem.

   `Events` implementations must still not call back into the registry —
   now specifically because `List`/`Sweep` touch every shard, so a
   callback landing on the same shard (or calling either of those methods)
   deadlocks, not because of one global lock anymore.
2. **Fixed: per-agent logging inside `Sweep`'s locked loop was `Info`,
   now `Debug`.** Still one line per disconnected/evicted agent, still
   inside the write lock every other `Registry` operation blocks on — that
   part of the shape is unchanged, moving the log call doesn't shrink the
   lock's critical section. What changed: at the default `info` log level
   (every production fleet, unless explicitly turned up for debugging), a
   mass-disconnect event now costs zero log I/O instead of a burst of
   blocking writes inside the lock — `slog` short-circuits a disabled
   level before formatting or writing anything. `grex_agents_disconnected`
   /`grex_agents_evicted_total` already carry the aggregate signal at any
   log level, so nothing observability-relevant was lost. Aggregating into
   one summary line per `Sweep`, or moving the call outside the lock
   entirely, are still open if per-agent detail is ever needed back at
   `info` — not done here, this was the cheap fix.
3. **Partially fixed: `SessionSnapshotter`'s `SaveSession` writes are now
   chunked-batched, but it's still unconditional every tick.** Round-trip
   count is real progress — `defaultBatchSize` (500, `write.go`) agents per
   `pgx.Batch` send instead of one round trip each, when the store supports
   `BatchStateStore` (`PostgresStore` does; a store that doesn't, e.g. a
   test fake, falls back to the original one-row-per-agent path unchanged).
   What's *not* fixed: it still writes every registered agent's session
   state every `sessionSnapshotInterval` regardless of whether anything
   changed (`session_snapshot.go`'s own doc comment). Batching cuts
   round-trip overhead, not total write volume — at 1M agents Postgres
   still needs to absorb 1M row-writes every 5s, batched or not. Real
   dirty-tracked cadence for session data (accepting the staleness
   tradeoff Agent state schema already discusses) is separate, undecided
   future work.
4. **Fixed: `Flusher`'s `SoftDeleteAgent` and `agent_connections`'s
   `UpsertAgentConnection` are chunked-batched, same mechanism as #3.**
   `SaveAgent` itself is deliberately *not* batched — it's not a single
   statement, it's a whole per-agent transaction (the `agents` row plus a
   variable-count rewrite of `agent_effective_config` rows), and folding
   that into a shared `pgx.Batch` across agents would need a bigger
   restructure of how effective_config is stored, not just a batching
   pass. It stays one round trip per dirty agent, same as before this fix.
   A stuck row inside one chunk's batch delays reading the rest of that
   chunk's results (`pgx.Batch` results are read in send order) but no
   longer any other chunk's — a smaller blast radius than before, not the
   same full per-agent independence `runConcurrent` gives `SaveAgent`'s
   own path. Proven with `TestFlusherBatchChunkErrorDoesNotStopRestOfChunk`
   /`TestSessionSnapshotterBatchChunkErrorDoesNotStopRestOfChunk`: one bad
   row's error, read from its batch result, doesn't stop the rest of its
   chunk from landing.
5. **Fixed: `GET /api/agents` no longer sorts the entire matched set
   before paginating.** Was O(N log N) per request regardless of how much
   of it was actually returned (`limit` capped at `maxLimit`). Now
   `api.pageByInstanceUID` (`internal/api/paginate.go`): a bounded max-heap
   keeps the `end` (`offset+limit`) smallest elements seen in one O(N log
   end) pass, only those get sorted, falling back to a plain full sort when
   `end` isn't meaningfully smaller than the matched set anyway. Same
   output for every input — verified with 50 randomized trials against a
   reference full-sort-then-slice implementation
   (`TestPageByInstanceUIDMatchesFullSort`), not just hand-picked cases.
6. **No splay for the case that actually matters, and grex structurally
   cannot add it alone.** Checked `opamp-go` v0.23.0 directly
   (`client/wsclient.go:291-334`, same shape in
   `client/internal/httpsender.go:229-238`; the Supervisor depends on the
   same version, so it's identical there too) rather than assuming:
     - `ensureConnected`'s retry loop uses `cenkalti/backoff/v4`'s
       exponential backoff with `MaxElapsedTime = 0` (retries forever) and
       default `RandomizationFactor: 0.5` (±50% jitter) — but only from the
       *second* attempt onward. `interval := time.Duration(0)` means the
       first connect/reconnect attempt in every cycle fires immediately,
       no delay, no jitter, and `runUntilStopped` resets `interval` back to
       0 on every new cycle. So jitter only helps while the server or
       network is still down and clients are repeatedly failing to get in
       — genuinely useful for that case, nothing to build there.
     - It does **not** help the more common shape of "mass restart": grex
       (or the network) comes back up, and every client's *first* reconnect
       attempt fires at the same instant with zero splay, because attempt
       #1 never goes through the backoff timer at all. That's a real
       thundering-herd moment upstream doesn't address.
     - Steady-state check-in sync (a mass rolling deploy connects the whole
       fleet at once, then every agent's heartbeat ticks in lockstep
       forever after) is a third, separate case, also uncovered: once
       connected, the ongoing report interval is not something backoff
       touches at all.
   All three converge on the same lock: OpAMP is agent-initiated push — the
   agent decides when it sends `AgentToServer`, grex only ever receives —
   so any of the three hits `Registry.Report`'s single lock (#1) at the
   same instant across the fleet, worst case exactly when a `Sweep` might
   also be mid-traversal. Grex has no mechanism to move an agent's timer in
   1.0: that needs remote config push (`AcceptsRemoteConfig`), an explicit
   non-goal requiring the Supervisor (see Post-1.0 roadmap: why the
   Supervisor matters). Fix direction: once config-push exists (tracked
   there, not a new mechanism), add per-agent jitter to the pushed
   `report_interval` as
       one of its config knobs. Until then, this is a deployment-layer
       concern, not grex's code — rolling-update pacing
       (`maxUnavailable`/PDB) or a random startup delay in the
       collector/Supervisor's own entrypoint before its first report.

Not yet a gap, checked and ruled out: `MergeAgents`
(`internal/api/filter.go:223`) uses a map for the overlap check, O(N+M) not
O(N·M). `Registry.List()` deep-copies each agent per call: `snapshot()`
(`internal/fleet/registry.go`) `maps.Clone`s the four per-agent maps
(`Identifying`, `NonIdentifying`, `EffectiveConfig`, `Packages`) and copies
two slices for every agent returned. Real, O(N)-per-call GC pressure — not a
correctness or throughput failure on its own, but every caller that only
needs aggregates (the metrics `FleetCollector` counting connected/healthy at
scrape time) pays a full-fleet clone to produce a handful of numbers. A
non-cloning aggregate walk over the shards is the fix, tracked as its own
scaling item, not done here.

## Open questions

- **Jobs execution engine** (post-1.0, not yet implemented): River for
  per-agent dispatch, grex-owned `jobs`/`job_targets` tables for the
  parent/rollup shape River doesn't provide in OSS, restart as the first
  action, plan/execute split with a 5-minute cancellable arm delay. See
  Jobs: schema and execution. Requires the Supervisor's stable
  `instance_uid` fleet-wide (see Hard requirement: Supervisor adoption) —
  bare-extension agents are excluded from job targets, not degraded-for.
  Still open: restart's reconnect timeout and backoff cap defaults (both
  configurable per job via `action_config`, but no default chosen), and
  the viewer-redaction mechanism for action types with sensitive
  `action_config`/results (rough plan only, see Authorization in Jobs:
  schema and execution).
- **Declarative/policy layer** (post-1.0, future idea, partly designed):
  jobs above are edge-triggered ("do X now to these agents"). The *sync*
  half of "where does desired config come from" is now designed (see
  Config source of truth: sync and apply) — `config_sources`, populated
  only by `grex config sync` from Helm/git, applied only by a separate
  human-triggered `grex jobs apply`. What's still open is the
  level-triggered complement — "whenever an agent matching some filter
  checks in out of compliance with its `config_sources` row, apply it
  automatically" — a different primitive: continuous, no single dispatch
  moment, no natural "done." Cheap to build on top of what jobs/compliance
  already need, since the trigger point is the same per-check-in code path
  (`internal/opamp`'s handler → `fleet.Registry`) and the condition it
  evaluates is the same compliance predicate a job's success check and
  `GET /api/jobs/{id}/compliance` already use — just evaluated per check-in
  instead of on-demand, auto-creating a job for a non-compliant agent
  instead of a human doing it. Not free: fires automatically and
  repeatedly with no human in the loop, so it needs its own safety story
  (rate limiting, a circuit breaker after N failures, an audit trail)
  rather than inheriting jobs' 5-minute-arm-and-cancel mechanism, which
  only makes sense for a human-initiated action. Not scoped or scheduled;
  parked here as a named idea.
