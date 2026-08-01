# AGENTS.md

Project notes for AI coding agents working in this repo. Read
`docs/spec/design.md` first for anything touching architecture or
behavior — it's the living design spec and changes frequently; this file
won't duplicate it.

## What this is

grex is an OpAMP control plane for OpenTelemetry Collector fleets, in Go.
Server side of the [OpAMP spec](https://opentelemetry.io/docs/specs/opamp/).
Read-only fleet view in 1.0, with one mutating exception so far
(`POST /api/jobs`, see `docs/developer/read-api.md`).

## Commands

```sh
just build         # go build ./...
just test          # go test -race ./...
just lint          # golangci-lint run
just markdownlint   # markdownlint-cli2 on repo markdown
just compose-up     # build and start the full local dev stack
just compose-down   # tear down, removing volumes
deploy/compose/smoke.sh  # assert the compose stack is healthy end to end
```

`just` with no args lists every recipe (`justfile`).

## Testing conventions

- TDD: write a failing test first, confirm it fails, then implement.
- No mocks. `internal/persistence` tests run against a real, disposable
  Postgres via testcontainers (`newTestStore(t)`); Docker must be running.
  Unit tests elsewhere use small hand-written fakes (`fakeStateStore`,
  `fakeConnectionStore`, etc.), not mocking frameworks.
- `go test -race ./...` must stay clean; so must `golangci-lint run ./...`.

## Docker / compose debugging

The compose dev stack (`compose.yaml`, `deploy/compose/`) runs grex (2
replicas), Postgres, Envoy, Dex, Prometheus, an OpAMP gateway, and three
collectors (one Supervisor-managed, two bare `opamp` extension).

**Known flaky failure mode on macOS Docker Desktop:** containers exit on
startup with `resource deadlock avoided` reading a bind-mounted file —
different file, different service, every time (seen on `prometheus.yml`,
`supervisor.yaml`, TLS certs, migration `.sql` files). This is a Docker
Desktop virtiofs bug, not a grex bug. Confirmed by hitting it repeatedly
against unmodified compose config on an otherwise-passing branch.

Recovery, in order of how disruptive it is:

```sh
just compose-down          # tear down first, always — don't prune under a live stack
docker system prune -af    # remove all stopped containers, unused networks/images/build cache
docker image prune -af     # remove all images not used by an existing container (redundant with
                            # the above most of the time, but cheap and sometimes catches stragglers)
just compose-up            # retry
```

If it keeps recurring after a prune: update Docker Desktop, or switch its
file-sharing backend (Settings → General → virtiofs vs gRPC-FUSE) — this is
the actual upstream bug, not something grex's compose files can work
around. If it's still unreliable after that, treat it as a signal to run
the compose stack on a non-macOS dev box instead of fighting the local
environment further.

## Structure pointers

- `cmd/grex` — main binary entrypoint, wires everything in `main.go`.
- `internal/fleet` — in-memory agent registry, the fast path for every read.
- `internal/persistence` — Postgres-backed durability (opt-in via
  `database.host`), River-based jobs/purge, migrations in
  `internal/persistence/migrations/` (numbered, `NNN_description.up/down.sql`).
- `internal/api` — JSON read API (`docs/developer/read-api.md`).
- `internal/ui` — server-rendered fleet web UI (htmx).
- `internal/opamp` — OpAMP protocol handler, bridges opamp-go to the registry.
- `docs/spec/design.md` — architecture decisions, known gaps, what's built
  vs. not yet built. Any change affecting behavior/config/architecture
  updates this file in the same PR — not a follow-up task.
