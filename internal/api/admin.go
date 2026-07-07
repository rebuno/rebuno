package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/kernel"
)

type AdminKernel interface {
	Register(ctx context.Context, agent domain.Agent) error
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
	ListAgents(ctx context.Context) ([]domain.Agent, error)
	DeleteAgent(ctx context.Context, id string) error
	LoadPolicyBundle(ctx context.Context, agentID string, bundle string) error
	ListPendingApprovals(ctx context.Context) ([]domain.Approval, error)
	GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error)
	GrantApproval(ctx context.Context, id uuid.UUID, req kernel.GrantApprovalRequest) error
	DenyApproval(ctx context.Context, id uuid.UUID, req kernel.DenyApprovalRequest) error
}

type AgentRegistrationRequest struct {
	ID         string `json:"id"`
	WebhookURL string `json:"webhook_url"`
	Secret     string `json:"secret"`
}

type LoadPolicyRequest struct {
	Bundle string `json:"bundle"` // raw YAML text
}

func (rt *Router) registerAgent(w http.ResponseWriter, r *http.Request) {
	var req AgentRegistrationRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	agent := domain.Agent{ID: req.ID, WebhookURL: req.WebhookURL, Secret: req.Secret}
	if err := rt.admin.Register(r.Context(), agent); err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, agent, http.StatusCreated)
}

func (rt *Router) getAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, err := rt.admin.GetAgent(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	agent.Secret = ""
	WriteJSON(w, agent, http.StatusOK)
}

func (rt *Router) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := rt.admin.ListAgents(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	for i := range agents {
		agents[i].Secret = ""
	}
	WriteJSON(w, agents, http.StatusOK)
}

func (rt *Router) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := rt.admin.DeleteAgent(r.Context(), id); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) loadPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "agent_id")
	var req LoadPolicyRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.admin.LoadPolicyBundle(r.Context(), id, req.Bundle); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) listApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := rt.admin.ListPendingApprovals(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, approvals, http.StatusOK)
}

func (rt *Router) getApproval(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	approval, err := rt.admin.GetApproval(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, approval, http.StatusOK)
}

func (rt *Router) grantApproval(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.GrantApprovalRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.admin.GrantApproval(r.Context(), id, req); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}

func (rt *Router) denyApproval(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	var req kernel.DenyApprovalRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if err := rt.admin.DenyApproval(r.Context(), id, req); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}
