package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type AgentKernel interface {
	GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	GetStep(ctx context.Context, stepID string) (domain.Step, error)
	ListSteps(ctx context.Context, execID uuid.UUID) ([]domain.Step, error)
	SubmitStep(ctx context.Context, execID uuid.UUID, req kernel.SubmitStepRequest) (domain.StepDecision, error)
	CompleteStep(ctx context.Context, stepID string, req kernel.CompleteStepRequest) (domain.StepDecision, error)
	FailStep(ctx context.Context, stepID string, req kernel.FailStepRequest) (domain.StepDecision, error)
	CompleteExecution(ctx context.Context, execID uuid.UUID, output json.RawMessage) error
	FailExecution(ctx context.Context, execID uuid.UUID, reason string) error
}

type CompleteExecutionRequest struct {
	Output json.RawMessage `json:"output"`
}

type FailExecutionRequest struct {
	Error string `json:"error"`
}

func (rt *Router) getStep(w http.ResponseWriter, r *http.Request) {
	stepID := chi.URLParam(r, "step_id")
	step, err := rt.agent.GetStep(r.Context(), stepID)
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
	req.StepID = r.Header.Get("Rebuno-Step-Id")
	dec, err := rt.agent.SubmitStep(r.Context(), id, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
}

func (rt *Router) completeStep(w http.ResponseWriter, r *http.Request) {
	stepID := chi.URLParam(r, "step_id")
	var req kernel.CompleteStepRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	dec, err := rt.agent.CompleteStep(r.Context(), stepID, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
}

func (rt *Router) failStep(w http.ResponseWriter, r *http.Request) {
	stepID := chi.URLParam(r, "step_id")
	var req kernel.FailStepRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	dec, err := rt.agent.FailStep(r.Context(), stepID, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, dec, http.StatusOK)
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
	if err := rt.agent.CompleteExecution(r.Context(), id, req.Output); err != nil {
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
	if err := rt.agent.FailExecution(r.Context(), id, req.Error); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}
