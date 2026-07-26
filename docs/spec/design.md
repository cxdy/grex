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
  today: an `identities` table (SPIFFE ID or OIDC group, one row each) and a
  `role_bindings` table (identity → role). Simplest possible migration off
  the static config, cheapest to query (`what role does this caller have`
  is one indexed lookup). Doesn't express "admin on fleet A, viewer on
  fleet B" without bolting on a scope column later.
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

Leaning flat for the first pass, but with nullable `tenant_id`/scope columns
present from the first migration even before anything populates them:
retrofitting a scope column onto a live permissions table users already
depend on is the expensive path, adding an unused nullable column up front
is not.

#### Agent sharding scheme

`replicaCount` is pinned to 1 in the Helm chart today because fleet state is
one process's memory. Once state lives in a database, the next question is
whether — and how — agent state gets partitioned, either across database
partitions or across multiple grex processes.

- **Hash-partition by `instance_uid`** (consistent hashing across shards).
  Even load distribution, no coordination needed to decide an agent's shard
  since it's a pure function of `instance_uid`. The blocker: the OpAMP
  gateway's `connect` message carries no `instance_uid` (see OpAMP server
  above), so a gateway-relayed agent can't be routed to the correct shard
  at connect time without either an upstream protocol change or an extra
  proxy hop between shards. Direct agents don't have this problem; relayed
  ones are the common case.
- **DB-native hash partitioning, single app tier.** grex itself stays a
  single logical writer (or a stateless pool of processes sharing one
  database), and Postgres declarative partitioning
  (`PARTITION BY HASH (instance_uid)`) does the scaling at the storage
  layer. The app code and the gateway connect flow don't change at all.
  Ceiling is wherever the database (or the app tier's own connection/TLS
  handling) saturates first, not a design limit.
- **Shard by gateway or tenant** (coarse-grained: a whole upstream gateway
  connection, or a whole tenant once multi-tenancy exists, is pinned to one
  shard). Sidesteps the missing-`instance_uid`-on-connect problem entirely,
  since the sharding decision happens at the connection/tenant level, which
  grex already sees today. Risk: an outsized single gateway or tenant
  becomes a hot shard that this scheme can't split further.

Leaning DB-native partitioning first, since it needs no gateway protocol
change and extends the current architecture directly, with tenant/gateway
sharding as the natural next step once multi-tenancy exists (tenant
boundary and shard boundary become the same boundary). Per-agent hash
sharding at the app tier is the one to defer hardest: it's real complexity
(routing relayed agents without an `instance_uid` on connect) that should
only be built once benchmarking work shows the DB-only approach actually
runs out of headroom, not before.

#### Jobs: schema and execution

Every 1.0 non-goal that mutates something (remote config push, restart,
package upgrade) needs the same shape underneath: a user (via API first,
UI second) picks a target set of agents by attribute filter — the same
filter language `GET /api/agents` already uses — and an action to perform,
and grex carries that out per matched agent and reports back per-agent
outcome. This assumes the Supervisor work above has already landed as the
standard client by the time mutation ships, so `instance_uid` is stable
across an agent's restarts; job targets don't need to account for an
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
- **Dispatch needs the target's live connection**, which only works
  cleanly with a single app tier holding all OpAMP connections — which is
  exactly what "DB-native partitioning" above keeps true. If per-agent
  app-tier sharding is ever built instead, job dispatch would need to route
  to whichever replica holds that agent's socket, not just query the
  database; another reason to defer that sharding option.

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
   `/metrics/fleet` at heartbeat granularity with its own `sample_limit`.
7. **Dev certificate generation** — an init step (script or one-shot container)
   that mints a local CA, server certs for grex and the OpAMP gateway, and
   client certs for the collectors so mTLS is exercised on both hops in dev,
   not just prod.

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
- **Helm chart:** source under `deploy/charts/grex`. Packaged and published
  as a Helm repository on GitHub Pages at
  `https://dennisme.github.io/grex/charts/` (path prefix on the same Pages
  site as MkDocs docs and the static demo — single deploy workflow, no second
  Pages source). The docs workflow packages the chart into `site/charts/`
  after `mkdocs build` so `/`, `/demo/`, and `/charts/` share one artifact.
  Chart `version` / `appVersion` are bumped with grex releases once GoReleaser
  lands; until then operators build/load images and set `image.tag` explicitly.
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
9. **Local dev** — compose stack with collectors and dev certs (built;
   OpAMP gateway insertion lands with milestone 2). One agent runs under an
   OpAMP Supervisor instead of the bare `opamp` extension, exercising both
   client-side models and giving one agent a stable, volume-persisted
   `instance_uid`.
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
- Stale eviction: an agent that misses N consecutive check-ins is evicted and
  disappears from the UI (no tombstones). Evictions are counted in
  `grex_agents_evicted_total`.
- Scaling topology: observIQ `opampgateway` extension collectors sit between
  agents and grex, multiplexing agent sessions over few upstream connections.
  grex implements the gateway's custom connect capability and keeps direct
  connections working.

## Open questions

- **Permission table schema** (post-1.0): flat identity-to-role vs. scoped
  RBAC vs. ReBAC. See Post-1.0 roadmap: state database and sharding.
  Leaning flat-with-nullable-scope-columns; not decided.
- **Agent sharding scheme** (post-1.0): per-agent hash sharding vs.
  DB-native partitioning vs. gateway/tenant sharding. See the same section.
  Leaning DB-native partitioning first; not decided, and blocked in part on
  benchmarking data that doesn't exist yet.
- **Jobs execution engine** (post-1.0): River leaning sound for per-agent
  dispatch, on top of grex-owned `jobs`/`job_targets` tables for the
  parent/rollup shape River doesn't provide in OSS. See Jobs: schema and
  execution. Not decided; assumes the Supervisor's stable `instance_uid`
  lands first.
