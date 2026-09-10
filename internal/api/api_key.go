package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type APIKeyKernel interface {
	CreateAPIKey(context.Context, kernel.CreateAPIKeyRequest) (kernel.IssuedAPIKey, error)
	ListAPIKeys(context.Context) ([]domain.APIKey, error)
	RotateAPIKey(context.Context, string) (kernel.IssuedAPIKey, error)
	RevokeAPIKey(context.Context, string) error
	AuthenticateAPIKey(context.Context, string) (domain.APIKey, error)
}

func (rt *Router) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req kernel.CreateAPIKeyRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	key, err := rt.admin.CreateAPIKey(r.Context(), req)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, key, http.StatusCreated)
}

func (rt *Router) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := rt.admin.ListAPIKeys(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, keys, http.StatusOK)
}

func (rt *Router) rotateAPIKey(w http.ResponseWriter, r *http.Request) {
	key, err := rt.admin.RotateAPIKey(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, key, http.StatusOK)
}

func (rt *Router) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := rt.admin.RevokeAPIKey(r.Context(), chi.URLParam(r, "id")); err != nil {
		WriteError(w, err)
		return
	}
	WriteNoContent(w)
}
