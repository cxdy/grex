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
  or k3d (`make helm-e2e-kind` / `make helm-e2e-k3d`).
- **Versioning:** semver computed with [`svu`](https://github.com/caarlos0/svu)
  from conventional commit history; a release workflow tags `$(svu next)`.
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
6. **Auth: mTLS** — TLS termination plus required client certificates on the
   UI/API listener, reusing the mTLS plumbing built for the OpAMP listener
   in milestone 2. Identity comes from the client cert's SPIFFE ID (URI SAN,
   `spiffe://<trust-domain>/<path>`), not the X.509 subject: grex requires
   exactly one SPIFFE URI SAN per cert and rejects certs that lack one or
   carry a malformed one. Authorization maps SPIFFE ID (or a path prefix of
   it) to role via config, same shape as the `groups`-to-role table OIDC
   uses in milestone 7, so the role table's mechanism does not change when
   OIDC lands, only the identity source feeding it. Ships first: no external
   dependency, and it is real access control for API consumers even before
   OIDC login exists for the browser UI.
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

None.
