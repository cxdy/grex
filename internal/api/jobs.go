package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dennisme/grex/internal/persistence"
)

// createJobRequest is POST /api/jobs's body, per docs/spec/design.md's
// "Surface: API only for now": filter and action required, action_config
// optional and action-specific. filter is stored as opaque text — no
// validation against the agent-attribute filter language at create time,
// since matching happens later at arm time (not yet built).
type createJobRequest struct {
	Filter       string          `json:"filter"`
	Action       string          `json:"action"`
	ActionConfig json.RawMessage `json:"action_config,omitempty"`
	SubmittedBy  string          `json:"submitted_by"`
}

// jobResponse is persistence.Job's snake_case view, matching every other
// response in this package (listResponse, statusResponse) — persistence.Job
// has no json tags of its own, so encoding it directly would leak its bare
// Go field names instead.
type jobResponse struct {
	ID           string          `json:"id"`
	Filter       string          `json:"filter"`
	Action       string          `json:"action"`
	ActionConfig json.RawMessage `json:"action_config"`
	Status       string          `json:"status"`
	TargetMode   *string         `json:"target_mode"`
	SubmittedBy  string          `json:"submitted_by"`
	CreatedAt    time.Time       `json:"created_at"`
	ArmedAt      *time.Time      `json:"armed_at"`
	DispatchAt   *time.Time      `json:"dispatch_at"`
	CancelledAt  *time.Time      `json:"cancelled_at"`
}

func newJobResponse(j persistence.Job) jobResponse {
	return jobResponse{
		ID: j.ID, Filter: j.Filter, Action: j.Action, ActionConfig: j.ActionConfig,
		Status: j.Status, TargetMode: j.TargetMode, SubmittedBy: j.SubmittedBy,
		CreatedAt: j.CreatedAt, ArmedAt: j.ArmedAt, DispatchAt: j.DispatchAt, CancelledAt: j.CancelledAt,
	}
}

func (h *Handler) createJob(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		http.Error(w, "jobs require a configured database", http.StatusServiceUnavailable)
		return
	}

	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if req.Filter == "" {
		http.Error(w, "filter is required", http.StatusBadRequest)
		return
	}
	if req.Action == "" {
		http.Error(w, "action is required", http.StatusBadRequest)
		return
	}
	if req.SubmittedBy == "" {
		http.Error(w, "submitted_by is required", http.StatusBadRequest)
		return
	}

	created, err := h.jobs.CreateJob(r.Context(), persistence.Job{
		Filter:       req.Filter,
		Action:       req.Action,
		ActionConfig: req.ActionConfig,
		SubmittedBy:  req.SubmittedBy,
	})
	if err != nil {
		http.Error(w, "create job failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(newJobResponse(created))
}
