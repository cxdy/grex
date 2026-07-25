# Admin guide

This guide is for people who **deploy and operate** grex: install the
binary or image, configure listeners and TLS, scrape metrics, and understand
process lifecycle.

## Responsibilities

- Choose listen addresses and network exposure for the three listeners
- Configure OpAMP TLS/mTLS for collectors
- Set fleet heartbeat / stale / required attribute policy
- Scrape Prometheus endpoints with safe cardinality limits
- Integrate with orchestrator health probes (`/healthz`, `/readyz`)

## Contents

| Page | Description |
|------|-------------|
| [Install](install.md) | Binary, Docker, Compose |
| [Configuration](configuration.md) | YAML + `GREX_*` reference |
| [TLS and mTLS](tls-mtls.md) | OpAMP server TLS and client certs |
| [Authentication](authentication.md) | Current state + planned UI/API auth |
| [Health and lifecycle](health-and-lifecycle.md) | Probes, drain, shutdown |
| [Scaling with gateways](scaling-with-gateways.md) | OpAMP gateway topology |

## Security posture (today)

| Surface | Protection today |
|---------|------------------|
| OpAMP listener | Optional TLS + optional mTLS (`tls.*`) |
| UI / API listener | **Open** — auth [not implemented yet](https://github.com/dennisme/grex/issues/11) |
| Telemetry listener | Unauthenticated scrape of metrics/health; optional pprof |

Run the UI and telemetry listeners on trusted networks (or behind a gateway
you control) until UI authentication ships. Prefer mTLS on OpAMP for any
non-lab collector fleet.

## Related

- [Observability](../observability/index.md) — metrics, logs, scrape jobs
- [Reference: endpoints](../reference/endpoints.md)
- [SPEC](../spec/design.md) — intended 1.0 end state
