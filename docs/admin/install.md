# Install

grex ships as a **single Go binary**, a **container image**, and a
**Helm chart** for Kubernetes. There is no external database to provision
in 1.0: fleet state is in memory.

| Method | Best for | Doc |
|--------|----------|-----|
| Binary / `go run` | Local development | [Getting started](../getting-started/index.md) |
| Docker | Single-node smoke tests | below |
| Docker Compose | Multi-collector lab | [Compose stack](../getting-started/compose-stack.md) |
| **Helm** | Kubernetes (production shape) | [Deploy with Helm](helm.md) |

## Helm (Kubernetes)

```sh
helm repo add grex https://dennisme.github.io/grex/charts/
helm repo update
helm install grex grex/grex --namespace grex --create-namespace
```

The chart repository is hosted on the project GitHub Pages site under
`/charts/` (alongside docs at `/` and the static demo at `/demo/`). Full
install guide: [Deploy with Helm](helm.md). Values reference:
[Helm chart](../reference/helm-chart.md).

Until GHCR release images exist, build and set `image.repository` /
`image.tag` yourself (see the Helm guide).

## From source

Requires Go 1.26+ and [just](https://github.com/casey/just) (or use the
plain `go build` form below).

```sh
git clone https://github.com/dennisme/grex.git
cd grex
just build
# or:
go build -o grex ./cmd/grex
./grex -config /path/to/config.yaml
```

Version metadata for `grex_build_info` and the status page comes from
`-ldflags` (see the `justfile` `ldflags` and the `Dockerfile`).

## Docker

The repository `Dockerfile` multi-stage builds a static binary into
`alpine` and runs as a non-root `grex` user.

```sh
docker build -t grex:local .
docker run --rm -p 8080:8080 -p 9090:9090 -p 4320:4320 \
  -v "$PWD/config.yaml:/etc/grex/config.yaml:ro" \
  grex:local -config /etc/grex/config.yaml
```

Mount TLS material read-only when using `tls.cert_file` / `key_file` /
`client_ca_file`.

Release images (when published via GoReleaser / GHCR per the design) follow
semver tags; until then, build from source, Compose, or load a local image
into your cluster for Helm.

## Docker Compose (development)

```sh
just compose-up
just compose-down
```

See [Compose stack](../getting-started/compose-stack.md). This is the
supported local multi-collector environment and functional-test harness.
For Kubernetes production shape, use the [Helm chart](helm.md) instead
(Compose is not a production topology template).

## Configuration file

grex requires a config path:

```text
grex -config config.yaml
```

Default path if you omit the flag in code is `config.yaml` relative to the
process working directory. Copy `config.example.yaml` as a starting point.
The Helm chart renders an equivalent ConfigMap from `values.yaml`
(`config.*` / `listeners.*` / `tls.*`). Full field list:
[Configuration](configuration.md).

## Runtime requirements

- Writable nothing required for core operation (state is memory-only)
- Read access to config and TLS files
- Outbound network only if you add something external later (grex itself
  does not call cloud APIs today)
- Collectors must be able to reach the OpAMP listen address
