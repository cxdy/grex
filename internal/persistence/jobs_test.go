package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestCreateJobAlwaysStartsPlanned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Status is deliberately set to something else here: CreateJob must
	// ignore it. A job is always born "planned" — see docs/spec/design.md's
	// "Create and execute are separate calls".
	created, err := store.CreateJob(ctx, Job{
		Filter:      "service.name=otelcol-contrib",
		Action:      "restart",
		Status:      JobStatusDispatched,
		SubmittedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateJob did not assign an ID")
	}
	if created.Status != JobStatusPlanned {
		t.Errorf("Status = %q, want %q regardless of what was passed in", created.Status, JobStatusPlanned)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("CreateJob did not set CreatedAt")
	}
	if created.TargetMode != nil {
		t.Errorf("TargetMode = %v, want nil until the job is armed", *created.TargetMode)
	}
	if created.ArmedAt != nil || created.DispatchAt != nil || created.CancelledAt != nil {
		t.Errorf("a freshly created job must not be armed/dispatched/cancelled, got %+v", created)
	}
}

func TestCreateJobWithActionConfig(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	cfg := json.RawMessage(`{"reconnect_timeout":"5m","backoff_cap":"10m"}`)
	created, err := store.CreateJob(ctx, Job{
		Filter: "service.name=otelcol-contrib", Action: "restart",
		ActionConfig: cfg, SubmittedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, ok, err := store.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !ok {
		t.Fatal("GetJob: want ok = true")
	}
	var gotDecoded, wantDecoded map[string]string
	if err := json.Unmarshal(got.ActionConfig, &gotDecoded); err != nil {
		t.Fatalf("unmarshal got.ActionConfig: %v", err)
	}
	if err := json.Unmarshal(cfg, &wantDecoded); err != nil {
		t.Fatalf("unmarshal cfg: %v", err)
	}
	if gotDecoded["reconnect_timeout"] != wantDecoded["reconnect_timeout"] ||
		gotDecoded["backoff_cap"] != wantDecoded["backoff_cap"] {
		t.Errorf("ActionConfig = %s, want %s", got.ActionConfig, cfg)
	}
}

func TestCreateJobDefaultActionConfig(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	created, err := store.CreateJob(ctx, Job{
		Filter: "service.name=otelcol-contrib", Action: "restart", SubmittedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if string(created.ActionConfig) != "{}" {
		t.Errorf("ActionConfig = %s, want {} when none is given", created.ActionConfig)
	}
}

func TestGetJobNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, ok, err := store.GetJob(ctx, "00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if ok {
		t.Fatal("GetJob: want ok = false for a nonexistent id")
	}
}

func TestListJobs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.CreateJob(ctx, Job{Filter: "a", Action: "restart", SubmittedBy: "alice"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.CreateJob(ctx, Job{Filter: "b", Action: "restart", SubmittedBy: "bob"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	jobs, err := store.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListJobs returned %d rows, want 2", len(jobs))
	}
}

func TestCreateJobTargetsAndList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, Job{Filter: "service.name=otelcol-contrib", Action: "restart", SubmittedBy: "alice"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	targets, err := store.CreateJobTargets(ctx, job.ID, []string{"agent-1", "agent-2"})
	if err != nil {
		t.Fatalf("CreateJobTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("CreateJobTargets returned %d rows, want 2", len(targets))
	}
	for _, target := range targets {
		if target.Status != JobTargetStatusPending {
			t.Errorf("target %+v Status = %q, want %q", target, target.Status, JobTargetStatusPending)
		}
		if target.JobID != job.ID {
			t.Errorf("target %+v JobID = %q, want %q", target, target.JobID, job.ID)
		}
	}

	got, err := store.ListJobTargets(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListJobTargets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListJobTargets returned %d rows, want 2", len(got))
	}
}

// TestCreateJobTargetsBulk exercises CreateJobTargets' one-statement bulk
// insert (INSERT ... SELECT unnest($2::text[])) with enough rows that a
// naive one-round-trip-per-row implementation would be obviously slow —
// this is the shape a job targeting a large fraction of a fleet takes.
func TestCreateJobTargetsBulk(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, Job{Filter: "service.name=otelcol-contrib", Action: "restart", SubmittedBy: "alice"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	const n = 5000
	instanceUIDs := make([]string, n)
	for i := range instanceUIDs {
		instanceUIDs[i] = fmt.Sprintf("agent-%d", i)
	}

	targets, err := store.CreateJobTargets(ctx, job.ID, instanceUIDs)
	if err != nil {
		t.Fatalf("CreateJobTargets: %v", err)
	}
	if len(targets) != n {
		t.Fatalf("CreateJobTargets returned %d rows, want %d", len(targets), n)
	}

	got, err := store.ListJobTargets(ctx, job.ID)
	if err != nil {
		t.Fatalf("ListJobTargets: %v", err)
	}
	if len(got) != n {
		t.Fatalf("ListJobTargets returned %d rows, want %d", len(got), n)
	}
}

func TestCreateJobTargetsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	job, err := store.CreateJob(ctx, Job{Filter: "service.name=otelcol-contrib", Action: "restart", SubmittedBy: "alice"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	targets, err := store.CreateJobTargets(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("CreateJobTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("CreateJobTargets returned %d rows, want 0", len(targets))
	}
}

func TestCreateJobTargetsUnknownJob(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.CreateJobTargets(ctx, "00000000-0000-0000-0000-000000000000", []string{"agent-1"})
	if err == nil {
		t.Fatal("CreateJobTargets: want error for a job_id that doesn't exist")
	}
}

func TestJobContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.CreateJob(ctx, Job{Filter: "a", Action: "restart", SubmittedBy: "alice"}); err == nil {
		t.Fatal("CreateJob: want error for a cancelled context")
	}
	if _, _, err := store.GetJob(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("GetJob: want error for a cancelled context")
	}
	if _, err := store.ListJobs(ctx); err == nil {
		t.Fatal("ListJobs: want error for a cancelled context")
	}
	if _, err := store.CreateJobTargets(ctx, "00000000-0000-0000-0000-000000000000", []string{"agent-1"}); err == nil {
		t.Fatal("CreateJobTargets: want error for a cancelled context")
	}
	if _, err := store.ListJobTargets(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("ListJobTargets: want error for a cancelled context")
	}
}
