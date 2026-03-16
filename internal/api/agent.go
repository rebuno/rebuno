package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type agentHandlers struct {
	kernel *kernel.Kernel
}

type intentRequest struct {
	ExecutionID string        `json:"execution_id"`
	SessionID   string        `json:"session_id"`
	Intent      domain.Intent `json:"intent"`
}

func (h *agentHandlers) submitIntent(w http.ResponseWriter, r *http.Request) {
	var req intentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.ExecutionID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: execution_id is required", domain.ErrValidation))
		return
	}
	if req.SessionID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: session_id is required", domain.ErrValidation))
		return
	}
	if req.Intent.Type == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: intent.type is required", domain.ErrValidation))
		return
	}
	if err := validateStringLength("tool_id", req.Intent.ToolID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if err := validateStringLength("error", req.Intent.Error, 4096); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if err := validateStringLength("signal_type", req.Intent.SignalType, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}

	result, err := h.kernel.ProcessIntent(r.Context(), domain.IntentRequest{
		ExecutionID: req.ExecutionID,
		SessionID:   req.SessionID,
		Intent:      req.Intent,
	})
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

type stepResultRequest struct {
	ExecutionID string          `json:"execution_id"`
	SessionID   string          `json:"session_id"`
	StepID      string          `json:"step_id"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (h *agentHandlers) stepResult(w http.ResponseWriter, r *http.Request) {
	var req stepResultRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.ExecutionID == "" || req.SessionID == "" || req.StepID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: execution_id, session_id, and step_id are required", domain.ErrValidation))
		return
	}

	err := h.kernel.SubmitStepResult(r.Context(), kernel.StepResultRequest{
		ExecutionID: req.ExecutionID,
		SessionID:   req.SessionID,
		StepID:      req.StepID,
		Success:     req.Success,
		Data:        req.Data,
		Error:       req.Error,
	})
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
