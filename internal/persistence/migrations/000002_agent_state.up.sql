-- Durable counterpart to internal/fleet.Agent, per docs/spec/design.md's
-- Agent state schema. internal/fleet.Registry remains the runtime source of
-- truth; these tables are a durability layer under it, kept current via a
-- guarded UPSERT (see updated_at below), not read from anywhere yet.

CREATE TABLE agents (
    instance_uid TEXT PRIMARY KEY,
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen TIMESTAMPTZ NOT NULL,
    healthy BOOLEAN NOT NULL DEFAULT false,
    health_error TEXT NOT NULL DEFAULT '',
    health_status TEXT NOT NULL DEFAULT '',
    health_start_time TIMESTAMPTZ,
    health_status_time TIMESTAMPTZ,
    health_reported BOOLEAN NOT NULL DEFAULT false,
    -- Raw AgentCapabilities bitmask; decoded read-side same as the API does
    -- today, no per-bit columns.
    capabilities BIGINT NOT NULL DEFAULT 0,
    identifying JSONB NOT NULL DEFAULT '{}'::jsonb,
    non_identifying JSONB NOT NULL DEFAULT '{}'::jsonb,
    missing_attributes TEXT[] NOT NULL DEFAULT '{}',
    reserved_attribute_conflicts TEXT[] NOT NULL DEFAULT '{}',
    compliance_updated_at TIMESTAMPTZ,
    -- Null while live; set at soft-delete. Soft-delete/retention behavior
    -- itself is not implemented yet, this column just exists so the schema
    -- doesn't need a later migration to add it.
    evicted_at TIMESTAMPTZ,
    packages JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Event time (Agent.LastSeen), never flush wall-clock time. The guard
    -- for the multi-writer-safe UPSERT: `WHERE agents.updated_at <
    -- EXCLUDED.updated_at`.
    updated_at TIMESTAMPTZ NOT NULL
);

-- Session/connection state: reset to disconnected unconditionally on any
-- load into a fresh registry, regardless of what's stored here — a
-- restored connected=true is simply wrong, the agent hasn't reconnected to
-- this process yet.
CREATE TABLE agent_session (
    instance_uid TEXT PRIMARY KEY REFERENCES agents (instance_uid) ON DELETE CASCADE,
    connected BOOLEAN NOT NULL DEFAULT false,
    remote_addr TEXT NOT NULL DEFAULT '',
    tls_subject TEXT NOT NULL DEFAULT '',
    via_gateway BOOLEAN NOT NULL DEFAULT false,
    transport TEXT NOT NULL DEFAULT '',
    description_reported BOOLEAN NOT NULL DEFAULT false,
    -- Display/debugging only; never read back to resume ordering checks
    -- after a reconnect; the agent's own counter isn't guaranteed
    -- continuous across a restart either.
    sequence_num BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL
);

-- One row per config file, not a flattened blob, so the API/UI can keep
-- rendering each file separately without pulling every agent's config on
-- every list query.
CREATE TABLE agent_effective_config (
    instance_uid TEXT NOT NULL REFERENCES agents (instance_uid) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    body TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (instance_uid, filename)
);
