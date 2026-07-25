# Contributing

## Before you start

- Read [Architecture](architecture.md) and the living
  [design SPEC](../spec/design.md)
- Search existing issues/PRs on
  [github.com/dennisme/grex](https://github.com/dennisme/grex)

## Development loop

```sh
make test
make lint
# optional:
make compose-up && deploy/compose/smoke.sh && make compose-down
```

## Pull requests

- Keep changes focused; match existing style in the package you touch
- Include tests for behavior changes
- Update docs when you change user-facing behavior, config, API, or metrics
- Call out SPEC drift explicitly if implementation intentionally differs

CI (`.github/workflows/ci.yml`) on every PR:

- `golangci-lint`
- `go test -race ./...`
- `go build ./...`
- `docker compose build`

Docs CI (`.github/workflows/docs.yml`) builds MkDocs on docs-related paths
and deploys to GitHub Pages from `main`.

## License

Contributions fall under the repository **Apache-2.0** license.
