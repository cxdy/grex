-- User-submitted mutation intent, per docs/spec/design.md's "Jobs: schema
-- and execution". A jobs row is created in 'planned' status with nothing
-- dispatched yet; a separate arm step (not built yet) materializes
-- job_targets and schedules real dispatch after a cancellable delay.
CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Same filter language GET /api/agents already uses.
    filter TEXT NOT NULL,
    -- e.g. 'restart'. No CHECK constraint: new action types are additive,
    -- not a fixed enum grex wants to gate at the schema layer.
    action TEXT NOT NULL,
    -- Action-specific knobs (restart's reconnect_timeout/backoff_cap, for
    -- one). One shared shape across action types rather than a column per
    -- action.
    action_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 'planned' | 'armed' | 'cancelled' | 'dispatched'.
    status TEXT NOT NULL DEFAULT 'planned',
    -- 'recompute' | 'freeze'. Chosen on the arm call, not at creation (the
    -- arm step isn't built yet), so this stays null until then; no default
    -- baked in here since "recompute" is the arm call's default, not a
    -- schema-level one.
    target_mode TEXT,
    submitted_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    armed_at TIMESTAMPTZ,
    -- When real dispatch begins: arm time plus the 5-minute cancellable
    -- delay. Null until armed.
    dispatch_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ
);

-- job_targets: one row per instance_uid matched at freeze time or at the
-- end-of-arm-delay recompute moment (materialized exactly once, per the
-- design doc's "Known scope boundary"). Each row becomes one River job, the
-- dispatch attempt for that agent.
--
-- No foreign key to agents: a job_targets row is a historical dispatch
-- record that should survive an agent's eventual hard-delete (purge), not
-- disappear with it.
CREATE TABLE job_targets (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    instance_uid TEXT NOT NULL,
    -- 'pending' | 'sent' | 'send_failed' | 'applied' | 'failed'.
    status TEXT NOT NULL DEFAULT 'pending',
    dispatched_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (job_id, instance_uid)
);

CREATE INDEX idx_job_targets_job_id ON job_targets (job_id);
