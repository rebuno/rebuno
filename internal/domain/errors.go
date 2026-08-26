package domain

import "fmt"

var (
	ErrNotFound          = fmt.Errorf("not found")
	ErrConflict          = fmt.Errorf("conflict")
	ErrValidation        = fmt.Errorf("validation error")
	ErrExecutionTerminal = fmt.Errorf("execution terminal")
	ErrUnauthorized      = fmt.Errorf("unauthorized")
	ErrForbidden         = fmt.Errorf("forbidden")
	ErrRateLimited       = fmt.Errorf("rate limit exceeded")
)

const (
	ReasonPolicyDenied           = "policy_denied"
	ReasonBudgetExceeded         = "execution_token_budget_exceeded"
	ReasonApprovalTimeout        = "approval_timeout"
	ReasonIndeterminate          = "indeterminate"
	ReasonRateLimited            = "rate_limit_exceeded"
	ReasonRateLimiterUnavailable = "rate_limiter_unavailable"
	ReasonDispatchExhausted      = "dispatch_exhausted"
)
