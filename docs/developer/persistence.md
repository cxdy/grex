# Persistence

`internal/persistence` is a durability layer under `fleet.Registry`, not a
replacement for it. `fleet.Registry` stays the runtime source of truth for
every live read path (API, UI, metrics); the read paths covered below are
the only places grex reads from Postgres today. See
[Fleet state](fleet-state.md) for the in-memory model this sits
underneath, and [SPEC: Agent state schema](../spec/design.md) for the full
design rationale — this page covers what's actually built.

## Status: opt-in, read paths wired in

Persistence only activates when `database.host` is set. Unset, grex behaves
byte-identical to a build with no database code at all — no connection
attempted, no background goroutines started.

`fleet.Registry` is still authoritative and faster for everything it
holds. Two shapes of read path fall back to the database. Both share the
same rule: registry hits never consult the database, so there's no added
latency for the common case. The fallback result reflects that other
replica's last flush, not live state: `connected` and other session fields
can be a few seconds stale, same tolerance already accepted for the write
path.

### Single-agent

`GET /api/agents/{id}` and `internal/ui`'s agent-detail page (`GET
/agents/{id}`, `GET /partials/agents/{id}`): on a registry miss, each
handler calls `StateStore.GetAgent` before returning 404 — this is what
makes an agent live on a *sibling* grex replica (already flushed to
Postgres, never seen by this process's own registry) answerable instead of
a false 404. The UI keeps the same byte-identical 404 for a genuine miss
and for a store error alike (logged server-side, not leaked into the HTML
response); the JSON API instead returns a distinct 500 on a store error.

### Fleet-wide list

`GET /api/agents` and `internal/ui`'s fleet page (`GET /partials/agents`)
merge the local registry with one `StateStore.ListAgents` call
(`api.MergeAgents`) — local wins on overlap, it's fresher than the batched
write path.

Unlike the single-agent path, a store error here doesn't fail the request:
local registry data is valid on its own regardless of database health, so
the handler logs the error, increments
`grex_list_agents_store_fallback_errors_total{surface="api"|"ui"}` (see
[Metrics reference](../observability/metrics.md)), and serves a
registry-only list instead of losing known-good data over a database
hiccup. The JSON API's `partial` response field and the UI fleet page's
banner both reflect that degraded state — see [Read API](read-api.md).

### Soft-deleted rows

Both `StateStore.GetAgent` and `StateStore.ListAgents` return soft-deleted
rows too (`Agent.EvictedAt` set — see below); filtering those out of a
"live" read is this API/UI layer's job, not the persistence layer's. Every
read path above does that filtering: a soft-deleted agent is treated as
not-found (single-agent) or excluded from the merge (list), never
presented as if still live.

## Schema

Three tables (`internal/persistence/migrations/000002_agent_state.up.sql`),
split along the identity/session line `fleet.Agent` blurs in memory:

### `agents` — PK `instance_uid`

| Column | Notes |
|--------|-------|
| `first_seen`, `last_seen` | |
| `healthy`, `health_error`, `health_status`, `health_start_time`, `health_status_time`, `health_reported` | |
| `capabilities` | raw bitmask, decoded read-side same as the API does today |
| `identifying`, `non_identifying` | JSONB |
| `missing_attributes`, `reserved_attribute_conflicts` | `text[]`, `NOT NULL` |
| `compliance_updated_at` | |
| `evicted_at` | null while live; set at soft-delete (see below) |
| `packages` | JSONB |
| `updated_at` | event time (`Agent.LastSeen`), the write-safety guard — see below |

### `agent_session` — PK `instance_uid`, FK → `agents` `ON DELETE CASCADE`

`connected`, `remote_addr`, `tls_subject`, `via_gateway`, `transport`,
`description_reported`, `sequence_num`, `updated_at`.

Reset to disconnected unconditionally on any future load into a fresh
registry, regardless of what's stored — a restored `connected=true` would
simply be wrong, the agent hasn't reconnected to that process yet (not
built yet, since nothing reads this back; the rule is documented now so
whoever builds the read side doesn't have to rediscover it).

### `agent_effective_config` — PK `(instance_uid, filename)`, FK → `agents` `ON DELETE CASCADE`

`body`, `updated_at`. One row per config file, not a flattened blob, so a
future read path can render each file separately without pulling every
agent's config on every list query.

## Write path

1. `internal/persistence.DirtyTracker` implements `fleet.Events`. Combined
   with `internal/metrics.Events` via `fleet.MultiEvents`, so both listen
   to the same `Registry` — the metrics counters are unaffected.
2. `internal/persistence.Flusher` runs on its **own** ticker
   (`persistenceFlushInterval`, 5s in `cmd/grex`) — deliberately separate
   from `Registry.Sweep`'s ticker. Liveness/eviction and durability are
   different concerns and must not share a schedule even if the intervals
   end up similar.
3. Each tick, `Flusher` drains the dirty set and calls
   `StateStore.SaveAgent` for every `instance_uid` still present in the
   registry. An id no longer present (evicted between being marked dirty
   and this flush) gets `SoftDeleteAgent` instead — see below.
4. Whatever hasn't flushed yet at a crash is simply lost from the
   database: agents already re-report everything on reconnect
   (`ReportFullState`), so durability here only needs to survive seconds
   of staleness, not be per-message-perfect.

## Soft delete and purge

An evicted agent's row is **not** deleted immediately: `Flusher` calls
`SoftDeleteAgent`, which sets `evicted_at` (idempotent — a retried call
never moves it forward once set). The row is kept for
`fleet.soft_delete_duration` (default `7d`), so a future feature can still
answer "last seen, agent gone" — then a periodic purge job deletes it
outright.

That purge job is the first real use of River's job runtime in this
codebase (`cmd/river-migrate` only ever used River's *migrator*): an
hourly `river.PeriodicJob` (`internal/persistence/purge.go`) deletes
`agents` rows where `evicted_at` is older than the configured duration.
`agent_session`/`agent_effective_config` cascade via their foreign keys.

Metrics: `grex_agents_evicted_total` is unchanged — it already fires at
the soft-delete moment, same trigger as before. `grex_agents_purged_total`
is new, counting rows the purge job actually removes. Kept as two
counters so a dashboard can see fleet churn (soft-delete rate)
independently of storage cleanup (purge rate).

## Safeguards for going multi-instance

None of this is deployed yet — the Helm chart still pins `replicaCount: 1`
— but the write path is already built to be safe if more than one grex
process writes to the same database, because an agent's connection has no
affinity to a particular replica: it can reconnect to a *different* grex
on every connection, and nothing guarantees the replica holding older data
flushes first.

- **Guarded UPSERT, keyed on event time, not wall-clock time.** Every
  write is `INSERT ... ON CONFLICT (instance_uid) DO UPDATE SET ... WHERE
  table.updated_at < EXCLUDED.updated_at`. The value bound to
  `updated_at` is `Agent.LastSeen` — the timestamp of the actual OpAMP
  message — never the time the flush happened to run. A stale write from
  a replica that's behind becomes a no-op against a row that's already
  newer, regardless of which replica's write reaches Postgres first.

- **One whole-transaction gate, not three independent guards.**
  `agent_effective_config` has no per-row timestamp of its own (it's a
  wholesale delete-then-insert, not an upsert) — a table-by-table guard
  would leave it unprotected. Instead, `SaveAgent` checks whether the
  `agents` UPSERT actually applied (`RowsAffected() == 0` means the guard
  rejected it); if so, the whole transaction is abandoned before touching
  `agent_session` or `agent_effective_config` at all. This was a real gap
  found after the fact, not part of the original design — a stale write
  could otherwise still clobber `agent_effective_config` with old files
  after a newer replica had already written current ones.

- **`first_seen` never moves forward.** It's in the `INSERT` column list
  but deliberately absent from `ON CONFLICT DO UPDATE SET`. A replica
  meeting an agent for the first time locally creates its own registry
  entry with `FirstSeen` set to *its own* connect time — wrong, if the
  agent connected to a different replica earlier. Omitting the column
  from the update means that wrong value is discarded on conflict; the
  true first-seen time, written once on the actual first `INSERT`,
  survives untouched.

- **`Sweep`/`SetConnected` never touch `LastSeen`.** This is the
  invariant the whole guard depends on, documented on both methods in
  `internal/fleet/registry.go`. A disconnect detected well after an
  agent's last real message (e.g. by `Sweep`, possibly after the agent
  already reconnected elsewhere and flushed newer data) still carries the
  *old* timestamp, so that late write is correctly rejected instead of
  clobbering the reconnect. If `Sweep` were ever changed to stamp
  `LastSeen` at detection time, this guarantee breaks.

- **Reconnect-elsewhere is a read-side problem, not solved by writes
  alone — and not built yet.** No write-side timing can guarantee a
  replica's data lands before an agent reconnects elsewhere; the intended
  fix (see the SPEC) is the same mechanism that already handles a `grex`
  restart today: `ReportFullState`. A replica with no — or insufficiently
  fresh — local state for an `instance_uid` asks the agent to resend
  everything, rather than depending on flush timing at all. Extending
  that check to consult the database (not just local memory) on connect
  is what DB-read-fallback work would add later.

- **Sharding Postgres is a non-goal for grex.** One logical endpoint per
  fleet, HA via a primary plus replica(s) (the CloudNativePG shape fits
  well on Kubernetes, which grex already targets) — not
  partition-per-shard. Grex's app code never routes a per-agent write or
  read to a specific shard. A consumer can shard their own Postgres
  deployment as their own infrastructure choice; grex itself is built
  assuming one writable logical endpoint, optionally a separate read-only
  one for future read traffic. Because of that, a future fleet-wide list
  read (a given replica's view merged with what the database has for
  agents it doesn't hold) is one query plus a merge, not a scatter-gather
  across shards.

## Migrations

Two independent migrators against the same database — not one merged
pipeline:

- `just migrate-up` / `just migrate-down` — `golang-migrate`, owns grex's
  own tables (`internal/persistence/migrations`).
- `cmd/river-migrate` — River's own migrator, owns River's tables
  (`river_job`, `river_leader`, etc), tracked in its own
  `river_migration` table, independent of `golang-migrate`'s
  `schema_migrations`.

Both run against the same Postgres from the same deploy step; this is
tooling separation, not "two pieces of infrastructure" — River still
needs no second broker.

## Configuration

```yaml
database:
  host: "" # unset = persistence off entirely
  port: 5432
  user: ""
  password: ""
  dbname: ""
  sslmode: disable

fleet:
  soft_delete_duration: 168h # 7d; only takes effect when database.host is set
```
