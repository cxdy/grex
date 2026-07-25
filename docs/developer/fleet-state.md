# Fleet state

The fleet registry (`internal/fleet`) is the only mutable product state in
1.0. Everything else is a projection.

## Keying

Agents are stored by **`instance_uid`**. Never key by TCP connection: one
gateway socket carries many agents.

## What an `Agent` holds

High-level fields (see `fleet.Agent` for the full struct):

- Sequence number (gap detection)
- Identifying / non-identifying attributes
- Capabilities bitmask (+ decoded flags on views)
- Health (healthy, error, status strings, timestamps, reported flags)
- Effective config map: **filename → body** (not a single blob)
- Package statuses
- Connection metadata (`ConnMeta`)
- Connected flag, first/last seen
- Missing required attributes list
- Description-reported flag (for full-state convergence)

## Check-in liveness (two-stage)

Configuration:

- `fleet.heartbeat_interval` (default 30s)
- `fleet.stale_missed_heartbeats` (default 3)

| Stage | Condition | Effect |
|-------|-----------|--------|
| Connected | Check-in within interval (or direct conn still open) | `connected=true` |
| Disconnected | Missed ≥ 1 interval without check-in | `connected=false`; **retain** last health; still in UI |
| Evicted (stale) | Missed `stale_missed_heartbeats` consecutive intervals | Removed from registry; `grex_agents_evicted_total++` |

Any `AgentToServer` message counts as a check-in, not only explicit
heartbeats.

Direct OpAMP connection close still marks agents on that connection
disconnected immediately via the connection-close callback.

## Required attributes

`fleet.required_attributes` lists keys that should appear in identifying or
non-identifying attributes.

On violation:

- Warning log naming agent + missing keys
- `grex_agent_missing_attributes_total{attribute=…}`
- Gauge `grex_agents_noncompliant`
- Agent **still accepted** (observe-only)

## Reserved attribute keys

Agents must not use attribute keys that collide with API bool filters:

```text
healthy, connected, via_gateway
```

If they do, the attribute is permanently shadowed for filtering; grex
increments `grex_agent_reserved_attribute_conflicts_total` when the conflict
set changes.

## Views

| Helper | Omits bulky fields | Used by |
|--------|--------------------|---------|
| `SummaryView` | effective_config, packages | `GET /api/agents`, fleet table |
| `DetailView` | nothing | `GET /api/agents/{id}`, agent page |

Computed helpers on every view: `role`, `display_name`, `host_name`,
`version`, `capability_flags`.

## Background loop

`Registry.Run(ctx)` performs periodic stale evaluation until context cancel
(wired from `main` to the process signal context).

## Concurrency

- OpAMP callbacks write under the registry lock
- API/UI list/get under read paths
- `Events` methods are invoked **with the lock held** — metric implementations
  must not call back into the registry

## Persistence

None. Document this in any feature that assumes durable agent history.
