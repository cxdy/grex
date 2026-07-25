# grex

<p align="center">
  <img src="logo.png" alt="Project Logo" width="200">
</p>

grex is an OpAMP control plane for OpenTelemetry Collector fleets. It
implements the server side of the
[OpAMP specification](https://opentelemetry.io/docs/specs/opamp/) and gives
operators a read-only view of fleet health: connected collectors, their
identity, health, and effective configuration.

**Documentation:** [https://dennisme.github.io/grex/](https://dennisme.github.io/grex/)

## Demo

Try the fleet UI without installing anything — a static demo with sample
collectors and gateways runs entirely in the browser (100–300 synthetic
agents, filters, agent detail, status):

**[Open the demo →](https://dennisme.github.io/grex/demo/)**

This is sample data only, not a live grex process. For a real fleet locally,
use the compose stack below.

The living 1.0 design (changes frequently) is under
[docs/spec/design.md](docs/spec/design.md).

## Development

Requires Go 1.26+ and [golangci-lint](https://golangci-lint.run/). For
local git hooks, also install [pre-commit](https://pre-commit.com/)
(versions in `.tool-versions` if you use [mise](https://mise.jdx.dev/)).

```sh
make init          # optional: mise + pre-commit hooks
make build         # go build ./...
make test          # go test -race ./...
make lint          # golangci-lint run
make markdownlint  # markdownlint-cli2 on repo markdown
```

Run the server with the example config:

```sh
cp config.example.yaml config.yaml
go run ./cmd/grex -config config.yaml
```

The telemetry listener (default `:9090`) serves `/healthz`, `/readyz`, and
two Prometheus endpoints: `/metrics` (server health) and `/metrics/fleet`
(fleet series), separated so they can be scraped as independent jobs with
independent limits.

The UI listener (default `:8080`) serves the fleet web UI (`/`,
`/agents/{id}`, `/status`) and the JSON read API (`/api/agents`,
`/api/agents/{id}`, `/api/status`, `/api/attributes`,
`/api/attributes/values`). The UI auto-refreshes via htmx; interval
is `ui.poll_interval` (default `5s`).

## Compose dev stack

Requires Docker with Compose v2.

```sh
make compose-up      # build and start the full stack
deploy/compose/smoke.sh  # assert everything is healthy
make compose-down    # tear down, removing volumes
```

The stack runs grex (built from local source, OpAMP listener terminating TLS
with generated dev certificates), two OpenTelemetry Collector agents and one
gateway connecting to grex over mTLS, Dex as the dev OIDC issuer, and
Prometheus scraping both grex metrics endpoints as separate jobs. Ports on
localhost: grex UI `8080`, grex telemetry `9090`, grex OpAMP `4320`, Dex
`5556`, Prometheus `9091`. Dev certificates are minted once into `deploy/compose/certs/` by a
one-shot container; delete the directory to regenerate.

## Documentation site (local)

```sh
pip install -r requirements-docs.txt
make docs          # sync demo UI assets + mkdocs build --strict
# or interactive:
make demo-static && mkdocs serve
```

Local preview includes the [static demo](https://dennisme.github.io/grex/demo/)
at `/demo/`. GitHub Pages is published from `main` via
`.github/workflows/docs.yml`.

## License

Apache-2.0, see [LICENSE](LICENSE).
