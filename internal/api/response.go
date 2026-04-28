package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rebuno/rebuno/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"internal encoding error","code":"INTERNAL_ERROR"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
	w.Write([]byte("\n"))
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func writeError(w http.ResponseWriter, status int, code domain.ErrorCode, message string) {
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "5")
	}
	writeJSON(w, status, &domain.APIError{
		Message: message,
		Code:    code,
	})
}

func writeErrorFromErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound),
		errors.Is(err, domain.ErrRunnerNotFound):
		writeError(w, http.StatusNotFound, domain.CodeNotFound, err.Error())
	case errors.Is(err, domain.ErrConflict),
		errors.Is(err, domain.ErrStepAlreadyResolved),
		errors.Is(err, domain.ErrTerminalExecution),
		errors.Is(err, domain.ErrInvalidTransition),
		errors.Is(err, domain.ErrExecutionTainted),
		errors.Is(err, domain.ErrIdempotencyConflict),
		errors.Is(err, domain.ErrExecutionBlocked):
		writeError(w, http.StatusConflict, domain.CodeConflict, err.Error())
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, domain.CodeValidationError, err.Error())
	case errors.Is(err, domain.ErrPolicyDenied):
		writeError(w, http.StatusForbidden, domain.CodeForbidden, err.Error())
	case errors.Is(err, domain.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, err.Error())
	case errors.Is(err, domain.ErrSessionExpired),
		errors.Is(err, domain.ErrSessionNotFound):
		writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized, err.Error())
	case errors.Is(err, domain.ErrServiceUnavailable):
		writeError(w, http.StatusServiceUnavailable, domain.CodeServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, domain.CodeInternalError, "internal error")
	}
}
