# Server status

The status page (`/status`) summarizes **grex itself** and aggregate fleet
counts. It auto-refreshes on the same poll interval as the fleet page.

## grex process

| Field | Source |
|-------|--------|
| Version | Build-time `internal/buildinfo.Version` (`-ldflags` / Docker build args) |
| Commit | Build-time commit hash |
| Go version | Runtime |
| Started at / uptime | Process start time |

Unset builds often show `dev` / `none` for version and commit.

## Fleet summary

Counts come from the in-memory registry (same data as `GET /api/status`):

| Count | Meaning |
|-------|---------|
| Total | All registered agents (connected + disconnected, not yet evicted) |
| Connected | Currently considered connected |
| Disconnected | Retained but not connected |
| Healthy / Unhealthy | Among agents that have reported health |
| Health unknown | No health report yet |
| Awaiting full state | Registered without a description yet (e.g. after grex restart while agents resend full state) |

## When to use this page

- Confirm which grex build is deployed
- Sanity-check fleet size after a rollout or outage
- Spot a large **awaiting full state** spike right after grex restarts
  (gateway-relayed agents never saw grex go away; grex requests full state
  until descriptions return)

For time-series trends, use Prometheus metrics
([Observability](../observability/index.md)) rather than eyeballing the
status page.
