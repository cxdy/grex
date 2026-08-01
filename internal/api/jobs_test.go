package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/persistence"
)

// fakeJobQueue is a minimal spy JobQueue, mirroring fakeAPIStateStore's
// style: exercises createJob's request/response handling without a real
// Postgres. CreateJobTargets/ListJobTargets are unused by createJob and
// just panic if ever called.
type fakeJobQueue struct {
	created persistence.Job
	err     error

	gotJob persistence.Job // the Job createJob actually passed to CreateJob
}

var _ persistence.JobQueue = (*fakeJobQueue)(nil)

func (f *fakeJobQueue) CreateJob(_ context.Context, job persistence.Job) (persistence.Job, error) {
	f.gotJob = job
	if f.err != nil {
		return persistence.Job{}, f.err
	}
	return f.created, nil
}

func (f *fakeJobQueue) GetJob(context.Context, string) (persistence.Job, bool, error) {
	panic("not used by createJob")
}

func (f *fakeJobQueue) ListJobs(context.Context) ([]persistence.Job, error) {
	panic("not used by createJob")
}

func (f *fakeJobQueue) CreateJobTargets(context.Context, string, []string) ([]persistence.JobTarget, error) {
	panic("not used by createJob")
}

func (f *fakeJobQueue) ListJobTargets(context.Context, string) ([]persistence.JobTarget, error) {
	panic("not used by createJob")
}

func newMuxWithJobs(t *testing.T, jobs persistence.JobQueue) http.Handler {
	t.Helper()
	h := New(testRegistry(t, 0), time.Now(), nil, nil, jobs)
	mux := http.NewServeMux()
	h.Mount(mux, nil)
	return mux
}

func doPostJSON(t *testing.T, h http.Handler, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestCreateJobHappyPath(t *testing.T) {
	created := persistence.Job{
		ID: "job-1", Filter: "service.name=otelcol-contrib", Action: "restart",
		Status: persistence.JobStatusPlanned, SubmittedBy: "alice", CreatedAt: time.Now(),
	}
	queue := &fakeJobQueue{created: created}
	h := newMuxWithJobs(t, queue)

	code, raw := doPostJSON(t, h, map[string]string{
		"filter": "service.name=otelcol-contrib", "action": "restart", "submitted_by": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%s)", code, http.StatusCreated, raw)
	}

	var got jobResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, raw)
	}
	if got.ID != "job-1" || got.Status != persistence.JobStatusPlanned {
		t.Errorf("response = %+v, want id=job-1 status=planned", got)
	}

	if queue.gotJob.Filter != "service.name=otelcol-contrib" || queue.gotJob.Action != "restart" || queue.gotJob.SubmittedBy != "alice" {
		t.Errorf("CreateJob called with %+v, want filter/action/submitted_by from the request", queue.gotJob)
	}

	// Response keys must be snake_case, matching every other endpoint in
	// this API (listResponse, statusResponse) — not persistence.Job's bare
	// Go field names.
	var rawFields map[string]any
	if err := json.Unmarshal(raw, &rawFields); err != nil {
		t.Fatalf("decode response as map: %v (body=%s)", err, raw)
	}
	for _, key := range []string{"id", "filter", "action", "status", "submitted_by", "created_at"} {
		if _, ok := rawFields[key]; !ok {
			t.Errorf("response missing snake_case key %q; got keys %v", key, rawFields)
		}
	}
	for _, key := range []string{"ID", "SubmittedBy", "CreatedAt"} {
		if _, ok := rawFields[key]; ok {
			t.Errorf("response has PascalCase key %q, want snake_case only", key)
		}
	}
}

func TestCreateJobWithActionConfig(t *testing.T) {
	queue := &fakeJobQueue{created: persistence.Job{ID: "job-1", Status: persistence.JobStatusPlanned}}
	h := newMuxWithJobs(t, queue)

	code, _ := doPostJSON(t, h, map[string]any{
		"filter": "service.name=otelcol-contrib", "action": "restart", "submitted_by": "alice",
		"action_config": map[string]string{"reconnect_timeout": "5m"},
	})
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", code, http.StatusCreated)
	}
	if string(queue.gotJob.ActionConfig) != `{"reconnect_timeout":"5m"}` {
		t.Errorf("ActionConfig = %s, want the request's action_config verbatim", queue.gotJob.ActionConfig)
	}
}

func TestCreateJobRequiresFilter(t *testing.T) {
	h := newMuxWithJobs(t, &fakeJobQueue{})
	code, _ := doPostJSON(t, h, map[string]string{"action": "restart", "submitted_by": "alice"})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a missing filter", code, http.StatusBadRequest)
	}
}

func TestCreateJobRequiresAction(t *testing.T) {
	h := newMuxWithJobs(t, &fakeJobQueue{})
	code, _ := doPostJSON(t, h, map[string]string{"filter": "service.name=x", "submitted_by": "alice"})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a missing action", code, http.StatusBadRequest)
	}
}

func TestCreateJobRequiresSubmittedBy(t *testing.T) {
	h := newMuxWithJobs(t, &fakeJobQueue{})
	code, _ := doPostJSON(t, h, map[string]string{"filter": "service.name=x", "action": "restart"})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a missing submitted_by", code, http.StatusBadRequest)
	}
}

func TestCreateJobRejectsMalformedBody(t *testing.T) {
	h := newMuxWithJobs(t, &fakeJobQueue{})
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a malformed body", rec.Code, http.StatusBadRequest)
	}
}

func TestCreateJobStoreErrorReturns500(t *testing.T) {
	queue := &fakeJobQueue{err: errors.New("insert failed")}
	h := newMuxWithJobs(t, queue)
	code, _ := doPostJSON(t, h, map[string]string{
		"filter": "service.name=x", "action": "restart", "submitted_by": "alice",
	})
	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", code, http.StatusInternalServerError)
	}
}

// TestCreateJobWithoutJobQueueConfigured covers the opt-in-persistence case
// (database.host unset): jobs cannot exist without a JobQueue, so this must
// fail clearly rather than nil-pointer panic.
func TestCreateJobWithoutJobQueueConfigured(t *testing.T) {
	h := newMuxWithJobs(t, nil)
	code, _ := doPostJSON(t, h, map[string]string{
		"filter": "service.name=x", "action": "restart", "submitted_by": "alice",
	})
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d when no JobQueue is configured", code, http.StatusServiceUnavailable)
	}
}
