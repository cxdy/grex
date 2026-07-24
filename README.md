# grex

<p align="center">
  <img src="logo.png" alt="Project Logo" width="200">
</p>

grex is an OpAMP control plane for OpenTelemetry Collector fleets. It
implements the server side of the
[OpAMP specification](https://opentelemetry.io/docs/specs/opamp/) and gives
operators a read-only view of fleet health: connected collectors, their
identity, health, and effective configuration. See
[docs/design.md](docs/design.md) for the 1.0 design.

## Development

Requires Go 1.26+ and [golangci-lint](https://golangci-lint.run/).

```sh
make build   # go build ./...
make test    # go test -race ./...
make lint    # golangci-lint run
```

Run the server with the example config:

```sh
cp config.example.yaml config.yaml
go run ./cmd/grex -config config.yaml
```

The telemetry listener (default `:9090`) serves `/healthz`, `/readyz`, and
Prometheus `/metrics`.

## Compose dev stack

Requires Docker with Compose v2.

```sh
make compose-up      # build and start the full stack
deploy/compose/smoke.sh  # assert everything is healthy
make compose-down    # tear down, removing volumes
```

The stack runs grex (built from local source, OpAMP listener terminating TLS
with generated dev certificates), two OpenTelemetry Collector agents and one
gateway connecting to grex over mTLS, and Dex as the dev OIDC issuer. Ports on
localhost: grex UI `8080`, grex telemetry `9090`, grex OpAMP `4320`, Dex
`5556`. Dev certificates are minted once into `deploy/compose/certs/` by a
one-shot container; delete the directory to regenerate.

## License

Apache-2.0, see [LICENSE](LICENSE).
