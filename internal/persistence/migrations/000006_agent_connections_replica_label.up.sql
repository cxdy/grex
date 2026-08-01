-- replica_label is a human-readable identity (pod name/hostname) for
-- debugging, separate from replica_id: replica_id must be a value that
-- never collides across replicas or across restarts of the same one
-- (a randomly generated UUID, held in memory only), since it is both the
-- routing key for dispatch handoff and the value written into River's own
-- Config.ID. Pod/host names churn on restart and are not guaranteed unique
-- across clusters that could share one Postgres, so they are demoted to
-- this label-only column rather than used for replica_id itself.
ALTER TABLE agent_connections ADD COLUMN replica_label TEXT NOT NULL DEFAULT '';
