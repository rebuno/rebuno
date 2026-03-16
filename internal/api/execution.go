package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type executionHandlers struct {
	kernel *kernel.Kernel
}

type createExecutionRequest struct {
	AgentID string            `json:"agent_id"`
	Input   json.RawMessage   `json:"input"`
	Labels  map[string]string `json:"labels"`
}

func (h *executionHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createExecutionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if err := validateStringLength("agent_id", req.AgentID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.AgentID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: agent_id is required", domain.ErrValidation))
		return
	}

	execID, err := h.kernel.CreateExecution(r.Context(), kernel.CreateExecutionRequest{
		AgentID: req.AgentID,
		Input:   req.Input,
		Labels:  req.Labels,
	})
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	state, err := h.kernel.GetExecution(r.Context(), execID)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, state.Execution)
}

func (h *executionHandlers) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	state, err := h.kernel.GetExecution(r.Context(), id)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state.Execution)
}

func (h *executionHandlers) list(w http.ResponseWriter, r *http.Request) {
	statusStr := queryString(r, "status")
	if statusStr != "" {
		switch domain.ExecutionStatus(statusStr) {
		case domain.ExecutionPending, domain.ExecutionRunning, domain.ExecutionBlocked,
			domain.ExecutionCompleted, domain.ExecutionFailed, domain.ExecutionCancelled:
		default:
			writeErrorFromErr(w, fmt.Errorf("%w: invalid status %q", domain.ErrValidation, statusStr))
			return
		}
	}
	filter := domain.ExecutionFilter{
		Status:  domain.ExecutionStatus(statusStr),
		AgentID: queryString(r, "agent_id"),
	}
	cursor := queryString(r, "cursor")
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}

	executions, nextCursor, err := h.kernel.ListExecutions(r.Context(), filter, cursor, limit)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	if executions == nil {
		executions = []domain.ExecutionSummary{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"executions":  executions,
		"next_cursor": nextCursor,
	})
}

func (h *executionHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := h.kernel.CancelExecution(r.Context(), id)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	state, err := h.kernel.GetExecution(r.Context(), id)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state.Execution)
}

type sendSignalRequest struct {
	SignalType string          `json:"signal_type"`
	Payload    json.RawMessage `json:"payload"`
}

func (h *executionHandlers) sendSignal(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req sendSignalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if req.SignalType == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: signal_type is required", domain.ErrValidation))
		return
	}

	err := h.kernel.SendSignal(r.Context(), id, req.SignalType, req.Payload)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var terminalEventTypes = map[domain.EventType]bool{
	domain.EventExecutionCompleted: true,
	domain.EventExecutionFailed:    true,
	domain.EventExecutionCancelled: true,
}

func (h *executionHandlers) streamEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	afterSequence := int64(0)
	if v := r.URL.Query().Get("after_sequence"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			afterSequence = parsed
		}
	}

	if _, err := h.kernel.GetExecution(r.Context(), id); err != nil {
		writeErrorFromErr(w, err)
		return
	}

	flusher, ok := initSSE(w)
	if !ok {
		return
	}

	notify, unwatch := h.kernel.WatchExecution(id)
	defer unwatch()

	lastSeq := afterSequence

	sendNewEvents := func() (bool, error) {
		events, err := h.kernel.GetEvents(r.Context(), id, lastSeq, 1000)
		if err != nil {
			return false, err
		}
		terminal := false
		for _, evt := range events {
			data, err := json.Marshal(evt)
			if err != nil {
				return false, err
			}
			seqStr := strconv.FormatInt(evt.Sequence, 10)
			if err := writeSSEEvent(w, string(evt.Type), data, seqStr); err != nil {
				return false, err
			}
			lastSeq = evt.Sequence
			if terminalEventTypes[evt.Type] {
				terminal = true
			}
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		return terminal, nil
	}

	if terminal, err := sendNewEvents(); err != nil || terminal {
		return
	}

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-notify:
			if terminal, err := sendNewEvents(); err != nil || terminal {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ":heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *executionHandlers) getEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	afterSequence := int64(queryInt(r, "after_sequence", 0))
	limit := queryInt(r, "limit", 100)
	if limit > 1000 {
		limit = 1000
	}

	events, err := h.kernel.GetEvents(r.Context(), id, afterSequence, limit)
	if err != nil {
		writeErrorFromErr(w, err)
		return
	}

	if events == nil {
		events = []domain.Event{}
	}

	var latestSeq int64
	if len(events) > 0 {
		latestSeq = events[len(events)-1].Sequence
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events":          events,
		"latest_sequence": latestSeq,
	})
}
