# Health and lifecycle

grex exposes standard probes on the **telemetry** listener and implements a
deliberate drain sequence on shutdown.

## Probes

| Path | Meaning | HTTP |
|------|---------|------|
| `GET /healthz` | **Liveness** — process up, handlers respond | Always `200` once serving |
| `GET /readyz` | **Readiness** — accepting work | `200` when ready; `503` when draining / not ready |

Important properties:

- `/healthz` never reflects downstream dependencies or readiness. It does
  not flip during graceful drain, so orchestrators that only check liveness
  do not thrash.
- `/readyz` is false until listeners are up, and becomes false when
  **BeginDraining** runs (before listeners close).

Compose healthchecks use `/healthz` intentionally so `docker compose` does
not mark the container unhealthy during the drain window.

## Graceful shutdown

On `SIGINT` / `SIGTERM` (see `cmd/grex`):

1. Log shutdown reason
2. **BeginDraining** → `/readyz` returns `503`
3. Wait **drainDelay** = **5s** so load balancers can stop new traffic
4. **Shutdown** listeners with **shutdownGrace** = **10s** context timeout

If a listener fails fatally, grex logs the error and attempts shutdown
without the intentional drain delay path used for signals.

## Fleet state on restart

There is **no persistent store**. After restart:

- Direct agents reconnect and re-report
- Gateway-relayed agents never observed grex restarting; grex sets
  `ReportFullState` until descriptions return
- Metrics such as `grex_agents_awaiting_full_state` spike briefly during
  convergence

## Recommended orchestrator wiring

- Liveness → `/healthz` on the telemetry port
- Readiness → `/readyz` (stop routing when draining)
- Metrics scrapes → `/metrics` and `/metrics/fleet` (separate jobs)
- Do not put OpAMP and UI behind the same readiness semantics as telemetry
  without understanding that drain only marks the telemetry readiness flag
  used by grex’s own `/readyz`

The [Helm chart](helm.md) follows this wiring by default (`livenessProbe` /
`readinessProbe` on the named `telemetry` port,
`terminationGracePeriodSeconds: 30`).
