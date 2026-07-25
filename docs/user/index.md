# User guide

This guide is for **operators** who use grex’s web UI to understand collector
fleet health. You do not need to run grex yourself—someone may have deployed
it for you—but you need a browser and the UI URL.

## What you can do

- Browse all known collectors (agents and gateways) on the **fleet** page
- Filter by health, connection, gateway path, and attributes
- Open an **agent detail** page for attributes, capabilities, and effective config
- View **server status** (grex version, uptime, fleet summary counts)

## What you cannot do (1.0)

grex is **read-only**. The UI does not:

- Edit or push remote configuration
- Restart collectors
- Upgrade agent packages
- Change grex server settings

Those capabilities are out of scope for 1.0 (see [SPEC](../spec/design.md)).

## Access and authentication

!!! warning "Authentication not yet available"
    UI and API login (mTLS client certs and OIDC via Dex) is
    [planned but not implemented](https://github.com/dennisme/grex/issues/11).
    Today, anyone who can reach the UI listener can use the fleet views.
    Operators should treat the URL as internal until auth ships.

## Pages

| Page | Path | Description |
|------|------|-------------|
| [Fleet overview](fleet-overview.md) | `/` | Table of agents with filters and auto-refresh |
| [Agent detail](agent-detail.md) | `/agents/{instance_uid}` | Full snapshot for one collector |
| [Server status](server-status.md) | `/status` | grex build info and fleet counts |

## Concepts in one minute

| Term | Meaning in the UI |
|------|-------------------|
| **Connected** | Agent checked in recently enough (or TCP still open for direct connections) |
| **Disconnected** | Missed a check-in window but not yet evicted; last health is retained |
| **Healthy / Unhealthy** | From the agent’s OpAMP health report (not the same as connected) |
| **Via gateway** | Session is multiplexed through an OpAMP gateway collector |
| **Role** | Best-effort label: `service.component`, or Gateway if `service.name` contains “gateway”, else Collector |

More detail: [Fleet state (developer)](../developer/fleet-state.md).
