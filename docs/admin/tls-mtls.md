# TLS and mTLS

TLS settings apply to the **OpAMP listener** only. The UI and telemetry
listeners are plain HTTP in the current code.

## Modes

| Config | Behavior |
|--------|----------|
| No `cert_file` / `key_file` | Plaintext OpAMP (`ws://` / HTTP) |
| `cert_file` + `key_file` | Server TLS; collectors verify grex |
| + `client_ca_file` | **mTLS**: collectors must present a cert signed by that CA |

Rules enforced at load time:

- `cert_file` and `key_file` must both be set or both empty
- `client_ca_file` requires server cert/key
- Configured paths must exist

## Topology with an OpAMP gateway

In production-style fleets, TLS often appears on **two hops**:

1. Agents → OpAMP gateway (gateway’s TLS listener)
2. OpAMP gateway → grex (gateway’s client cert, grex mTLS)

grex records the **peer** certificate subject it sees. For relayed agents,
that is typically the **gateway’s** client identity, plus a via-gateway
marker. Per-agent remote addresses come from the gateway’s `connect` custom
message (logged; not joined to instance uid in 1.0).

Compose exercises this path with PEMs under `deploy/compose/certs/` generated
by `deploy/compose/gen-certs.sh`.

## Collector configuration sketch

Exact YAML depends on your distribution. Conceptually, the collector OpAMP
extension must:

- Point at grex: `wss://grex.example:4320/v1/opamp` (or the gateway, not grex,
  when using multiplexing)
- Trust grex’s (or the gateway’s) server CA
- Present a client certificate when grex (or the gateway) requires mTLS

Use the [compose agent/gateway configs](https://github.com/dennisme/grex/tree/main/deploy/compose)
as working examples.

## UI / API TLS

Not implemented in grex itself today. Terminate TLS at a reverse proxy if
you need HTTPS for browsers, and keep the UI listener off the public
internet until [auth lands](authentication.md).
