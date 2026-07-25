# Agent detail

Open any fleet row (or navigate to `/agents/{instance_uid}`) for a full
snapshot of one collector.

## What you see

- **Identity** — instance uid, display name, role, version, host
- **Attributes** — identifying and non-identifying `AgentDescription` maps
- **Health** — healthy flag, status string, error, start/status times
- **Connection** — remote address, TLS subject (when mTLS is used on OpAMP),
  transport, via-gateway flag
- **Capabilities** — decoded OpAMP capability flags plus raw bitmask
- **Effective configuration** — one YAML (or text) block per config file key
- **Packages** — package status map when the agent reports it
- **Missing attributes** — required keys the agent has not reported
  (observe-only; the agent is still accepted)

## Refresh behavior

Unlike the fleet and status pages, the agent detail page does **not**
auto-refresh. That preserves scroll position while you read long configs.
Use the **Refresh** control to reload the partial on demand.

## 404

If the agent was **evicted** as stale, or never registered, the detail route
shows a not-found page. Reconnect the collector so it re-registers; the
instance uid may reappear after the next check-in.

## Tips for operators

- Compare effective config across agents by opening two browser tabs
- Prefer filtering on the fleet page before drilling in when hunting a
  subset of the fleet
- If “via gateway” is set, the remote address is often the **gateway’s**
  peer view, not the original agent socket (see
  [OpAMP and gateway](../developer/opamp-and-gateway.md))
