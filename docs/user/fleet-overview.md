# Fleet overview

The fleet page (`/`) is the default view after you open the grex UI. It shows
every agent still in memory: connected agents and disconnected agents that
have not yet been **evicted** as stale.

## Layout

Typical columns include:

- **Status** — combined health + connected presentation (icons/labels)
- **Display name** — primarily `service.name`, then `host.name`, else instance uid
- **Role** — Collector / Gateway (or `service.component` when set)
- **Version** — `service.version` when reported
- **Attributes** — identifying and non-identifying keys as compact chips
- **Via** — direct vs gateway
- **Transport** — OpAMP `ws` or `http`
- **Last seen** — relative time of last check-in
- **Instance uid** — truncated OpAMP instance identifier (link to detail)

The table auto-refreshes via htmx at `ui.poll_interval` (default **5 seconds**).
Filter and sort query parameters are preserved across polls.

## Filters

Use the filter bar to narrow the list. Filters map to the
[read API](../developer/read-api.md):

| Control | Effect |
|---------|--------|
| Healthy | `healthy=true` / `false` |
| Connected | `connected=true` / `false` |
| Via gateway | `via_gateway=true` / `false` |
| Attribute matchers | Prometheus-style matchers (`=`, `!=`, `=~`, `!~`) |

Multiple filters are **ANDed**. Empty “Any” selections are ignored.

Examples of attribute matchers you might enter:

```text
service.name=otelcol-contrib
deployment.environment=prod
service.instance.id!~"canary-.*"
```

## Pagination and sort

- Page size defaults to **100** agents (API cap **1000**)
- Prev/next (and page controls) adjust `offset` while keeping filters
- Column sort uses `sort` and `order` query parameters (UI-only helpers on the
  list API path)

## Status meanings

| You see | Interpretation |
|---------|----------------|
| Connected + healthy | Checking in and reporting healthy |
| Connected + unhealthy | Checking in but reporting a problem (`health_error` / status text on detail) |
| Connected + health unknown | Connected but has not sent a health report yet |
| Disconnected | Missed check-ins; still visible until stale eviction |
| Missing from the table | Evicted after too many missed intervals (`fleet.stale_missed_heartbeats`) |

Disconnected is **not** the same as unhealthy. A collector can miss heartbeats
while its last reported health was still healthy.

## Tips

- Prefer attribute filters that match your org’s required attributes
  (`fleet.required_attributes` on the server) so noncompliant agents stand out
- Use the browser’s reduced-motion preference if you want calmer transitions;
  grex respects `prefers-reduced-motion`
- Deep-link filters by sharing the URL (query string)

## Next

- [Agent detail](agent-detail.md)
- [Server status](server-status.md)
