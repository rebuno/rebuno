package domain

import "errors"

var (
	ErrNotFound             = errors.New("not found")
	ErrConflict             = errors.New("conflict")
	ErrValidation           = errors.New("validation error")
	ErrInternal             = errors.New("internal error")
	ErrInvalidTransition    = errors.New("invalid state transition")
	ErrTerminalExecution    = errors.New("execution is in terminal state")
	ErrSessionExpired       = errors.New("session expired")
	ErrSessionNotFound      = errors.New("session not found")
	ErrExecutionTainted     = errors.New("execution is tainted")
	ErrPolicyDenied         = errors.New("policy denied")
	ErrRunnerNotFound       = errors.New("runner not found")
	ErrStepAlreadyResolved  = errors.New("step already resolved")
	ErrDuplicatePriority    = errors.New("duplicate rule priority")
	ErrInvalidConfiguration = errors.New("invalid configuration")
	ErrServiceUnavailable   = errors.New("service unavailable")
	ErrIdempotencyConflict  = errors.New("idempotency conflict")
	ErrRateLimited          = errors.New("rate limited")
)

type ErrorCode string

const (
	CodeValidationError    ErrorCode = "VALIDATION_ERROR"
	CodeNotFound           ErrorCode = "NOT_FOUND"
	CodeConflict           ErrorCode = "CONFLICT"
	CodeUnauthorized       ErrorCode = "UNAUTHORIZED"
	CodeForbidden          ErrorCode = "FORBIDDEN"
	CodeRateLimited        ErrorCode = "RATE_LIMITED"
	CodeInternalError      ErrorCode = "INTERNAL_ERROR"
	CodeServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
)

type APIError struct {
	Message string    `json:"error"`
	Code    ErrorCode `json:"code"`
	Details any       `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return e.Message
}
