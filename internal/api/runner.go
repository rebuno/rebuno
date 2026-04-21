package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
)

type runnerHandlers struct {
	kernel *kernel.Kernel
	hub    *hub.RunnerHub
}

type submitResultRequest struct {
	JobID       string          `json:"job_id"`
	ExecutionID string          `json:"execution_id"`
	StepID      string          `json:"step_id"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data"`
	Error       string          `json:"error"`
	Retryable   bool            `json:"retryable"`
	StartedAt   *time.Time      `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	ConsumerID  string          `json:"consumer_id"`
}

func (h *runnerHandlers) submitResult(w http.ResponseWriter, r *http.Request) {
	runnerID := chi.URLParam(r, "id")
	var req submitResultRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.JobID == "" || req.ExecutionID == "" || req.StepID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: job_id, execution_id, and step_id are required", domain.ErrValidation))
		return
	}

	result := domain.JobResult{
		JobID:       req.JobID,
		ExecutionID: req.ExecutionID,
		StepID:      req.StepID,
		Success:     req.Success,
		Data:        req.Data,
		Error:       req.Error,
		Retryable:   req.Retryable,
		StartedAt:   req.StartedAt,
		CompletedAt: req.CompletedAt,
		RunnerID:    runnerID,
		ConsumerID:  req.ConsumerID,
	}

	err := h.kernel.SubmitJobResult(r.Context(), result)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type stepStartedRequest struct {
	ExecutionID string `json:"execution_id"`
	RunnerID    string `json:"runner_id"`
}

func (h *runnerHandlers) stepStarted(w http.ResponseWriter, r *http.Request) {
	stepID := chi.URLParam(r, "stepId")
	var req stepStartedRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.ExecutionID == "" || req.RunnerID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: execution_id and runner_id are required", domain.ErrValidation))
		return
	}

	err := h.kernel.RecordStepStarted(r.Context(), req.ExecutionID, stepID, req.RunnerID)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *runnerHandlers) unregister(w http.ResponseWriter, r *http.Request) {
	runnerID := chi.URLParam(r, "id")
	err := h.kernel.UnregisterRunner(r.Context(), runnerID)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}
	writeNoContent(w)
}

type updateCapabilitiesRequest struct {
	Tools []string `json:"tools"`
}

func (h *runnerHandlers) updateCapabilities(w http.ResponseWriter, r *http.Request) {
	runnerID := chi.URLParam(r, "id")
	var req updateCapabilitiesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}

	h.hub.UpdateCapabilities(runnerID, req.Tools)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
