# Metric cardinality

## Per-agent series

These series include an `instance_uid` label:

- `grex_agent_health`
- `grex_agent_last_seen_timestamp_seconds`

Unbounded agent counts would create unbounded Prometheus series. grex applies
`metrics.per_agent_series_limit` (default **1000**).

## Cap behavior

When `len(agents) > per_agent_series_limit`:

1. **All** per-agent series are omitted (not a random subset)
2. Aggregate fleet gauges/counters still emit
3. `grex_fleet_size` continues to show true registry size
4. `grex_agent_series_capped` is set to **1**

When under the limit, `grex_agent_series_capped` is **0**.

## Operator response

1. Alert on `grex_agent_series_capped == 1` (see [Alerts](alerts.md))
2. Raise `per_agent_series_limit` only if Prometheus can absorb the series
3. Or accept aggregates-only fleet metrics and rely on the grex UI/API for
   per-agent drill-down
4. Confirm `sample_limit` on the fleet scrape job still has headroom

## Other cardinality sources

| Source | Risk | Mitigation |
|--------|------|------------|
| `grex_agent_missing_attributes_total{attribute}` | One series per required key | Small fixed set of keys |
| `grex_agent_reports_total{type}` | Fixed enum of report kinds | Low |
| `grex_api_*{route,method,code}` | Routes are fixed set | Low |
| `grex_agents_connected{transport,via}` | 2×2 label set | Low |

Do not add high-cardinality labels (raw hostnames, full config hashes) to new
metrics without an explicit SPEC decision.
