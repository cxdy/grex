# Observability

grex is itself an operations control plane; it must be easy to scrape and
debug with the same Prometheus-centric habits as the rest of an OpenTelemetry
fleet.

## What exists today

| Signal | Support in grex 1.0 |
|--------|---------------------|
| **Metrics** | First-class. Prometheus text exposition on the telemetry listener: `/metrics` (server) and `/metrics/fleet` (fleet). |
| **Logs** | Process logs via `log/slog` to **stderr**. Levels and `text`/`json` formats. No built-in log shipping. |
| **Traces** | grex does **not** export distributed traces of its own request path. No OTLP exporter for grex self-telemetry. |

Agents may still report **own-telemetry** metadata over OpAMP; that is agent
configuration data, not grex spanning traces.

## Design decisions

- **Prometheus scrape only** for grex metrics (no OTLP metrics export in 1.0)
- **Two metric endpoints** so server and fleet jobs have independent intervals
  and `sample_limit` protection
- **Telemetry listener** separate from UI and OpAMP for network policy
- **pprof** optional on telemetry, off by default

## Contents

| Page | Description |
|------|-------------|
| [Metrics](metrics.md) | Full series catalog |
| [Scraping](scraping.md) | Recommended Prometheus jobs |
| [Cardinality](cardinality.md) | Per-agent series cap |
| [Logging](logging.md) | slog configuration and what to collect |
| [Tracing](tracing.md) | What is absent and what may come later |
| [Alerts](alerts.md) | Starter PromQL ideas |
| [Debug pprof](debug-pprof.md) | Runtime profiling |

## Quick checks

```sh
curl -sS http://127.0.0.1:9090/healthz
curl -sS http://127.0.0.1:9090/metrics | grep grex_build_info
curl -sS http://127.0.0.1:9090/metrics/fleet | grep grex_fleet_size
```
