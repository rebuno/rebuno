package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rebuno/rebuno/internal/domain"
)

func WriteJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func DecodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return domain.ErrValidation
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return domain.ErrValidation
	}
	return nil
}

func WriteError(w http.ResponseWriter, err error) {
	code, status := MapError(err)
	WriteJSON(w, domain.APIError{Code: code, Message: err.Error()}, status)
}

func MapError(err error) (string, int) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "not_found", http.StatusNotFound
	case errors.Is(err, domain.ErrConflict):
		return "conflict", http.StatusConflict
	case errors.Is(err, domain.ErrValidation):
		return "validation_error", http.StatusBadRequest
	case errors.Is(err, domain.ErrUnauthorized):
		return "unauthorized", http.StatusUnauthorized
	case errors.Is(err, domain.ErrStepIDMismatch):
		return "step_id_divergence", http.StatusConflict
	case errors.Is(err, domain.ErrExecutionTerminal):
		return "execution_terminal", http.StatusConflict
	default:
		return "internal_error", http.StatusInternalServerError
	}
}

func CtxWithValue(ctx context.Context, key, val any) context.Context {
	return context.WithValue(ctx, key, val)
}
