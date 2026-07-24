# grex 1.0 Design Spec

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

## Architecture

```
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
2. **UI + read API** — serves the web UI and a JSON API the UI consumes. Guarded
   by OIDC login and roles.
3. **Telemetry endpoint** — Prometheus `/metrics` plus health/readiness probes.

### OpAMP server

- Library: `open-telemetry/opamp-go`. grex implements the server callbacks;
  it does not fork or reimplement the protocol.
- Transports: WebSocket (primary) and plain HTTP polling, both required by the
  spec for interoperability. Gateway-relayed traffic is WebSocket only.
- Gateway support: grex implements the `com.bindplane.opamp-gateway` custom
  capability. When a gateway relays a new agent it sends a `connect` custom
  message carrying the agent's HTTP headers and remote address; grex answers
  with `connectResult` (accept/reject plus HTTP status). In 1.0 grex accepts
  every agent relayed over an authenticated (mTLS) gateway connection and
  stores the forwarded headers and remote address as that agent's connection
  metadata. The extension is alpha; its version is pinned and protocol
  changes are absorbed at upgrade time. Accepted risk: if the extension's
  maturity or its absence from the pinned observIQ distribution becomes a
  blocker, the fallback is forking the extension and fixing it (building it
  into an OCB image).
- Multiplexing: a single gateway connection carries many agents, so fleet
  state and per-agent handling key on `instance_uid`, never on the socket.
  Connection counts and agent counts are tracked as separate things.
- Capabilities accepted from agents in 1.0: status reporting, effective config
  reporting, health reporting, own-telemetry reporting metadata. Offers from the
  server (remote config, packages, connection settings) are not sent.
- Agent identity: `instance_uid` plus reported `AgentDescription` attributes
  (service.name, service.version, host, OS, etc.).
- Required attributes: a configurable list of attribute keys
  (`fleet.required_attributes`) that every agent's `AgentDescription` must
  carry, checked across identifying and non-identifying attributes. When an
  agent reports without one, grex logs a warning naming the agent and the
  missing keys and increments `grex_agent_missing_attributes_total`. The agent
  is still accepted and displayed; enforcement is observe-only in 1.0.
- Check-in tracking: the server expects each agent to check in (any
  AgentToServer message, heartbeat or otherwise) at least once per
  `heartbeat_interval`. An agent that misses `stale_missed_heartbeats`
  consecutive intervals is stale: evicted from fleet state, removed from the
  UI, and counted in `grex_agents_evicted_total`. Until it crosses that
  threshold, a disconnected agent stays visible with its last-seen timestamp.
  A stale agent that returns re-registers as a fresh entry.

### Fleet state

- In-memory registry keyed by `instance_uid`, holding the latest
  `AgentDescription`, health, effective config, capabilities, connection
  metadata (remote address, TLS client identity), and timestamps.
- Concurrency-safe; the OpAMP callbacks write, the API reads.
- No persistence in 1.0 (see non-goals). State survives agent reconnects
  because agents re-report on connect.

### Web UI

- Purpose: visualize the fleet. Read-only.
- Pages:
  - **Fleet overview** — table of agents: identity, type (agent/gateway),
    version, health, last seen, connection transport. Shows connected and
    not-yet-stale disconnected agents; evicted agents do not appear.
  - **Agent detail** — full attribute set, health history for the session,
    effective configuration (rendered YAML), capabilities, connection info.
  - **Server status** — grex version, uptime, connected/stale counts, summary
    of fleet health.
- Served by the grex binary (embedded assets via `go:embed`) so a single
  artifact deploys everything.
- Stack: server-rendered `html/template` with [htmx](https://htmx.org/) for
  polling refresh and partial page swaps. No Node toolchain; the only static
  asset beyond templates is the htmx script, vendored and embedded. If the
  template count grows past what stdlib templates handle cleanly, migrating to
  [templ](https://github.com/a-h/templ) is the designated escape hatch.

### AuthN/AuthZ

- **Collectors (OpAMP endpoint):** TLS termination by grex. Optional required
  mTLS: client certificate verified against a configured CA bundle. Two hops
  in the gateway topology: agents present client certificates to the OpAMP
  gateway's listener, and the gateway presents its own client certificate to
  grex. grex records the gateway's certificate identity plus the forwarded
  per-agent remote address; direct agents get their own certificate identity
  recorded.
- **Users (UI/API):** OIDC Authorization Code flow against [Dex](https://dexidp.io/).
  Dex federates the upstream identity provider; the first connector is GitHub,
  tied to an organization. Dex injects org/team membership as `groups` claims
  in the `id_token`, so grex is a plain OIDC client with claims-based role
  mapping and no provider-specific code. Any other identity provider Dex
  supports works without changes to grex.
- **Roles:** simple and static in 1.0.
  - `viewer` — can see everything the UI shows.
  - `admin` — same as viewer in 1.0 (mutation comes later); the role exists now
    so tokens/sessions carry it and 1.1+ can gate mutations on it.
  - Role assignment via configuration: map of `groups` claim values (e.g.
    GitHub `org:team`) to role, plus a default role for authenticated users
    (may be "none" to deny by default). Users with no mapped group get no
    role and no access.
- **Deployment note:** Dex is a required companion service in production. It
  ships in the compose stack for dev and is documented as a prerequisite for
  deployment.
- Sessions: encrypted cookie sessions; no server-side session store (consistent
  with no-database constraint).

### Metrics

grex exposes Prometheus metrics on the telemetry endpoint. Two groups:

**Fleet health** (the point of the product):

- `grex_agents_connected` (gauge, by transport, agent type, and `via`:
  direct or gateway)
- `grex_gateway_connections` (gauge: multiplexed gateway connections open)
- `grex_gateway_connects_total` (counter, by result: accepted/rejected
  `connectResult` answers)
- `grex_agents_disconnected` (gauge: retained agents awaiting return, not yet
  evicted)
- `grex_agents_evicted_total` (counter: agents removed after missing the
  check-in threshold)
- `grex_agent_health` (gauge, by instance_uid: 1 healthy / 0 unhealthy)
- `grex_agent_last_seen_timestamp_seconds` (gauge, by instance_uid)
- `grex_agent_reports_total` (counter, by message type: status, health,
  effective config)
- `grex_agent_missing_attributes_total` (counter, by attribute key: agent
  reports lacking a required AgentDescription attribute)
- `grex_agents_noncompliant` (gauge: agents currently missing at least one
  required attribute)
- `grex_agent_connects_total` / `grex_agent_disconnects_total` (counters)

**Server health:**

- OpAMP message processing counts/errors/latency
- HTTP API request counts/latency/status
- Auth outcomes (login success/failure, mTLS verification failures)
- Go runtime metrics (standard collectors)

Cardinality note: per-instance_uid metrics are capped by a configurable fleet
size limit; above the cap, per-agent series are dropped and only aggregates
remain.

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
   connections to grex with its own client certificate. Image is observIQ's
   collector distribution (pinned); if the pinned distribution does not ship
   the extension, a small OCB-built collector image is the fallback.
3. **otelcol agent × 2** — OpenTelemetry Collector containers running the
   `opamp` extension pointed at the OpAMP gateway, each generating some
   internal telemetry so the fleet view has real data.
4. **otelcol gateway × 1** — a collector configured in OTLP gateway topology,
   receiving from the two agents, its own OpAMP connection also routed through
   the OpAMP gateway. This satisfies "more than one otelcol agent or gateway"
   and demonstrates mixed fleet roles.
5. **Dex** — the OIDC issuer grex authenticates against. In dev it runs with
   static test users (Dex `staticPasswords`) carrying group claims that map to
   both roles, so the full login flow is exercised offline without GitHub
   credentials. Production swaps the connector config to GitHub; grex config
   is unchanged.
6. **Dev certificate generation** — an init step (script or one-shot container)
   that mints a local CA, server certs for grex and the OpAMP gateway, and
   client certs for the collectors so mTLS is exercised on both hops in dev,
   not just prod.

The stack currently runs with agents connected straight to grex; inserting the
OpAMP gateway service happens together with the OpAMP core milestone, since
the gateway's connect handshake needs grex to answer `connectResult`.

## Release engineering

- **CI (GitHub Actions):** on every PR and push to main run `golangci-lint`,
  `go test ./...` with race detector, and a build of the compose stack to keep
  the dev environment honest.
- **Versioning:** semver computed with [`svu`](https://github.com/caarlos0/svu)
  from conventional commit history; a release workflow tags `$(svu next)`.
- **Releases:** [GoReleaser](https://goreleaser.com/) builds:
  - Binaries: linux/darwin, amd64/arm64, checksums, archives attached to the
    GitHub Release.
  - Docker images: multi-arch, pushed to GHCR, tagged with the semver tag and
    `latest`.
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
| — | Remote config push | No, 1.1+ |
| — | Package management | No |
| — | Persistent storage | No |
| — | Multi-tenancy | No |

## Execution plan (milestones)

1. **Skeleton** — repo layout, config loading, logging, CI with lint + tests.
2. **OpAMP core** — opamp-go server wired up, in-memory fleet state, WS + HTTP
   transports, TLS + mTLS, `com.bindplane.opamp-gateway` capability
   (connect/connectResult handling, forwarded connection metadata), compose
   stack amended to route collectors through the OpAMP gateway service.
3. **Telemetry** — Prometheus metrics, health probes.
4. **Read API + UI** — JSON API, embedded UI, the three pages.
5. **Auth** — OIDC client against Dex, claims-based roles, Dex dev config
   with static users.
6. **Local dev** — compose stack with collectors and dev certs (built;
   OpAMP gateway insertion lands with milestone 2).
7. **Release** — GoReleaser, svu, image publishing, first tagged 1.0.

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

None.
