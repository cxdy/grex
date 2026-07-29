# Developer guide

grex is a Go module (`github.com/dennisme/grex`) that implements an OpAMP
**server**, an in-memory **fleet registry**, a **JSON read API**, an embedded
**web UI**, and **Prometheus** instrumentation.

This section is the primary technical documentation for people changing grex.

## Start here

| Goal | Page |
|------|------|
| See how the pieces fit | [Architecture](architecture.md) |
| Build and run tests | [Local development](local-development.md) |
| Find the right package | [Package map](package-map.md) |
| Understand OpAMP + gateways | [OpAMP and gateway](opamp-and-gateway.md) |
| Understand registry liveness | [Fleet state](fleet-state.md) |
| Call or extend the API | [Read API](read-api.md) |
| Change HTML/CSS/htmx | [Web UI](ui.md) |
| Write tests | [Testing](testing.md) |
| Add a metric / field / page | [Extending grex](extending.md) |
| Contribute a PR | [Contributing](contributing.md) |
| Cut a versioned release | [Releasing](releasing.md) |

## Repository layout

```text
cmd/grex/           # main: wire config, metrics, fleet, opamp, api, ui, server
internal/
  api/              # JSON read API
  buildinfo/        # version/commit/date via ldflags
  config/           # YAML + GREX_* loading
  fleet/            # in-memory registry and views
  metrics/          # Prometheus registries, fleet collector, HTTP metrics
  opamp/            # opamp-go bridge + gateway capability
  server/           # three listeners, TLS, probes, pprof
  testcert/         # helpers for TLS tests
  ui/               # html/template + htmx + embedded static assets
deploy/compose/     # local stack configs, certs script, smoke test
deploy/charts/grex/ # Helm chart (K8s production shape; Pages /charts/ repo)
docs/               # this MkDocs site
```

## Design vs code

Product intent lives under [SPEC](../spec/index.md). The design
**changes often**. When implementing a feature, reconcile:

1. Living design (`docs/spec/design.md`)
2. Existing packages and tests
3. Open issues (e.g. [auth #11](https://github.com/dennisme/grex/issues/11))

## Tooling

- Go **1.26+**
- `just build` / `just test` / `just lint` (`golangci-lint`)
- Docker Compose for integration-style smoke tests

## Prior art

- [opampcommander](https://github.com/minuk-dev/opampcommander) — another
  OpAMP control plane, similar goals (fleet view, remote config, RBAC). Uses
  MongoDB plus optional Kafka event bus for multi-node coordination, versus
  grex's Postgres (planned) and a `kubectl`-style CLI (`opampctl`), versus
  grex's config-file-only binary today.
