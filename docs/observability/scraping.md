# Scraping grex with Prometheus

## Recommended layout

Two scrape jobs against the **same** telemetry host:port, different paths.

```yaml
scrape_configs:
  - job_name: grex-server
    metrics_path: /metrics
    scrape_interval: 15s
    static_configs:
      - targets: ["grex:9090"]

  - job_name: grex-fleet
    metrics_path: /metrics/fleet
    scrape_interval: 30s   # ≈ heartbeat granularity
    sample_limit: 5000     # protect Prometheus; tune to fleet size
    static_configs:
      - targets: ["grex:9090"]
```

This matches `deploy/compose/prometheus.yaml` used in local development.

## Choosing intervals

| Job | Guidance |
|-----|----------|
| `grex-server` | Short interval (10–30s) for API latency and process health |
| `grex-fleet` | Near `fleet.heartbeat_interval` (default 30s); faster scrapes mostly re-read the same registry snapshot |

## sample_limit

Prometheus `sample_limit` is **per scrape job**. Put a limit on
`grex-fleet` so a misconfigured cardinality explosion cannot drop
`grex-server` samples you need for diagnosis.

Also set `metrics.per_agent_series_limit` in grex so the process itself stops
emitting per-uid series above the cap (see [Cardinality](cardinality.md)).

## Network access

The telemetry listener has **no application authentication**. Restrict it
with:

- Bind address (e.g. internal interface only)
- Network policies / security groups
- Mesh or reverse proxy IP allowlists

Do not expose `:9090` to the public internet, especially if
`debug.pprof_enabled` is true.

## Service discovery

### Helm ServiceMonitors (optional)

The [Helm chart](../admin/helm.md) can create **two** Prometheus Operator
`ServiceMonitor` resources when `serviceMonitor.enabled=true`: one for
`/metrics` and one for `/metrics/fleet`, with independent intervals. That
matches the dual-job layout above and the compose Prometheus config.

Requires the Prometheus Operator CRDs in the cluster. Set
`serviceMonitor.labels` so your Prometheus instance selects the monitors.

### Without the operator

Point static configs, file SD, annotation-based discovery, or your
platform’s SD at the telemetry port. Keep the two jobs even if targets are
identical. The chart’s `metrics.serviceAnnotations.enabled` only annotates
the Service for `/metrics`; it does not create a second fleet job—use an
external scrape config or ServiceMonitors for that.

## Relabeling

Useful labels to attach externally (not emitted by grex):

- `env`, `cluster`, `region` for multi-fleet deployments (one grex per fleet)
- `instance` already comes from Prometheus target labels

Avoid relabeling away `instance_uid` on fleet series if you rely on per-agent
dashboards—and prefer aggregates when fleets are large.
