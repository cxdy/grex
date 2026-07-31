-- Socket ownership: which grex replica currently holds a given agent's live
-- OpAMP connection, per docs/spec/design.md's "Dispatch routing:
-- agent_connections and cross-replica handoff". Tracks a different thing
-- than agent_session (data freshness) — this is "who can reach this agent
-- right now," needed so a job dispatch on any replica can hand off to the
-- one replica that actually holds the socket.
--
-- No foreign key to agents: a connection registers the instant an agent's
-- socket opens, but the agents row is written on its own async cadence
-- (Flusher's dirty-triggered flush) that can lag behind — an FK here would
-- reject a legitimate first-connection race instead of the actual bad case
-- (a row for an instance_uid that never existed).
CREATE TABLE agent_connections (
    instance_uid TEXT PRIMARY KEY,
    replica_id TEXT NOT NULL,
    connected_at TIMESTAMPTZ NOT NULL,
    -- What a lease-expiry sweep checks: a replica that crashes without
    -- deregistering leaves a stale row here until it ages out. The sweep
    -- itself is not built yet.
    last_seen TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_agent_connections_replica_id ON agent_connections (replica_id);
