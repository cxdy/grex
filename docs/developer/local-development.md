# Local development

## Prerequisites

- Go version from `go.mod` (1.26+)
- [golangci-lint](https://golangci-lint.run/) for `make lint`
- Docker + Compose v2 for the full stack and smoke script

## Common commands

```sh
make build    # go build ./... with version ldflags
make test     # go test -race ./...
make lint     # golangci-lint run

cp config.example.yaml config.yaml
go run ./cmd/grex -config config.yaml
```

```sh
make compose-up
deploy/compose/smoke.sh
make compose-down
```

## Suggested workflow

1. Create a branch from `main`
2. Prefer **TDD**: package tests under `internal/...`
3. Run `make test` and `make lint` before pushing
4. For OpAMP/TLS/gateway behavior, extend unit tests first; use compose smoke
   for multi-container confidence
5. If you change metrics names/labels, update
   [observability docs](../observability/metrics.md) and any golden compares
   in `internal/metrics`

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

Strict build (as in CI):

```sh
mkdocs build --strict
```

## IDE notes

- Module path: `github.com/dennisme/grex`
- Go 1.22+ `ServeMux` method patterns (`GET /api/agents/{id}`) are required
- Do not register pprof on `http.DefaultServeMux`; telemetry uses grex’s mux
