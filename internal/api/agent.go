package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type AgentKernel interface {
	GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	GetStep(ctx context.Context, execID uuid.UUID, stepID string) (domain.Step, error)
	AuthorizeStepDelta(ctx context.Context, execID uuid.UUID, stepID string, lease domain.Lease) error
	ListSteps(ctx context.Context, execID uuid.UUID) ([]domain.Step, error)
	SubmitStep(ctx context.Context, execID uuid.UUID, req kernel.SubmitStepRequest) (domain.StepDecision, error)
	CompleteStep(ctx context.Context, execID uuid.UUID, stepID string, req kernel.CompleteStepRequest) (domain.StepDecision, error)
	FailStep(ctx context.Context, execID uuid.UUID, stepID string, req kernel.FailStepRequest) (domain.StepDecision, error)
	Heartbeat(ctx context.Context, execID uuid.UUID, lease domain.Lease) error
	CompleteExecution(ctx context.Context, execID uuid.UUID, lease domain.Lease, output, state json.RawMessage) error
	FailExecution(ctx context.Context, execID uuid.UUID, lease domain.Lease, reason string) error
	Suspend(ctx context.Context, execID uuid.UUID, lease domain.Lease) (bool, error)
	RegisterResource(ctx context.Context, execID uuid.UUID, req kernel.RegisterResourceRequest) (kernel.ResourceView, error)
	BindResource(ctx context.Context, execID uuid.UUID, key string, req kernel.BindResourceRequest) error
	PublishCheckpoints(ctx context.Context, execID uuid.UUID, req kernel.PublishCheckpointsRequest) error
}

// The lease the kernel issued in the webhook, which every mutation sends back.
const (
	headerDispatchID      = "Rebuno-Dispatch-Id"
	headerDispatchAttempt = "Rebuno-Dispatch-Attempt"
)

func leaseFrom(r *http.Request) (domain.Lease, error) {
	dispatchID, err := uuid.Parse(r.Header.Get(headerDispatchID))
	if err != nil {
		return domain.Lease{}, fmt.Errorf("%w: bad or missing %s", domain.ErrValidation, headerDispatchID)
	}
	attempt, err := strconv.Atoi(r.Header.Get(headerDispatchAttempt))
	if err != nil || attempt <= 0 {
		return domain.Lease{}, fmt.Errorf("%w: bad or missing %s", domain.ErrValidation, headerDispatchAttempt)
	}
	return domain.Lease{DispatchID: dispatchID, Attempt: attempt}, nil
}

type CompleteExecutionRequest struct {
	Output json.RawMessage `json:"output"`
	State  json.RawMessage `json:"state,omitempty"`
}

type FailExecutionRequest struct {
	Error string `json:"error"`
}

type SuspendResponse struct {
	Suspended bool `json:"suspended"`
}

func (rt *Router) getStep(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	stepID := chi.URLParam(r, "step_id")
	step, err := rt.agent.GetStep(r.Context(), id, stepID)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, step, http.StatusOK)
}

func (rt *Router) listSteps(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	steps, err := rt.agent.ListSteps(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}

	if r.URL.Query().Get("status") == "terminal" {
		terminal := make([]domain.Step, 0, len(steps))
		for _, s := range steps {
			if s.Status.IsTerminal() {
				terminal = append(terminal, s)
			}
		}
		steps = terminal
	}
	WriteJSON(w, steps, http.StatusOK)
}

func (rt *Router) submitStep(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.SubmitStepRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Lease, err = leaseFrom(r); err != nil {
		WriteError(w, err)
		return
	}
	dec, err := rt.agent.SubmitStep(r.Context(), id, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
}

func (rt *Router) completeStep(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	stepID := chi.URLParam(r, "step_id")
	var req kernel.CompleteStepRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	req.Lease = lease
	dec, err := rt.agent.CompleteStep(r.Context(), id, stepID, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
}

func (rt *Router) failStep(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	stepID := chi.URLParam(r, "step_id")
	var req kernel.FailStepRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	req.Lease = lease
	dec, err := rt.agent.FailStep(r.Context(), id, stepID, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
}

func (rt *Router) heartbeat(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.agent.Heartbeat(r.Context(), id, lease); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) agentCompleteExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req CompleteExecutionRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.agent.CompleteExecution(r.Context(), id, lease, req.Output, req.State); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) agentFailExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req FailExecutionRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.agent.FailExecution(r.Context(), id, lease, req.Error); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) suspend(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	lease, err := leaseFrom(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	suspended, err := rt.agent.Suspend(r.Context(), id, lease)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, SuspendResponse{Suspended: suspended}, http.StatusOK)
}

func (rt *Router) registerResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.RegisterResourceRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Lease, err = leaseFrom(r); err != nil {
		WriteError(w, err)
		return
	}
	view, err := rt.agent.RegisterResource(r.Context(), id, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, view, http.StatusOK)
}

func (rt *Router) bindResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.BindResourceRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Lease, err = leaseFrom(r); err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.agent.BindResource(r.Context(), id, chi.URLParam(r, "key"), req); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) publishCheckpoints(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.PublishCheckpointsRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Lease, err = leaseFrom(r); err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.agent.PublishCheckpoints(r.Context(), id, req); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}
