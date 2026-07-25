# Alert ideas

These are **starting points**, not a shipped rule pack. Adjust for your
Prometheus / Alertmanager / Grafana stack. No runbooks are provided here
(operational playbooks come later).

## Fleet

### Per-agent series capped

```promql
grex_agent_series_capped == 1
```

**Meaning:** fleet larger than `metrics.per_agent_series_limit`; per-uid
series omitted. UI/API still have per-agent data.

### Many disconnected agents

```promql
grex_agents_disconnected > 0
```

Tune with `>` threshold or ratio vs `grex_fleet_size` for your environment.

### Evictions increasing

```promql
rate(grex_agents_evicted_total[15m]) > 0
```

Agents disappearing after missed check-ins—network, agent crash, or
heartbeat settings too aggressive.

### Noncompliant agents

```promql
grex_agents_noncompliant > 0
```

Required attributes missing; fix agent OpAMP description config.

### Awaiting full state after deploy

```promql
grex_agents_awaiting_full_state > 0
```

Normal briefly after grex restart; alert if sustained.

## Server

### OpAMP decode errors

```promql
rate(grex_opamp_message_errors_total[5m]) > 0
```

### API error rate

```promql
sum(rate(grex_api_requests_total{code=~"5.."}[5m]))
  /
sum(rate(grex_api_requests_total[5m])) > 0.01
```

### API latency

```promql
histogram_quantile(0.99,
  sum by (le, route) (rate(grex_api_request_duration_seconds_bucket[5m]))
)
```

### Process up

Rely on scrape failure of `grex-server` and/or `up{job="grex-server"} == 0`
rather than grex-specific series alone.

## Config drift

Compare `grex_config_info` label values after a rollout (recording rules or
Grafana checks) to ensure the running process matches intended
`heartbeat_interval`, TLS flags, and series limit.
