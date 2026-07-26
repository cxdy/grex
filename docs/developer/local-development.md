# Local development

## Prerequisites

- Go version from `go.mod` (1.26+)
- [just](https://github.com/casey/just) for the recipe runner (`justfile`)
- [golangci-lint](https://golangci-lint.run/) for `just lint`
- Docker + Compose v2 for the full stack and smoke script

## Common commands

```sh
just build    # go build ./... with version ldflags
just test     # go test -race ./...
just lint     # golangci-lint run

cp config.example.yaml config.yaml
go run ./cmd/grex -config config.yaml
```

```sh
just compose-up
deploy/compose/smoke.sh
just compose-down
```

Helm chart (requires `helm`; e2e also needs Docker + kind or k3d):

```sh
just helm-lint                 # lint + template render
just helm-package              # package into dist/charts/
just helm-e2e-kind             # build image, kind cluster, install, smoke + helm test
just helm-e2e-k3d              # same with k3d (k3s)
# or install into an existing cluster:
helm install grex ./deploy/charts/grex -n grex --create-namespace
```

See [Deploy with Helm](../admin/helm.md#local-cluster-smoke-test-kind--k3d)
and `deploy/charts/smoke.sh --help`.

## Suggested workflow

1. Create a branch from `main`
2. Prefer **TDD**: package tests under `internal/...`
3. Run `just test` and `just lint` before pushing
4. For OpAMP/TLS/gateway behavior, extend unit tests first; use compose smoke
   for multi-container confidence
5. If you change metrics names/labels, update
   [observability docs](../observability/metrics.md) and any golden compares
   in `internal/metrics`
6. If you change chart templates or values, run `just helm-lint` and update
   [admin/helm](../admin/helm.md) / [reference/helm-chart](../reference/helm-chart.md)

## Config for local runs

- Plaintext OpAMP is fine for unit tests (`internal/testcert` when TLS is needed)
- Compose enables mTLS and `log.level: debug` / `json` for grex
- UI poll interval defaults to 5s; lower it only if you are UI-polishing

## Documentation site

```sh
pip install -r requirements-docs.txt
cp logo.png docs/assets/logo.png   # workflow does this in CI
mkdocs serve
# http://127.0.0.1:8000
```

Strict build (as in CI; also packages the Helm chart into `site/charts/` when
`helm` is on `PATH`):

```sh
just docs
# or: mkdocs build --strict
```

## IDE notes

- Module path: `github.com/dennisme/grex`
- Go 1.22+ `ServeMux` method patterns (`GET /api/agents/{id}`) are required
- Do not register pprof on `http.DefaultServeMux`; telemetry uses grex’s mux
