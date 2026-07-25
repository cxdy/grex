# Testing

## Unit tests

```sh
make test
# equivalent: go test -race ./...
```

Race detector is required in CI and the Makefile.

### Package patterns

| Package | What tests emphasize |
|---------|----------------------|
| `config` | Defaults, env overrides, validation errors |
| `fleet` | Registry lifecycle, eviction, attributes, views |
| `opamp` | Handler behavior, gateway JSON, e2e server |
| `api` | Pagination, filters, status JSON, reserved keys |
| `metrics` | Registry split, fleet collector, golden gathers |
| `server` | Listeners, TLS, metrics path separation, probes |
| `ui` | Routes, filters, rendering smoke |

Prefer table-driven tests. For Prometheus, use
`prometheus/testutil` gather/compare helpers (see `metrics_test.go`).

### TLS

`internal/testcert` builds ephemeral CAs/certs so server TLS tests do not
depend on files under `deploy/compose/certs/`.

## Lint

```sh
make lint
make markdownlint
```

Go lint config: `.golangci.yml` (CI: `golang-lint` workflow). Markdown:
`.markdownlint.yaml` / `.markdownlint-cli2.yaml` (CI: `markdownlint` workflow).
Local hooks via `pre-commit` also run golangci-lint, Go fmt/build/test, and
markdownlint.

## Coverage

```sh
make coverage   # coverage.out, coverage.html, coverage.xml
```

CI posts a Cobertura report on PRs (`golang-tests` workflow) with a 70%
line-coverage floor.

## Compose smoke

```sh
make compose-up
deploy/compose/smoke.sh
make compose-down
```

Smoke is the multi-container honesty check: grex health, metrics, and
collector log signals. CI builds the compose images (`docker compose build`)
on every PR; full smoke may be run locally or extended in CI later.

## What not to do

- Do not mark tests that require Docker as plain `go test` without build tags
  if they would break default developer machines
- Do not hit real external OIDC providers; Dex static users are for future
  auth work
- Do not assert on wall-clock flakiness; prefer fake clocks or generous
  heartbeat settings in unit tests

## Coverage philosophy

Design calls for TDD per package with compose as the end-to-end harness.
When adding a metric or API field, add a test that fails before the
implementation lands.
