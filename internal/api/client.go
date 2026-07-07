package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

const maxEventsPageLimit = 1000

type ClientKernel interface {
	CreateExecution(ctx context.Context, agentID string, input json.RawMessage, version string) (domain.Execution, error)
	GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error)
	GetEvents(ctx context.Context, id uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error)
	CancelExecution(ctx context.Context, id uuid.UUID) error
}

type CreateExecutionRequest struct {
	AgentID      string          `json:"agent_id"`
	Input        json.RawMessage `json:"input"`
	AgentVersion string          `json:"agent_version,omitempty"`
}

func (rt *Router) createExecution(w http.ResponseWriter, r *http.Request) {
	var req CreateExecutionRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	exec, err := rt.client.CreateExecution(r.Context(), req.AgentID, req.Input, req.AgentVersion)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, exec, http.StatusCreated)
}

func (rt *Router) listExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := domain.ExecutionFilter{
		AgentID: q.Get("agent_id"),
		Status:  domain.ExecutionStatus(q.Get("status")),
		Cursor:  q.Get("cursor"),
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			WriteError(w, domain.ErrValidation)
			return
		}
		filter.Limit = n
	}
	// The cursor is an execution id; reject garbage so it can't reach the query.
	if filter.Cursor != "" {
		if _, err := uuid.Parse(filter.Cursor); err != nil {
			WriteError(w, domain.ErrValidation)
			return
		}
	}
	page, err := rt.client.ListExecutions(r.Context(), filter)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, page, http.StatusOK)
}

func (rt *Router) getExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	exec, err := rt.client.GetExecution(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, exec, http.StatusOK)
}

func (rt *Router) getEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > maxEventsPageLimit {
		limit = maxEventsPageLimit
	}
	events, err := rt.client.GetEvents(r.Context(), id, afterSeq, limit)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, events, http.StatusOK)
}

func (rt *Router) cancelExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	if err := rt.client.CancelExecution(r.Context(), id); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}
