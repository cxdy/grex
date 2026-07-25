# Install

grex ships as a **single Go binary** and as a **container image**. There is
no external database to provision in 1.0: fleet state is in memory.

## From source

Requires Go 1.26+.

```sh
git clone https://github.com/dennisme/grex.git
cd grex
make build
# or:
go build -o grex ./cmd/grex
./grex -config /path/to/config.yaml
```

Version metadata for `grex_build_info` and the status page comes from
`-ldflags` (see the `Makefile` `LDFLAGS` and the `Dockerfile`).

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
semver tags; until then, build from source or Compose.

## Docker Compose (development)

```sh
make compose-up
make compose-down
```

See [Compose stack](../getting-started/compose-stack.md). This is the
supported local multi-collector environment, not a production topology
template (no HA grex, no Kubernetes manifests in-repo yet).

## Configuration file

grex requires a config path:

```text
grex -config config.yaml
```

Default path if you omit the flag in code is `config.yaml` relative to the
process working directory. Copy `config.example.yaml` as a starting point.
Full field list: [Configuration](configuration.md).

## Runtime requirements

- Writable nothing required for core operation (state is memory-only)
- Read access to config and TLS files
- Outbound network only if you add something external later (grex itself
  does not call cloud APIs today)
- Collectors must be able to reach the OpAMP listen address
