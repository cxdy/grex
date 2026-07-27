# Testing

## Unit tests

```sh
just test
# equivalent: go test -race ./...
```

Race detector is required in CI and the justfile.

### Package patterns

| Package | What tests emphasize |
|---------|----------------------|
| `config` | Defaults, env overrides, validation errors |
| `fleet` | Registry lifecycle, eviction, attributes, views |
| `opamp` | Handler behavior, gateway JSON, e2e server |
| `api` | Pagination, filters, status JSON, reserved keys |
| `metrics` | Registry split, fleet collector, golden gathers |
| `server` | Listeners, TLS, metrics path separation, probes |
| `ui` | Routes, filters, rendering smoke |

Prefer table-driven tests. For Prometheus, use
`prometheus/testutil` gather/compare helpers (see `metrics_test.go`).

### TLS

`internal/testcert` builds ephemeral CAs/certs so server TLS tests do not
depend on files under `deploy/compose/certs/`.

## Lint

```sh
just lint
just markdownlint
```

Go lint config: `.golangci.yml` (CI: `golang-lint` workflow). Markdown:
`.markdownlint.yaml` / `.markdownlint-cli2.yaml` (CI: `markdownlint` workflow).
Local hooks via `pre-commit` also run golangci-lint, Go fmt/build/test, and
markdownlint.

## Coverage

```sh
just coverage   # coverage.out, coverage.html, coverage.xml
```

CI posts a Cobertura report on PRs (`golang-tests` workflow) with a 70%
line-coverage floor.

## Compose smoke

```sh
just compose-up
deploy/compose/smoke.sh
just compose-down
```

Smoke is the multi-container honesty check: grex health, metrics, and
collector log signals. CI builds the compose images (`docker compose build`)
on every PR; full smoke may be run locally or extended in CI later.

## Manual: DB read fallback

Reproduces `GET /api/agents/{id}` (and the UI's `/agents/{id}`) answering
for an agent this grex process never saw locally — the scenario
`internal/api` and `internal/ui`'s `StateStore` fallback exists for (see
[Persistence](persistence.md)). `cmd/seed-agent` writes fake agents
straight into Postgres, bypassing `fleet.Registry` entirely, simulating "a
sibling replica already flushed this."

```sh
# 1. Bring up Postgres and apply both schemas (grex's own + River's).
docker compose up -d postgres --wait
just migrate-up
DATABASE_URL="postgres://grex:grex-dev-password@localhost:5432/grex?sslmode=disable" \
  go run ./cmd/river-migrate

# 2. Seed one or more agents directly, no grex process involved yet.
DATABASE_URL="postgres://grex:grex-dev-password@localhost:5432/grex?sslmode=disable" \
  go run ./cmd/seed-agent agent-from-replica-1 agent-from-replica-2

# 3. Run grex with database configured, plaintext listeners for a quick
#    local check (see config.example.yaml for the full database: block).
cat > /tmp/db-fallback-config.yaml <<'EOF'
listeners:
  opamp: "127.0.0.1:14320"
  ui: "127.0.0.1:18080"
  telemetry: "127.0.0.1:19090"
database:
  host: 127.0.0.1
  port: 5432
  user: grex
  password: grex-dev-password
  dbname: grex
  sslmode: disable
EOF
go run ./cmd/grex -config /tmp/db-fallback-config.yaml &

# 4. Fetch an id this process's own registry never reported.
curl -s -w '\nHTTP %{http_code}\n' http://127.0.0.1:18080/api/agents/agent-from-replica-1
# want: 200, the seeded agent's JSON

curl -s -o /dev/null -w 'HTTP %{http_code}\n' http://127.0.0.1:18080/api/agents/totally-unknown
# want: 404 (missing from both registry and database)

curl -s -o /dev/null -w 'HTTP %{http_code}\n' http://127.0.0.1:18080/agents/agent-from-replica-1
# want: 200, the UI agent-detail page
```

Kill the `go run ./cmd/grex` background job and `docker compose down -v`
when done — it's easy to leave a stray process holding the ports across
terminal sessions; check with `lsof -i :14320 -i :18080 -i :19090` if a
later run reports "address already in use."

## What not to do

- Do not mark tests that require Docker as plain `go test` without build tags
  if they would break default developer machines
- Do not hit real external OIDC providers; Dex static users are for future
  auth work
- Do not assert on wall-clock flakiness; prefer fake clocks or generous
  heartbeat settings in unit tests

## Coverage philosophy

Design calls for TDD per package with compose as the end-to-end harness.
When adding a metric or API field, add a test that fails before the
implementation lands.
