package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestWriteErrorFromErr(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   domain.ErrorCode
	}{
		{
			name:       "not found",
			err:        fmt.Errorf("%w: exec-1", domain.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   domain.CodeNotFound,
		},
		{
			name:       "validation",
			err:        fmt.Errorf("%w: agent_id required", domain.ErrValidation),
			wantStatus: http.StatusBadRequest,
			wantCode:   domain.CodeValidationError,
		},
		{
			name:       "conflict",
			err:        fmt.Errorf("%w: conflict", domain.ErrConflict),
			wantStatus: http.StatusConflict,
			wantCode:   domain.CodeConflict,
		},
		{
			name:       "terminal execution",
			err:        fmt.Errorf("%w: already completed", domain.ErrTerminalExecution),
			wantStatus: http.StatusConflict,
			wantCode:   domain.CodeConflict,
		},
		{
			name:       "step already resolved",
			err:        domain.ErrStepAlreadyResolved,
			wantStatus: http.StatusConflict,
			wantCode:   domain.CodeConflict,
		},
		{
			name:       "idempotency conflict",
			err:        domain.ErrIdempotencyConflict,
			wantStatus: http.StatusConflict,
			wantCode:   domain.CodeConflict,
		},
		{
			name:       "policy denied",
			err:        domain.ErrPolicyDenied,
			wantStatus: http.StatusForbidden,
			wantCode:   domain.CodeForbidden,
		},
		{
			name:       "rate limited",
			err:        domain.ErrRateLimited,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   domain.CodeRateLimited,
		},
		{
			name:       "session expired",
			err:        fmt.Errorf("%w: session-1", domain.ErrSessionExpired),
			wantStatus: http.StatusUnauthorized,
			wantCode:   domain.CodeUnauthorized,
		},
		{
			name:       "session not found",
			err:        fmt.Errorf("%w: session-1", domain.ErrSessionNotFound),
			wantStatus: http.StatusUnauthorized,
			wantCode:   domain.CodeUnauthorized,
		},
		{
			name:       "service unavailable",
			err:        fmt.Errorf("%w: db down", domain.ErrServiceUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   domain.CodeServiceUnavailable,
		},
		{
			name:       "execution tainted",
			err:        fmt.Errorf("%w: corrupt state", domain.ErrExecutionTainted),
			wantStatus: http.StatusConflict,
			wantCode:   domain.CodeConflict,
		},
		{
			name:       "unknown error becomes 500",
			err:        errors.New("something unexpected"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   domain.CodeInternalError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeErrorFromErr(w, tt.err)

			if w.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d", w.Code, tt.wantStatus)
			}

			var body domain.APIError
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to parse response body: %v", err)
			}
			if body.Code != tt.wantCode {
				t.Fatalf("code: got %s, want %s", body.Code, tt.wantCode)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Fatalf("expected ok, got %s", body["status"])
	}
}

func TestWriteJSON_UnmarshalableValue(t *testing.T) {
	w := httptest.NewRecorder()
	// A channel cannot be marshaled to JSON.
	writeJSON(w, http.StatusOK, make(chan int))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for unmarshalable value, got %d", w.Code)
	}
	var body domain.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse error body: %v", err)
	}
	if body.Code != domain.CodeInternalError {
		t.Fatalf("expected INTERNAL_ERROR, got %s", body.Code)
	}
}

func TestWriteError_RateLimitedSetsRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, "slow down")

	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After: 5, got %q", got)
	}
}

func TestWriteError_NonRateLimitedNoRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, domain.CodeValidationError, "bad input")

	if got := w.Header().Get("Retry-After"); got != "" {
		t.Fatalf("expected no Retry-After header, got %q", got)
	}
}

func TestWriteErrorFromErr_RunnerNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	writeErrorFromErr(w, domain.ErrRunnerNotFound)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body domain.APIError
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != domain.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", body.Code)
	}
}

func TestWriteErrorFromErr_InvalidTransition(t *testing.T) {
	w := httptest.NewRecorder()
	writeErrorFromErr(w, fmt.Errorf("%w: bad transition", domain.ErrInvalidTransition))

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	var body domain.APIError
	json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != domain.CodeConflict {
		t.Fatalf("expected CONFLICT, got %s", body.Code)
	}
}

func TestWriteErrorFromErr_NilError(t *testing.T) {
	// nil error should not panic; it falls through to the default 500 case.
	w := httptest.NewRecorder()
	writeErrorFromErr(w, nil)

	// errors.Is(nil, X) is always false, so it hits the default branch.
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for nil error, got %d", w.Code)
	}
}

func TestWriteErrorFromErr_WrappedChain(t *testing.T) {
	// Deeply wrapped error should still be detected.
	inner := fmt.Errorf("%w: session-abc", domain.ErrSessionExpired)
	outer := fmt.Errorf("agent disconnect: %w", inner)

	w := httptest.NewRecorder()
	writeErrorFromErr(w, outer)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for deeply-wrapped session expired, got %d", w.Code)
	}
}
