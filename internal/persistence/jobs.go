package persistence

import (
	"context"
	"encoding/json"
	"fmt"
)

var _ JobQueue = (*PostgresStore)(nil)

// CreateJob inserts a jobs row. Status and TargetMode on the given Job are
// ignored: every job is born StatusPlanned with TargetMode unset, per
// docs/spec/design.md's "Create and execute are separate calls" — arming a
// job (not built yet) is the only thing that can change either.
func (s *PostgresStore) CreateJob(ctx context.Context, job Job) (Job, error) {
	actionConfig := job.ActionConfig
	if len(actionConfig) == 0 {
		actionConfig = json.RawMessage("{}")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (filter, action, action_config, submitted_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, filter, action, action_config, status, target_mode,
			submitted_by, created_at, armed_at, dispatch_at, cancelled_at`,
		job.Filter, job.Action, actionConfig, job.SubmittedBy)
	created, err := scanJob(row)
	if err != nil {
		return Job{}, fmt.Errorf("insert jobs: %w", err)
	}
	return created, nil
}

// GetJob reads one jobs row by id.
func (s *PostgresStore) GetJob(ctx context.Context, id string) (Job, bool, error) {
	jobs, err := s.queryJobs(ctx, `WHERE id = $1`, id)
	if err != nil {
		return Job{}, false, err
	}
	if len(jobs) == 0 {
		return Job{}, false, nil
	}
	return jobs[0], true, nil
}

// ListJobs reads every jobs row.
func (s *PostgresStore) ListJobs(ctx context.Context) ([]Job, error) {
	return s.queryJobs(ctx, "")
}

func (s *PostgresStore) queryJobs(ctx context.Context, where string, args ...any) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, filter, action, action_config, status, target_mode,
			submitted_by, created_at, armed_at, dispatch_at, cancelled_at
		FROM jobs `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan jobs: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func scanJob(row rowScanner) (Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.Filter, &j.Action, &j.ActionConfig, &j.Status, &j.TargetMode,
		&j.SubmittedBy, &j.CreatedAt, &j.ArmedAt, &j.DispatchAt, &j.CancelledAt); err != nil {
		return Job{}, err
	}
	return j, nil
}

// CreateJobTargets inserts one job_targets row per instanceUID, all
// JobTargetStatusPending, as one bulk INSERT rather than one round trip per
// row: this is a single materialization event (see docs/spec/design.md's
// "Known scope boundary") that can be sized to a large fraction of a fleet
// — a per-row round trip would mean a transaction held open for as many
// network round trips as there are targets. One statement is atomic on its
// own (constraint violations roll back the whole INSERT), no explicit
// transaction needed. Called once per arm (recompute or freeze), not at job
// creation.
func (s *PostgresStore) CreateJobTargets(ctx context.Context, jobID string, instanceUIDs []string) ([]JobTarget, error) {
	if len(instanceUIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		INSERT INTO job_targets (job_id, instance_uid)
		SELECT $1, unnest($2::text[])
		RETURNING id, job_id, instance_uid, status, dispatched_at, completed_at`,
		jobID, instanceUIDs)
	if err != nil {
		return nil, fmt.Errorf("insert job_targets: %w", err)
	}
	defer rows.Close()

	targets := make([]JobTarget, 0, len(instanceUIDs))
	for rows.Next() {
		var t JobTarget
		if err := rows.Scan(&t.ID, &t.JobID, &t.InstanceUID, &t.Status, &t.DispatchedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan job_targets: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job_targets: %w", err)
	}
	return targets, nil
}

// ListJobTargets reads every job_targets row for one job.
func (s *PostgresStore) ListJobTargets(ctx context.Context, jobID string) ([]JobTarget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, job_id, instance_uid, status, dispatched_at, completed_at
		FROM job_targets WHERE job_id = $1 ORDER BY id`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query job_targets: %w", err)
	}
	defer rows.Close()

	var targets []JobTarget
	for rows.Next() {
		var t JobTarget
		if err := rows.Scan(&t.ID, &t.JobID, &t.InstanceUID, &t.Status, &t.DispatchedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan job_targets: %w", err)
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job_targets: %w", err)
	}
	return targets, nil
}
