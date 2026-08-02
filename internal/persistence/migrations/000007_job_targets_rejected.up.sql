-- reason records why a job_targets row is 'rejected' at arm time (see
-- docs/spec/design.md's "Decided: per-target rejection with a reason" —
-- one non-compliant agent in a filter must not block dispatch to the rest
-- of the matched fleet, and must not be silently dropped either). NULL for
-- every other status; only ever set alongside status = 'rejected'.
ALTER TABLE job_targets ADD COLUMN reason TEXT;
