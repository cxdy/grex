# Contributing

## Before you start

- Read [Architecture](architecture.md) and the living
  [design SPEC](../spec/design.md)
- Search existing issues/PRs on
  [github.com/dennisme/grex](https://github.com/dennisme/grex)

## One-time setup

Requires Go 1.26+, [golangci-lint](https://golangci-lint.run/), and
[pre-commit](https://pre-commit.com/). Optional: [mise](https://mise.jdx.dev/)
to install versions from `.tool-versions`.

```sh
make init          # mise install (if present) + pre-commit install
# or manually:
pre-commit install
```

Hooks run on commit (Go build/tidy/test/fmt, golangci-lint, markdownlint,
commitizen message check). Branch-level conventional-commit validation also
runs in CI via the `commitizen-branch` hook.

## Development loop

```sh
make test
make lint
make markdownlint
# optional:
make compose-up && deploy/compose/smoke.sh && make compose-down
```

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) with types
defined in `.cz.toml`:

`feat` · `fix` · `doc` · `perf` · `ref` · `test` · `chore` · `ci` · `revert`

Examples: `feat(ui): add agent attribute chips`, `fix: close idle OpAMP
connections`. Optional helper: `cz commit`.

## Pull requests

- Keep changes focused; match existing style in the package you touch
- Include tests for behavior changes
- Update docs when you change user-facing behavior, config, API, or metrics
- Call out SPEC drift explicitly if implementation intentionally differs

CI on every PR:

| Workflow | What it runs |
|----------|----------------|
| `golang-lint` | `golangci-lint` |
| `golang-tests` | `govulncheck`, `make build`, `make test` (race), coverage XML artifact |
| `coverage-comment` | PR coverage report comment (via `workflow_run`; works for fork PRs) |
| `ci` | `docker compose build` |
| `conventional-commit-check` | commitizen branch message validation |
| `markdownlint` | markdownlint-cli2 on `**/*.md` (path-filtered) |
| `docs` | MkDocs strict build + Helm chart package into `site/charts/`; deploy Pages from `main` (path-filtered) |
| `helm` | `helm lint` / `helm template`, plus kind e2e install via `deploy/charts/smoke.sh` (path-filtered) |

## License

Contributions fall under the repository **Apache-2.0** license.
