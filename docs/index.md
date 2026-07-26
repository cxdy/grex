# grex

**grex** is an [OpAMP](https://opentelemetry.io/docs/specs/opamp/) control
plane for [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/)
fleets. It implements the server side of the OpAMP specification and gives
operators a **read-only** view of fleet health: connected collectors, identity,
health, and effective configuration.

```text
  otelcol agents / gateways
  (OpAMP over WS, optional mTLS)
           │
           ▼
  OpAMP gateway collector(s)  ──►  grex  ──►  browser (web UI)
  (optional multiplexing)            │
           │                         ├── JSON read API
  direct agents (optional)  ─────────┤
                                     └── Prometheus /metrics + /metrics/fleet
```

## Who these docs are for

| Audience | Start here | Goal |
|----------|------------|------|
| **Operators (UI users)** | [User guide](user/index.md) | Understand fleet status in the web UI |
| **Administrators** | [Admin guide](admin/index.md) | Install, configure, secure, and scrape grex |
| **Developers** | [Developer guide](developer/index.md) | Build, test, and extend grex (primary focus of this site) |
| **Everyone** | [Getting started](getting-started/index.md) | Run grex and see a fleet in minutes |

Deep coverage of **metrics, logs, and traces** lives under
[Observability](observability/index.md).

## What grex does (and does not) do in 1.0

**Does:**

- Accept OpAMP connections from collectors (WebSocket and plain HTTP)
- Support OpAMP gateway multiplexing (`com.bindplane.opamp-gateway`)
- Maintain an in-memory fleet registry (identity, health, effective config, …)
- Serve a read-only web UI and JSON API
- Expose Prometheus metrics on a dedicated telemetry listener
- Terminate TLS and require client mTLS on the **OpAMP** listener when configured

**Does not (yet):**

- Push remote configuration, restarts, or package upgrades from the UI
- Persist fleet state across restarts (agents re-report on reconnect)
- Multi-tenancy (one fleet per grex instance)
- UI/API authentication (mTLS and OIDC are [planned](https://github.com/dennisme/grex/issues/11); the UI listener is open today)
- OTLP export of grex’s own telemetry (Prometheus scrape only)

The living product plan is under [SPEC](spec/index.md)
([design](spec/design.md)). That document **changes frequently**.

## Quick start

=== "Demo UI (static)"

    No install required — open the **[static fleet demo](demo/)** on this
    site (sample data generated in your browser).

=== "Compose (full stack)"

    ```sh
    just compose-up
    deploy/compose/smoke.sh
    # UI: http://127.0.0.1:8080
    ```

    See [Compose stack](getting-started/compose-stack.md).

=== "Local binary"

    ```sh
    cp config.example.yaml config.yaml
    go run ./cmd/grex -config config.yaml
    # UI :8080 · telemetry :9090 · OpAMP :4320
    ```

    See [Getting started](getting-started/index.md).

=== "Helm (Kubernetes)"

    ```sh
    helm repo add grex https://dennisme.github.io/grex/charts/
    helm install grex grex/grex -n grex --create-namespace
    ```

    See [Deploy with Helm](admin/helm.md). Chart values:
    [reference](reference/helm-chart.md).

## Documentation map

- **[Demo](demo/)** — static sample fleet UI (no grex process)
- **[Getting started](getting-started/index.md)** — tutorials to a first success
- **[User](user/index.md)** — fleet UI for operators
- **[Admin](admin/index.md)** — deploy (binary, Compose, Helm), config, TLS, lifecycle
- **[Developer](developer/index.md)** — architecture, packages, API, UI, tests
- **[Observability](observability/index.md)** — metrics catalog, scrape layout, logs, traces
- **[Reference](reference/cli.md)** — CLI, HTTP endpoints, Helm values
- **[SPEC](spec/index.md)** — living design documents

## Why "grex"?

!!! info "Latin: *grex, gregis* (n.) — flock, herd, group"
    The same root gives English *gregarious* and *congregate*.
    Fitting for a control plane that manages a flock of OpenTelemetry
    Collectors as one fleet.

## License

Apache-2.0. Source: [github.com/dennisme/grex](https://github.com/dennisme/grex).
