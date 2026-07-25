# Extending grex

Practical recipes for common changes. Keep 1.0 non-goals in mind: no
mutations, no persistence, no multi-tenancy unless the SPEC and an issue
explicitly open that work.

## Add a Prometheus metric

1. Decide **server** vs **fleet** registry (see
   [Observability](../observability/index.md)).
2. Event-driven counter/gauge updates → extend `metrics.Events` and the
   `fleet.Events` (or opamp `Metrics`) interface if the source is registry/OpAMP.
3. Scrape-time gauge from fleet state → extend `metrics.FleetCollector`
   `Describe`/`Collect`.
4. Register in the correct registry inside `NewEvents` / collector setup.
5. Add gather/compare tests.
6. Document in [Metrics](../observability/metrics.md).

Cardinality: avoid unbounded labels. Per-agent series already use
`instance_uid` under a hard cap.

## Add an API field

1. Add data on `fleet.Agent` if it is state (populate from OpAMP in
   `opamp` / registry apply paths).
2. Expose via `AgentView` in `fleet/view.go` (summary vs detail).
3. API handlers already encode views as JSON—no DTO layer.
4. Extend `handler_test.go` assertions.
5. Update [Read API](read-api.md) docs.

If the field is filterable, decide whether it is a reserved bool (must update
`api.boolFields` **and** `fleet.ReservedAttributeKeys` together) or an
attribute matcher.

## Add an API route

1. Implement method on `api.Handler`.
2. `Mount` with Go 1.22 pattern and pass through `wrap` for metrics:

   ```go
   mux.Handle("GET /api/example", wrap("/api/example", http.HandlerFunc(h.example)))
   ```

3. Tests with `net/http/httptest` and a registry fixture.
4. Document under [Read API](read-api.md) and [Endpoints](../reference/endpoints.md).

## Add a UI page or partial

1. Template in `internal/ui/templates/`.
2. Handler method + `Mount` route; partials under `/partials/...` for htmx.
3. Reuse filters/views from `api` / `fleet`.
4. CSS only if needed (`static/app.css`); keep design tokens.
5. UI test for status code and a stable selector/string.

## Change check-in / eviction policy

Logic lives in `fleet.Registry` with config from `fleet.heartbeat_interval`
and `stale_missed_heartbeats`. Adjust tests that encode timing carefully;
document operator-facing behavior in user/admin docs.

## Wire authentication (future)

Follow [issue #11](https://github.com/dennisme/grex/issues/11) and the
[design SPEC](../spec/design.md) milestones (UI mTLS → OIDC). Expect new
config structs, middleware on the UI mux, and auth metrics. Do not half-land
auth without updating [Admin: authentication](../admin/authentication.md).

## Config field checklist

1. Struct field on `config.Config` (or nested)
2. Default in `defaults()`
3. Env override in `envOverrides()` with `GREX_…` name
4. Validation in `validate()`
5. `config.example.yaml` comment
6. Admin configuration docs
7. Optional: label on `grex_config_info` if operators should see it in
   metrics (non-secret only)
