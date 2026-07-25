# TLS and mTLS

Every listener has its own TLS block: `opamp_tls`, `ui_tls`, `telemetry_tls`.
Same shape, independent settings.

## Modes

| Config | Behavior |
|--------|----------|
| No `cert_file` / `key_file` | Plaintext (`ws://` / HTTP) |
| `cert_file` + `key_file` | Server TLS; peers verify grex |
| + `client_ca_file` | mTLS: peers must present a cert signed by that CA |

Rules enforced at load time:

- `cert_file` and `key_file` must both be set or both empty
- `client_ca_file` requires server cert/key
- Configured paths must exist

## OpAMP listener

`opamp_tls.client_ca_file` requires a valid client certificate on every
connection at the TLS handshake: there is no unauthenticated path on this
listener, so a missing or invalid cert fails the handshake outright.

## UI and telemetry listeners

`ui_tls.client_ca_file` and `telemetry_tls.client_ca_file` behave
differently: the TLS handshake accepts connections **with or without** a
client certificate (so `/healthz` and `/readyz` on the telemetry listener
stay reachable for orchestrator probes, which cannot present one). Instead,
every route except `/healthz` and `/readyz` is gated at the HTTP layer:

1. No certificate presented → `403`
2. Certificate has zero or more than one URI SAN → `403`
3. The one URI SAN is not a well-formed `spiffe://<trust-domain>/<path>` URI → `403`
4. The SPIFFE ID resolves to no role, or to `"none"`, via `auth.role_mapping` /
   `auth.default_role` → `403`
5. Otherwise the request proceeds; `grex_auth_allowed_total{role=...}` counts
   the resolved role, `grex_auth_denied_total{reason=...}` counts each denial
   above (`no_cert`, `no_uri_san`, `multiple_uri_sans`, `bad_scheme`,
   `malformed`, `no_role`)

See [Authentication](authentication.md) for the SPIFFE ID format and role
table, and [Scraping grex with Prometheus](../observability/scraping.md) for
configuring Prometheus's client certificate.

## Topology with an OpAMP gateway

In production-style fleets, OpAMP TLS often appears on **two hops**:

1. Agents → OpAMP gateway (gateway's TLS listener)
2. OpAMP gateway → grex (gateway's client cert, grex mTLS)

grex records the **peer** certificate subject it sees. For relayed agents,
that is typically the **gateway's** client identity, plus a via-gateway
marker. Per-agent remote addresses come from the gateway's `connect` custom
message (logged; not joined to instance uid in 1.0).

This gateway hop's SPIFFE-format alignment (agents presenting SPIFFE IDs
through the gateway to grex) is separate work, not covered here or by the
UI/telemetry SPIFFE work above.

Compose exercises this path with PEMs under `deploy/compose/certs/` generated
by `deploy/compose/gen-certs.sh`.

## Collector configuration sketch

Exact YAML depends on your distribution. Conceptually, the collector OpAMP
extension must:

- Point at grex: `wss://grex.example:4320/v1/opamp` (or the gateway, not grex,
  when using multiplexing)
- Trust grex's (or the gateway's) server CA
- Present a client certificate when grex (or the gateway) requires mTLS

Use the [compose agent/gateway configs](https://github.com/dennisme/grex/tree/main/deploy/compose)
as working examples.
