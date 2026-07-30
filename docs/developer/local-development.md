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
- Compose runs two grex instances (`grex`, `grex-2`) behind an Envoy
  least-connections tier (`deploy/compose/envoy.yaml`), so `opamp-gateway`'s
  upstream connections actually spread across replicas — see
  [Scaling with gateways](../admin/scaling-with-gateways.md#load-balancing-across-grex-replicas).
  `grex-2` is reachable directly on host ports `8081`/`9092`/`4321` (same
  layout as `grex`'s `8080`/`9090`/`4320`) for `gxcurl`/debugging.
- Want to poke around the UI or `/riverui` in a browser without installing
  a client cert? `grex-browser` (`deploy/compose/grex-browser.yaml`) has
  no `ui_tls` at all, plain HTTP: <http://127.0.0.1:8082>. Not part of the
  Envoy/gateway pool, but shares the same Postgres, so it shows the same
  fleet via the cross-replica DB merge.
- If certs generated before this topology existed are still on disk
  (`deploy/compose/certs/`), `gen-certs.sh`'s per-file idempotency means the
  server cert won't pick up the new `grex-2`/`envoy` SANs automatically —
  delete `deploy/compose/certs/server*.pem` (or the whole directory) and
  rerun `gen-certs` if TLS handshakes to `grex-2` or through `envoy` fail
  hostname verification.
- Occasionally `smoke.sh` reports both gateway-relayed agents landing on
  one grex replica instead of splitting across both — this is expected,
  not a regression. `opamp-gateway` opens its 2 upstream connections
  microseconds apart at startup, tighter than a TCP handshake, so Envoy's
  `LEAST_REQUEST` active-connection count sometimes has no signal yet for
  the second pick. `smoke.sh` reports this, it doesn't fail on it; see the
  comments in `deploy/compose/envoy.yaml` and
  `deploy/compose/opamp-gateway.yaml` for why this is left as-is rather
  than worked around (switching to `ROUND_ROBIN` or raising `connections`
  would either give up the production LB policy's actual justification or
  just mask the race behind more trials).

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
