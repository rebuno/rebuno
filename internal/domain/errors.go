package domain

import "fmt"

var (
	ErrNotFound          = fmt.Errorf("not found")
	ErrConflict          = fmt.Errorf("conflict")
	ErrValidation        = fmt.Errorf("validation error")
	ErrExecutionTerminal = fmt.Errorf("execution terminal")
	ErrStepIDMismatch    = fmt.Errorf("step id mismatch")
	ErrUnauthorized      = fmt.Errorf("unauthorized")
	ErrForbidden         = fmt.Errorf("forbidden")
	ErrRateLimited       = fmt.Errorf("rate limit exceeded")
)
