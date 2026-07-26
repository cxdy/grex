# Compose stack

This tutorial brings up grex with a realistic local fleet: OpAMP gateway,
two agents, one OTLP gateway collector, Dex (for future OIDC work), and
Prometheus scraping grex the way production should.

## Prerequisites

- Docker with **Compose v2**
- [just](https://github.com/casey/just) (or run the `docker compose` commands from the `justfile` by hand)
- Ports free on localhost: `8080`, `9090`, `4320`, `5556`, `9091`, `5432`, `9187`

## Start the stack

From the repository root:

```sh
just compose-up
```

This builds grex from local source, mints dev certificates (one-shot
`gen-certs` container into `deploy/compose/certs/`), and waits for healthchecks.

Then run the smoke script:

```sh
deploy/compose/smoke.sh
```

Smoke asserts grex health, metrics endpoints, and collector-related signals
via logs rather than container healthchecks (the otelcol images are
distroless and cannot run shell healthchecks; log assertions keep all
collectors, including the Supervisor-managed one, checked the same way).

## What is running

| Service | Role | Host port |
|---------|------|-----------|
| **grex** | OpAMP server, UI, metrics | UI `8080`, telemetry `9090`, OpAMP `4320` |
| **opamp-gateway** | Multiplexes agent OpAMP sessions to grex (mTLS) | (internal) |
| **otelcol-agent-1** | Collector, bare `opamp` extension, via OpAMP gateway | (internal) |
| **otelcol-agent-2** | Collector managed by an OpAMP Supervisor (stable `instance_uid`, survives recreation), via OpAMP gateway | (internal) |
| **otelcol-gateway** | OTLP gateway topology + OpAMP via gateway | (internal) |
| **Dex** | Dev OIDC issuer (static users; grex OIDC not wired yet) | `5556` |
| **Prometheus** | Scrapes grex `/metrics` and `/metrics/fleet` as separate jobs | `9091` |
| **postgres** / **postgres-exporter** | Dev-only infra for a future durable state/job backend (see [design spec](../spec/design.md)). Nothing in grex reads or writes to it yet | `5432` / `9187` |
| **river-migrate** / **migrate** | One-shot: apply River's own schema and grex's (placeholder-only) schema respectively, then exit | (internal) |

Dev certificates live under `deploy/compose/certs/`. Delete that directory to
regenerate on the next `compose-up`.

## Explore

1. **Fleet UI:** [http://127.0.0.1:8080/](http://127.0.0.1:8080/)  
   You should see multiple collectors (agents and gateway role).
2. **Agent detail:** click a row → attributes, health, effective config.
3. **Status:** [http://127.0.0.1:8080/status](http://127.0.0.1:8080/status)
4. **Prometheus:** [http://127.0.0.1:9091/](http://127.0.0.1:9091/)  
   Jobs `grex-server` and `grex-fleet` (see [Scraping](../observability/scraping.md)).
5. **JSON API:**

   ```sh
   curl -sS http://127.0.0.1:8080/api/agents | jq '.total'
   curl -sS http://127.0.0.1:8080/api/status | jq
   ```

## Tear down

```sh
just compose-down
```

Removes containers and volumes (`docker compose down -v`).

## OpAMP gateway image note

The compose OpAMP gateway is built from
`deploy/compose/opamp-gateway-build/`. It uses a small OCB build against a
fork that wires upstream TLS into the `opampgateway` dialer (stock
observIQ images at the pinned extension version do not). Details are in the
[design SPEC](../spec/design.md) and the Dockerfile comments.

## Next steps

- [User: fleet overview](../user/fleet-overview.md)
- [Admin: TLS and mTLS](../admin/tls-mtls.md)
- [Observability: scraping](../observability/scraping.md)
- [Developer: testing](../developer/testing.md) (compose as e2e harness)
